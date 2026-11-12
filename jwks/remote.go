package jwks

import (
	"cmp"
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"slices"
	"sync"
	"time"

	"github.com/oleg-tkachuk/limes/internal/errs"
	"github.com/oleg-tkachuk/limes/internal/httpsurl"
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
	// Logger logs each fetch — a new or confirmed set at DEBUG, a failure
	// at WARN while the held set is still served and at ERROR once none is —,
	// a fetch skipped inside MinRefreshInterval at DEBUG, Block, Unblock and
	// Refresh, and each kid refused for being blocked. Nil: slog.Default().
	Logger *slog.Logger
}

// Remote is a KeyResolver backed by an issuer's JWKS endpoint,
// for verifiers that run apart from the issuer. It caches the key set,
// revalidates with ETag, picks up a rotated-in key on first sight of its kid,
// and keeps serving the last good set through a bounded outage.
//
// A key the issuer withdraws stays accepted until the next refresh, and
// through an outage for up to MaxStale. For an incident — a signing key
// leaked — Block refuses a kid at once, whatever the held set says, and
// Refresh fetches the set now rather than at the next interval.
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
	// blocked are the kids Block refused, and Unblock not given back.
	blocked map[string]struct{}
}

// NewRemote builds a Remote, refusing with limes.ErrInvalidConfig a config
// without a URL or whose URL is not https with a host, a negative interval
// or MaxDocumentBytes, or intervals that do not nest. It does not fetch: the
// first PublicKey call does, so constructing a verifier never blocks on the
// network.
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
	return &Remote{cfg: cfg, endpoint: u.Redacted(), fetching: make(chan struct{}, 1), blocked: map[string]struct{}{}}, nil
}

// parseRemoteURL reads the JWKS endpoint's URL, refusing one that is not
// an https URL with a host (httpsurl.Parse).
func parseRemoteURL(raw string) (*url.URL, error) {
	u, err := httpsurl.Parse(raw)
	if err != nil {
		return nil, errs.Errorf("%w: jwks: RemoteConfig.URL: %w", errs.ErrInvalidConfig, err)
	}
	return u, nil
}

// setRemoteDefaults gives cfg's unset members their defaults, and its
// client the same-origin redirect policy.
func setRemoteDefaults(cfg *RemoteConfig) {
	if cfg.Client == nil {
		cfg.Client = &http.Client{Timeout: DefaultFetchTimeout}
	}
	cfg.Client = httpsurl.SameOrigin(cfg.Client)
	cfg.RefreshInterval = cmp.Or(cfg.RefreshInterval, DefaultRefreshInterval)
	cfg.MinRefreshInterval = cmp.Or(cfg.MinRefreshInterval, DefaultMinRefreshInterval)
	cfg.MaxStale = cmp.Or(cfg.MaxStale, DefaultMaxStale)
	cfg.MaxDocumentBytes = cmp.Or(cfg.MaxDocumentBytes, DefaultMaxDocumentBytes)
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
}

// PublicKey implements limes.KeyResolver. A known key from a set past its
// freshness but within MaxStale is served at once, its refresh started behind
// it: a slow endpoint delays no request while the set it holds is still good.
// A caller waits on the endpoint only for a kid the set does not hold, or a
// set it cannot serve. A kid Block refused is ErrUnknownKID, before the set
// or the endpoint is consulted.
func (r *Remote) PublicKey(ctx context.Context, kid string) (ed25519.PublicKey, error) {
	if r.isBlocked(kid) {
		r.cfg.Logger.WarnContext(ctx, "jwks: refused a blocked kid", slog.String(logKeyKID, kid), slog.String(logKeyEndpoint, r.endpoint))
		return nil, fmt.Errorf("%w: kid %q is blocked", ErrUnknownKID, kid)
	}
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
		return nil, errs.Errorf("%w: %s: %w", ErrUnavailable, r.endpoint, err)
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
		return nil, errs.Errorf("%w: %s: %w", ErrUnavailable, r.endpoint, lastErr)
	}
	// A kid the held set lacks while the endpoint is failing may be one the
	// issuer has rotated in since: the fault is the endpoint's, not the
	// token's, so it is not refused as unknown.
	if !ok && lastErr != nil {
		return nil, errs.Errorf("%w: %s: kid %q is not in the set held: %w", ErrUnavailable, r.endpoint, kid, lastErr)
	}
	if !ok {
		return nil, ErrUnknownKID
	}
	return slices.Clone(key), nil
}

// The attributes of Remote's log lines.
const (
	logKeyKID      = "kid"
	logKeyKIDs     = "kids"
	logKeyEndpoint = "endpoint"
	logKeyError    = "error"
	// logKeyKeyCount is how many keys the set holds after a fetch.
	logKeyKeyCount = "keys"
	// logKeyNotModified is a fetch the endpoint answered 304 Not Modified.
	logKeyNotModified = "not_modified"
	// logKeyFreshFor is how long a fetched set is used before the next fetch.
	logKeyFreshFor = "fresh_for"
	// logKeyHeldAge is how long ago the held set was last confirmed current.
	logKeyHeldAge = "held_age"
	// logKeyMaxStale is RemoteConfig.MaxStale: how long a held set is served
	// while fetches fail.
	logKeyMaxStale = "max_stale"
	// logKeyRetryAfter is RemoteConfig.MinRefreshInterval: how soon after a
	// fetch the next one may start.
	logKeyRetryAfter = "retry_after"
	// logKeySinceAttempt is how long ago the last fetch was attempted.
	logKeySinceAttempt = "since_attempt"
)

