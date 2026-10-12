package jwks

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"sync"
	"time"

	"github.com/oleg-tkachuk/limes/internal/httpx"
)

// ErrUnavailable is returned by Remote when it holds no key
// set recent enough to trust: the endpoint has been failing for longer than
// MaxStale, or never answered. The verifier refuses the token with it — an
// issuer we cannot reach verifies nothing — and, being the server's fault
// rather than the token's, it matches no ErrUnauthenticated sentinel.
var ErrUnavailable = errors.New("jwks: key set unavailable")

// RemoteConfig defaults, for a field left zero.
const (
	DefaultFetchTimeout       = 10 * time.Second
	DefaultRefreshInterval    = 5 * time.Minute
	DefaultMinRefreshInterval = 10 * time.Second
	DefaultMaxStale           = time.Hour
	DefaultMaxDocumentBytes   = 1 << 20
)

// RemoteConfig configures a Remote. Only URL is required.
// The intervals must nest: MinRefreshInterval ≤ RefreshInterval ≤ MaxStale.
type RemoteConfig struct {
	// URL of the issuer's JWKS document, e.g.
	// https://issuer.example/.well-known/jwks.json.
	URL string
	// Client performs the fetch. Default: a client with a
	// DefaultFetchTimeout timeout. A fetch runs apart from the caller that
	// started it, and is bounded by DefaultFetchTimeout whatever the
	// client allows.
	Client *http.Client
	// RefreshInterval is how long a fetched key set is used before it is
	// re-fetched, unless the response's Cache-Control says a shorter time
	// (RFC 9111): its max-age, less its Age, never below
	// MinRefreshInterval. Default DefaultRefreshInterval.
	RefreshInterval time.Duration
	// MinRefreshInterval rate-limits fetches. A token with an unknown kid
	// triggers a refetch (that is how a newly published key is picked up),
	// but at most once per this interval, so a stream of tokens with
	// made-up kids cannot turn the verifier into a request amplifier
	// against the issuer. Default DefaultMinRefreshInterval.
	MinRefreshInterval time.Duration
	// MaxStale is how long the last good key set keeps being served while
	// fetches fail. Past it the resolver fails closed with
	// ErrUnavailable. Default DefaultMaxStale — comfortably longer than the
	// capability TTLs, shorter than a key's useful life after withdrawal.
	MaxStale time.Duration
	// MaxDocumentBytes caps the response body. Default
	// DefaultMaxDocumentBytes.
	MaxDocumentBytes int
	// Now is the clock; tests override.
	Now func() time.Time
}

// Remote is a KeyResolver backed by an issuer's JWKS endpoint,
// for verifiers that run apart from the issuer. It caches the key set,
// revalidates with ETag, picks up a rotated-in key on first sight of its kid,
// and keeps serving the last good set through a bounded outage.
type Remote struct {
	cfg RemoteConfig

	// fetching holds one token while a fetch is in flight. It is a channel
	// rather than a mutex so that a caller waiting behind another's fetch
	// can give up when its own context does.
	fetching chan struct{}

	mu          sync.RWMutex
	keys        map[string]ed25519.PublicKey
	etag        string
	fetchedAt   time.Time     // last time the set was confirmed current
	freshFor    time.Duration // how long after fetchedAt it stays current
	lastAttempt time.Time
	lastErr     error
}

// NewRemote validates the config. It does not fetch: the first
// PublicKey call does, so constructing a verifier never blocks on the network.
func NewRemote(cfg RemoteConfig) (*Remote, error) {
	if cfg.URL == "" {
		return nil, errors.New("jwks: RemoteConfig.URL required")
	}
	if cfg.RefreshInterval < 0 || cfg.MinRefreshInterval < 0 || cfg.MaxStale < 0 || cfg.MaxDocumentBytes < 0 {
		return nil, errors.New("jwks: RemoteConfig intervals and MaxDocumentBytes must not be negative")
	}
	if cfg.Client == nil {
		cfg.Client = &http.Client{Timeout: DefaultFetchTimeout}
	}
	if cfg.RefreshInterval == 0 {
		cfg.RefreshInterval = DefaultRefreshInterval
	}
	if cfg.MinRefreshInterval == 0 {
		cfg.MinRefreshInterval = DefaultMinRefreshInterval
	}
	if cfg.MaxStale == 0 {
		cfg.MaxStale = DefaultMaxStale
	}
	if cfg.MaxDocumentBytes == 0 {
		cfg.MaxDocumentBytes = DefaultMaxDocumentBytes
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	// A set past MaxStale before it is due a refresh would fail closed
	// with no fetch having failed, and a refresh due inside the rate limit
	// would wait it out anyway.
	if cfg.MinRefreshInterval > cfg.RefreshInterval || cfg.RefreshInterval > cfg.MaxStale {
		return nil, fmt.Errorf("jwks: RemoteConfig needs MinRefreshInterval (%s) ≤ RefreshInterval (%s) ≤ MaxStale (%s)",
			cfg.MinRefreshInterval, cfg.RefreshInterval, cfg.MaxStale)
	}
	return &Remote{cfg: cfg, fetching: make(chan struct{}, 1)}, nil
}

// PublicKey implements KeyResolver.
func (r *Remote) PublicKey(ctx context.Context, kid string) (ed25519.PublicKey, error) {
	key, ok, fetchedAt, freshFor := r.lookup(kid)
	if !ok || r.cfg.Now().Sub(fetchedAt) >= freshFor {
		if err := r.refresh(ctx); err != nil {
			return nil, err
		}
		key, ok, fetchedAt, _ = r.lookup(kid)
	}

	if fetchedAt.IsZero() || r.cfg.Now().Sub(fetchedAt) > r.cfg.MaxStale {
		r.mu.RLock()
		lastErr := r.lastErr
		r.mu.RUnlock()
		return nil, fmt.Errorf("%w: %s: %w", ErrUnavailable, r.cfg.URL, lastErr)
	}
	if !ok {
		return nil, ErrUnknownKID
	}
	return key, nil
}

func (r *Remote) lookup(kid string) (ed25519.PublicKey, bool, time.Time, time.Duration) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	k, ok := r.keys[kid]
	return k, ok, r.fetchedAt, r.freshFor
}

