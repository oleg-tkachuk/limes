package jwks

import (
	"cmp"
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"github.com/oleg-tkachuk/limes/internal/errs"
	"io"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/oleg-tkachuk/limes/internal/httpx"
)

// ErrUnavailable is returned by Remote when it holds no key set recent
// enough to trust — the endpoint has been failing for longer than MaxStale,
// or never answered —, when the kid asked for is not in the set it holds
// while the endpoint is failing, since the issuer may have rotated it in
// since, and when the caller's context ends while it waits on the endpoint
// with no set to serve; the context's error stays matchable. The verifier
// refuses the token with it — an issuer we cannot reach verifies nothing —
// and, being the server's fault rather than the token's, it matches no
// ErrUnauthenticated sentinel.
var ErrUnavailable = errors.New("jwks: key set unavailable")

// RemoteConfig defaults, for a field left zero.
const (
	DefaultFetchTimeout       = 10 * time.Second
	DefaultRefreshInterval    = 5 * time.Minute
	DefaultMinRefreshInterval = 10 * time.Second
	DefaultMaxStale           = time.Hour
	DefaultMaxDocumentBytes   = 1 << 20
)

// schemeHTTPS is the only scheme a key set is fetched over: over anything
// else whoever sits on the path chooses the keys the verifier trusts.
const schemeHTTPS = "https"

// maxRedirects is how many redirects a fetch follows, net/http's own default
// limit, for a Client that sets no CheckRedirect of its own.
const maxRedirects = 10

// RemoteConfig configures a Remote. Only URL is required.
// The intervals must nest: MinRefreshInterval ≤ RefreshInterval ≤ MaxStale.
type RemoteConfig struct {
	// URL of the issuer's JWKS document, e.g.
	// https://issuer.example/.well-known/jwks.json. It must be https.
	URL string
	// Client performs the fetch. Default: a client with a
	// DefaultFetchTimeout timeout. A fetch runs apart from the caller that
	// started it, and is bounded by DefaultFetchTimeout whatever the
	// client allows. Whatever the client's own redirect policy, a redirect
	// to another scheme or host is refused.
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
	// endpoint is cfg.URL with any password masked, the form an error
	// names: an error is what reaches a log.
	endpoint string

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
		return nil, errs.Config("jwks: RemoteConfig.URL required")
	}
	u, err := parseRemoteURL(cfg.URL)
	if err != nil {
		return nil, err
	}
	if cfg.RefreshInterval < 0 || cfg.MinRefreshInterval < 0 || cfg.MaxStale < 0 || cfg.MaxDocumentBytes < 0 {
		return nil, errs.Config("jwks: RemoteConfig intervals and MaxDocumentBytes must not be negative")
	}
	setRemoteDefaults(&cfg)
	// A set past MaxStale before it is due a refresh would fail closed
	// with no fetch having failed, and a refresh due inside the rate limit
	// would wait it out anyway.
	if cfg.MinRefreshInterval > cfg.RefreshInterval || cfg.RefreshInterval > cfg.MaxStale {
		return nil, errs.Config("jwks: RemoteConfig needs MinRefreshInterval (%s) ≤ RefreshInterval (%s) ≤ MaxStale (%s)",
			cfg.MinRefreshInterval, cfg.RefreshInterval, cfg.MaxStale)
	}
	return &Remote{cfg: cfg, endpoint: u.Redacted(), fetching: make(chan struct{}, 1)}, nil
}

// parseRemoteURL reads the JWKS endpoint's URL, refusing one that is not
// an https URL with a host.
func parseRemoteURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		// A *url.Error quotes the whole URL, password and all: only the
		// reason is kept.
		if ue, ok := errors.AsType[*url.Error](err); ok {
			err = ue.Err
		}
		return nil, fmt.Errorf("jwks: RemoteConfig.URL: %w", err)
	}
	if u.Scheme != schemeHTTPS || u.Host == "" {
		return nil, errs.Config("jwks: RemoteConfig.URL must be an %s URL, got %q", schemeHTTPS, u.Redacted())
	}
	return u, nil
}

// setRemoteDefaults gives cfg's unset members their defaults, and its
// client the same-origin redirect policy.
func setRemoteDefaults(cfg *RemoteConfig) {
	if cfg.Client == nil {
		cfg.Client = &http.Client{Timeout: DefaultFetchTimeout}
	}
	cfg.Client = sameOrigin(cfg.Client)
	cfg.RefreshInterval = cmp.Or(cfg.RefreshInterval, DefaultRefreshInterval)
	cfg.MinRefreshInterval = cmp.Or(cfg.MinRefreshInterval, DefaultMinRefreshInterval)
	cfg.MaxStale = cmp.Or(cfg.MaxStale, DefaultMaxStale)
	cfg.MaxDocumentBytes = cmp.Or(cfg.MaxDocumentBytes, DefaultMaxDocumentBytes)
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
}

// sameOrigin is a shallow copy of c, so the caller's client is left as it
// is, whose redirects keep to the scheme and host of the first request: a
// redirect is the server's choice, and one to http or to another host would
// let it, or whoever can steer it, pick the keys. Within that, c's own policy
// decides, or net/http's default when it has none.
func sameOrigin(c *http.Client) *http.Client {
	own := c.CheckRedirect
	out := *c
	out.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		first := via[0].URL
		if req.URL.Scheme != schemeHTTPS || !strings.EqualFold(req.URL.Host, first.Host) {
			return fmt.Errorf("jwks: redirect from %s to %s leaves the origin", first.Redacted(), req.URL.Redacted())
		}
		if own != nil {
			return own(req, via)
		}
		if len(via) >= maxRedirects {
			return fmt.Errorf("jwks: stopped after %d redirects", maxRedirects)
		}
		return nil
	}
	return &out
}