// Block refuses kids from now on, as ErrUnknownKID, even while the held set
// or the endpoint still has them: for a signing key withdrawn, or leaked,
// that must stop verifying before the next refresh — or, with the endpoint
// down, before MaxStale. It holds for the life of the Remote, until Unblock.
func (r *Remote) Block(kids ...string) {
	r.mu.Lock()
	for _, kid := range kids {
		r.blocked[kid] = struct{}{}
	}
	r.mu.Unlock()
	r.cfg.Logger.Warn("jwks: kids blocked", slog.Any(logKeyKIDs, kids), slog.String(logKeyEndpoint, r.endpoint))
}

// Unblock gives back kids Block refused: each is served again as the key
// set has it.
func (r *Remote) Unblock(kids ...string) {
	r.mu.Lock()
	for _, kid := range kids {
		delete(r.blocked, kid)
	}
	r.mu.Unlock()
	r.cfg.Logger.Info("jwks: kids unblocked", slog.Any(logKeyKIDs, kids), slog.String(logKeyEndpoint, r.endpoint))
}

// isBlocked reports whether Block refused kid.
func (r *Remote) isBlocked(kid string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.blocked[kid]
	return ok
}

// Refresh fetches the key set now, whenever the last fetch was — an
// operator's call, which MinRefreshInterval does not hold back — and returns
// the fetch's error, wrapping ErrUnavailable, or ctx's when the caller gives
// up first. A key the endpoint no longer serves stops verifying when it
// returns nil. It waits behind a fetch in flight, then fetches again: the
// one in flight may have started before the key was withdrawn.
func (r *Remote) Refresh(ctx context.Context) error {
	select {
	case r.fetching <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		r.releaseFetch()
		return err
	}
	select {
	case <-r.startFetch(ctx, r.cfg.Now()):
	case <-ctx.Done():
		return ctx.Err()
	}
	r.mu.RLock()
	err := r.lastErr
	r.mu.RUnlock()
	if err != nil {
		r.cfg.Logger.ErrorContext(ctx, "jwks: refresh failed", slog.String(logKeyEndpoint, r.endpoint), slog.Any(logKeyError, err))
		return errs.Errorf("%w: %s: %w", ErrUnavailable, r.endpoint, err)
	}
	r.cfg.Logger.InfoContext(ctx, "jwks: key set refreshed", slog.String(logKeyEndpoint, r.endpoint))
	return nil
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
	since := now.Sub(r.lastAttempt)
	recent := !r.lastAttempt.IsZero() && since < r.cfg.MinRefreshInterval
	r.mu.RUnlock()
	if recent {
		r.releaseFetch()
		r.cfg.Logger.DebugContext(ctx, "jwks: fetch skipped: the last attempt is within MinRefreshInterval",
			slog.String(logKeyEndpoint, r.endpoint), slog.Duration(logKeySinceAttempt, since), slog.Duration(logKeyRetryAfter, r.cfg.MinRefreshInterval))
		return nil
	}
	return r.startFetch(ctx, now)
}

// startFetch fetches the set for a caller holding the fetching token, which
// the fetch gives back when it ends, and returns a channel closed then.
func (r *Remote) startFetch(ctx context.Context, now time.Time) <-chan struct{} {
	r.mu.RLock()
	etag := r.etag
	r.mu.RUnlock()
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
		out := r.record(now, keys, newETag, header, notModified, err)
		r.logFetch(fetchCtx, out, err)
	}()
	return done
}

// fetchOutcome is what a fetch left the Remote holding, for its log line.
type fetchOutcome struct {
	keys        int
	notModified bool
	freshFor    time.Duration
	// held is a failed fetch leaving a set within MaxStale to serve,
	// confirmed current heldAge ago.
	held    bool
	heldAge time.Duration
}

// record keeps what a fetch attempted at now found, and returns what the
// Remote holds after it.
func (r *Remote) record(now time.Time, keys map[string]ed25519.PublicKey, etag string, header http.Header, notModified bool, err error) fetchOutcome {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lastAttempt = now
	r.lastErr = err
	if err != nil {
		age := now.Sub(r.fetchedAt)
		return fetchOutcome{keys: len(r.keys), held: !r.fetchedAt.IsZero() && age <= r.cfg.MaxStale, heldAge: age}
	}
	r.fetchedAt = now
	r.freshFor = r.freshness(header)
	if !notModified {
		r.keys = keys
		r.etag = etag
	}
	return fetchOutcome{keys: len(r.keys), notModified: notModified, freshFor: r.freshFor}
}

// logFetch logs a fetch's outcome: a set fetched or confirmed at DEBUG; a
// failure at WARN while the held set is still served, and at ERROR once
// there is none to serve and the resolver fails closed. err names the
// endpoint as net/http does, its password masked.
func (r *Remote) logFetch(ctx context.Context, out fetchOutcome, err error) {
	endpoint := slog.String(logKeyEndpoint, r.endpoint)
	switch {
	case err == nil:
		r.cfg.Logger.DebugContext(ctx, "jwks: key set fetched", endpoint,
			slog.Int(logKeyKeyCount, out.keys), slog.Bool(logKeyNotModified, out.notModified), slog.Duration(logKeyFreshFor, out.freshFor))
	case out.held:
		r.cfg.Logger.WarnContext(ctx, "jwks: key set fetch failed; serving the held set until it is MaxStale old", endpoint,
			slog.Any(logKeyError, err), slog.Int(logKeyKeyCount, out.keys), slog.Duration(logKeyHeldAge, out.heldAge),
			slog.Duration(logKeyMaxStale, r.cfg.MaxStale), slog.Duration(logKeyRetryAfter, r.cfg.MinRefreshInterval))
	default:
		r.cfg.Logger.ErrorContext(ctx, "jwks: key set fetch failed with no set to serve; refusing tokens until a fetch succeeds", endpoint,
			slog.Any(logKeyError, err), slog.Duration(logKeyRetryAfter, r.cfg.MinRefreshInterval))
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