// freshness is how long a response keeps the set current: what its
// Cache-Control says, within MinRefreshInterval..RefreshInterval, or
// RefreshInterval when it says nothing.
func (r *Remote) freshness(h http.Header) time.Duration {
	d, ok := responseFreshness(h)
	if !ok {
		return r.cfg.RefreshInterval
	}
	return min(max(d, r.cfg.MinRefreshInterval), r.cfg.RefreshInterval)
}

// refresh fetches the document unless a fetch was attempted within
// MinRefreshInterval. Failures are recorded, not returned: the caller decides
// from the age of the set it still holds. It returns only ctx's error, when
// the caller gave up — waiting behind another fetch, or during its own.
//
// The fetch runs apart from the caller, bounded by DefaultFetchTimeout
// rather than by the caller's context, and is recorded when it ends. A
// caller that gives up therefore neither makes the endpoint look failed to
// everyone else nor escapes MinRefreshInterval: it leaves a fetch in
// flight, which the next caller waits for instead of starting another.
func (r *Remote) refresh(ctx context.Context) error {
	select {
	case r.fetching <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	release := func() { <-r.fetching }
	// select picks at random between two ready cases: a caller already
	// done may have won the slot.
	if err := ctx.Err(); err != nil {
		release()
		return err
	}

	now := r.cfg.Now()
	r.mu.RLock()
	recent := !r.lastAttempt.IsZero() && now.Sub(r.lastAttempt) < r.cfg.MinRefreshInterval
	etag := r.etag
	r.mu.RUnlock()
	if recent {
		release()
		return nil
	}

	done := make(chan struct{})
	go func() {
		defer release()
		defer close(done)
		fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), DefaultFetchTimeout)
		defer cancel()
		keys, newETag, header, notModified, err := r.fetch(fetchCtx, etag)
		r.record(now, keys, newETag, header, notModified, err)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// record keeps what a fetch attempted at now found.
func (r *Remote) record(now time.Time, keys map[string]ed25519.PublicKey, etag string, header http.Header, notModified bool, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lastAttempt = now
	r.lastErr = err
	if err != nil {
		return
	}
	r.fetchedAt = now
	r.freshFor = r.freshness(header)
	if !notModified {
		r.keys = keys
		r.etag = etag
	}
}

// JWKS media types (RFC 7517 §8.5), the registered one first: some servers
// still label a key set as plain JSON.
const (
	mediaTypeJWKSet = "application/jwk-set+json"
	acceptJWKSet    = mediaTypeJWKSet + ", application/json;q=0.9"
)

func (r *Remote) fetch(ctx context.Context, etag string) (keys map[string]ed25519.PublicKey, newETag string, header http.Header, notModified bool, err error) {
	// The URL is the operator's configuration, not a request's input.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.cfg.URL, nil) //nolint:gosec // G704: see above
	if err != nil {
		return nil, "", nil, false, err
	}
	req.Header.Set(httpx.HeaderAccept, acceptJWKSet)
	if etag != "" {
		req.Header.Set(httpx.HeaderIfNoneMatch, etag)
	}
	resp, err := r.cfg.Client.Do(req) //nolint:gosec // G704: the configured URL
	if err != nil {
		return nil, "", nil, false, err
	}
	defer func() { _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode == http.StatusNotModified && etag != "":
		return nil, etag, resp.Header, true, nil
	case resp.StatusCode != http.StatusOK:
		return nil, "", nil, false, fmt.Errorf("JWKS fetch: HTTP %d", resp.StatusCode)
	}
	body, err := readCapped(resp.Body, r.cfg.MaxDocumentBytes)
	if err != nil {
		return nil, "", nil, false, err
	}
	keys, skipped, err := parse(body)
	if err != nil {
		return nil, "", nil, false, err
	}
	// Entries, none usable: a broken document, not a withdrawal of every
	// key — failing the fetch keeps the last good set within MaxStale.
	if len(keys) == 0 && skipped > 0 {
		return nil, "", nil, false, fmt.Errorf("JWKS document has %d entries and no usable Ed25519 key", skipped)
	}
	return keys, resp.Header.Get(httpx.HeaderETag), resp.Header, false, nil
}

// readCapped reads all of body, refusing more than maxBytes. It reads one
// byte past maxBytes to tell a document of exactly that size from a longer
// one; at math.MaxInt64 none is longer, and one past it would overflow.
func readCapped(body io.Reader, maxBytes int) ([]byte, error) {
	limit := int64(maxBytes)
	if limit < math.MaxInt64 {
		limit++
	}
	b, err := io.ReadAll(io.LimitReader(body, limit))
	if err != nil {
		return nil, fmt.Errorf("JWKS read: %w", err)
	}
	if len(b) > maxBytes {
		return nil, fmt.Errorf("JWKS document exceeds %d bytes", maxBytes)
	}
	return b, nil
}