// PublicKey implements limes.KeyResolver. A known key from a set past its
// freshness but within MaxStale is served at once, its refresh started behind
// it: a slow endpoint delays no request while the set it holds is still good.
// A caller waits on the endpoint only for a kid the set does not hold, or a
// set it cannot serve.
func (r *Remote) PublicKey(ctx context.Context, kid string) (ed25519.PublicKey, error) {
	key, ok, fetchedAt, freshFor := r.lookup(kid)
	age := r.cfg.Now().Sub(fetchedAt)
	if ok && age < freshFor {
		return slices.Clone(key), nil
	}
	if ok && age <= r.cfg.MaxStale {
		r.refreshBehind(ctx)
		return slices.Clone(key), nil
	}
	if err := r.refresh(ctx); err != nil {
		// The caller gave up waiting on the endpoint with no set it could
		// serve: the keys are unavailable to it, the server's fault rather
		// than the token's. The context's error stays matchable.
		return nil, fmt.Errorf("%w: %s: %w", ErrUnavailable, r.endpoint, err)
	}
	return r.refreshedKey(kid)
}

// refreshedKey is kid's key from the set a refresh left: unavailable when
// none is held within MaxStale, or when kid is missing while the endpoint
// fails.
func (r *Remote) refreshedKey(kid string) (ed25519.PublicKey, error) {
	key, ok, fetchedAt, _ := r.lookup(kid)
	r.mu.RLock()
	lastErr := r.lastErr
	r.mu.RUnlock()
	if fetchedAt.IsZero() || r.cfg.Now().Sub(fetchedAt) > r.cfg.MaxStale {
		return nil, fmt.Errorf("%w: %s: %w", ErrUnavailable, r.endpoint, lastErr)
	}
	// A kid the held set lacks while the endpoint is failing may be one the
	// issuer has rotated in since: the fault is the endpoint's, not the
	// token's, so it is not refused as unknown.
	if !ok && lastErr != nil {
		return nil, fmt.Errorf("%w: %s: kid %q is not in the set held: %w", ErrUnavailable, r.endpoint, kid, lastErr)
	}
	if !ok {
		return nil, ErrUnknownKID
	}
	return slices.Clone(key), nil
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
	// select picks at random between two ready cases: a caller already
	// done may have won the slot.
	if err := ctx.Err(); err != nil {
		r.releaseFetch()
		return err
	}
	done := r.fetchHeld(ctx)
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// refreshBehind starts a refresh without waiting for it, unless a fetch is
// already in flight or was attempted within MinRefreshInterval.
func (r *Remote) refreshBehind(ctx context.Context) {
	select {
	case r.fetching <- struct{}{}:
		r.fetchHeld(ctx)
	default: // a fetch is in flight; it refreshes the set
	}
}

// releaseFetch gives back the token fetching holds.
func (r *Remote) releaseFetch() { <-r.fetching }

// fetchHeld starts a fetch for a caller holding the fetching token, which
// the fetch gives back when it ends, and returns a channel closed then. When
// a fetch was attempted within MinRefreshInterval it gives the token back at
// once and returns nil.
func (r *Remote) fetchHeld(ctx context.Context) <-chan struct{} {
	now := r.cfg.Now()
	r.mu.RLock()
	recent := !r.lastAttempt.IsZero() && now.Sub(r.lastAttempt) < r.cfg.MinRefreshInterval
	etag := r.etag
	r.mu.RUnlock()
	if recent {
		r.releaseFetch()
		return nil
	}
	done := make(chan struct{})
	go func() {
		// The token goes back before done closes: a caller woken by done may
		// start the next refresh at once, and must find the slot free rather
		// than take its own finished fetch for one still in flight.
		defer func() {
			r.releaseFetch()
			close(done)
		}()
		fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), DefaultFetchTimeout)
		defer cancel()
		keys, newETag, header, notModified, err := r.fetch(fetchCtx, etag)
		r.record(now, keys, newETag, header, notModified, err)
	}()
	return done
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
	req, err := r.request(ctx, etag)
	if err != nil {
		return nil, "", nil, false, err
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
	keys, err = readKeys(resp.Body, r.cfg.MaxDocumentBytes)
	if err != nil {
		return nil, "", nil, false, err
	}
	return keys, resp.Header.Get(httpx.HeaderETag), resp.Header, false, nil
}

// request is the fetch of the JWKS, conditional on etag when it is set.
func (r *Remote) request(ctx context.Context, etag string) (*http.Request, error) {
	// The URL is the operator's configuration, not a request's input.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.cfg.URL, nil) //nolint:gosec // G704: see above
	if err != nil {
		return nil, err
	}
	req.Header.Set(httpx.HeaderAccept, acceptJWKSet)
	if etag != "" {
		req.Header.Set(httpx.HeaderIfNoneMatch, etag)
	}
	return req, nil
}

// readKeys reads a JWKS document of at most maxBytes, refusing one whose
// entries hold no usable key: a broken document, not a withdrawal of every
// key — failing the fetch keeps the last good set within MaxStale.
func readKeys(body io.Reader, maxBytes int) (map[string]ed25519.PublicKey, error) {
	raw, err := readCapped(body, maxBytes)
	if err != nil {
		return nil, err
	}
	keys, skipped, err := parse(raw)
	if err != nil {
		return nil, err
	}
	if len(keys) == 0 && skipped > 0 {
		return nil, fmt.Errorf("JWKS document has %d entries and no usable Ed25519 key", skipped)
	}
	return keys, nil
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
