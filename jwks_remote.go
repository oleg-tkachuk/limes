package limes

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// ErrJWKSUnavailable is returned by RemoteJWKSResolver when it holds no key
// set recent enough to trust: the endpoint has been failing for longer than
// MaxStale, or never answered. The verifier turns it into
// ErrInvalidSignature — an issuer we cannot reach verifies nothing.
var ErrJWKSUnavailable = errors.New("limes: JWKS unavailable")

// RemoteJWKSConfig defaults, documented on its fields.
const (
	defaultJWKSFetchTimeout       = 10 * time.Second
	defaultJWKSRefreshInterval    = 5 * time.Minute
	defaultJWKSMinRefreshInterval = 10 * time.Second
	defaultJWKSMaxStale           = time.Hour
	defaultJWKSMaxDocumentBytes   = 1 << 20
)

// RemoteJWKSConfig configures a RemoteJWKSResolver. Only URL is required.
// The intervals must nest: MinRefreshInterval ≤ RefreshInterval ≤ MaxStale.
type RemoteJWKSConfig struct {
	// URL of the issuer's JWKS document, e.g.
	// https://issuer.example/.well-known/jwks.json.
	URL string
	// Client performs the fetch. Default: a client with a 10s timeout.
	Client *http.Client
	// RefreshInterval is how long a fetched key set is used before it is
	// re-fetched. Default 5m.
	RefreshInterval time.Duration
	// MinRefreshInterval rate-limits fetches. A token with an unknown kid
	// triggers a refetch (that is how a newly published key is picked up),
	// but at most once per this interval, so a stream of tokens with
	// made-up kids cannot turn the verifier into a request amplifier
	// against the issuer. Default 10s.
	MinRefreshInterval time.Duration
	// MaxStale is how long the last good key set keeps being served while
	// fetches fail. Past it the resolver fails closed with
	// ErrJWKSUnavailable. Default 1h — comfortably longer than the
	// capability TTLs, shorter than a key's useful life after withdrawal.
	MaxStale time.Duration
	// MaxDocumentBytes caps the response body. Default 1 MiB.
	MaxDocumentBytes int64
	// Now is the clock; tests override.
	Now func() time.Time
}

// RemoteJWKSResolver is a KeyResolver backed by an issuer's JWKS endpoint,
// for verifiers that run apart from the issuer. It caches the key set,
// revalidates with ETag, picks up a rotated-in key on first sight of its kid,
// and keeps serving the last good set through a bounded outage.
type RemoteJWKSResolver struct {
	cfg RemoteJWKSConfig

	// fetching holds one token while a fetch is in flight. It is a channel
	// rather than a mutex so that a caller waiting behind another's fetch
	// can give up when its own context does.
	fetching chan struct{}

	mu          sync.RWMutex
	keys        map[string]ed25519.PublicKey
	etag        string
	fetchedAt   time.Time // last time the set was confirmed current
	lastAttempt time.Time
	lastErr     error
}

// NewRemoteJWKSResolver validates the config. It does not fetch: the first
// PublicKey call does, so constructing a verifier never blocks on the network.
func NewRemoteJWKSResolver(cfg RemoteJWKSConfig) (*RemoteJWKSResolver, error) {
	if cfg.URL == "" {
		return nil, errors.New("limes: RemoteJWKSConfig.URL required")
	}
	if cfg.Client == nil {
		cfg.Client = &http.Client{Timeout: defaultJWKSFetchTimeout}
	}
	if cfg.RefreshInterval <= 0 {
		cfg.RefreshInterval = defaultJWKSRefreshInterval
	}
	if cfg.MinRefreshInterval <= 0 {
		cfg.MinRefreshInterval = defaultJWKSMinRefreshInterval
	}
	if cfg.MaxStale <= 0 {
		cfg.MaxStale = defaultJWKSMaxStale
	}
	if cfg.MaxDocumentBytes <= 0 {
		cfg.MaxDocumentBytes = defaultJWKSMaxDocumentBytes
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	// A set past MaxStale before it is due a refresh would fail closed
	// with no fetch having failed, and a refresh due inside the rate limit
	// would wait it out anyway.
	if cfg.MinRefreshInterval > cfg.RefreshInterval || cfg.RefreshInterval > cfg.MaxStale {
		return nil, fmt.Errorf("limes: RemoteJWKSConfig needs MinRefreshInterval (%s) ≤ RefreshInterval (%s) ≤ MaxStale (%s)",
			cfg.MinRefreshInterval, cfg.RefreshInterval, cfg.MaxStale)
	}
	return &RemoteJWKSResolver{cfg: cfg, fetching: make(chan struct{}, 1)}, nil
}

// PublicKey implements KeyResolver.
func (r *RemoteJWKSResolver) PublicKey(ctx context.Context, kid string) (ed25519.PublicKey, error) {
	key, ok, fetchedAt := r.lookup(kid)
	if !ok || r.cfg.Now().Sub(fetchedAt) >= r.cfg.RefreshInterval {
		if err := r.refresh(ctx); err != nil {
			return nil, err
		}
		key, ok, fetchedAt = r.lookup(kid)
	}

	if fetchedAt.IsZero() || r.cfg.Now().Sub(fetchedAt) > r.cfg.MaxStale {
		r.mu.RLock()
		lastErr := r.lastErr
		r.mu.RUnlock()
		return nil, fmt.Errorf("%w: %s: %w", ErrJWKSUnavailable, r.cfg.URL, lastErr)
	}
	if !ok {
		return nil, ErrUnknownKID
	}
	return key, nil
}

func (r *RemoteJWKSResolver) lookup(kid string) (ed25519.PublicKey, bool, time.Time) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	k, ok := r.keys[kid]
	return k, ok, r.fetchedAt
}

// refresh fetches the document unless a fetch was attempted within
// MinRefreshInterval. Failures are recorded, not returned: the caller decides
// from the age of the set it still holds. It returns only ctx's error, when
// the caller gave up waiting behind another fetch.
func (r *RemoteJWKSResolver) refresh(ctx context.Context) error {
	select {
	case r.fetching <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-r.fetching }()

	now := r.cfg.Now()
	r.mu.RLock()
	recent := !r.lastAttempt.IsZero() && now.Sub(r.lastAttempt) < r.cfg.MinRefreshInterval
	etag := r.etag
	r.mu.RUnlock()
	if recent {
		return nil
	}

	keys, newETag, notModified, err := r.fetch(ctx, etag)

	r.mu.Lock()
	defer r.mu.Unlock()
	r.lastAttempt = now
	r.lastErr = err
	if err != nil {
		return nil
	}
	r.fetchedAt = now
	if !notModified {
		r.keys = keys
		r.etag = newETag
	}
	return nil
}

func (r *RemoteJWKSResolver) fetch(ctx context.Context, etag string) (keys map[string]ed25519.PublicKey, newETag string, notModified bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.cfg.URL, nil)
	if err != nil {
		return nil, "", false, err
	}
	req.Header.Set("Accept", "application/json")
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := r.cfg.Client.Do(req)
	if err != nil {
		return nil, "", false, err
	}
	defer func() { _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode == http.StatusNotModified && etag != "":
		return nil, etag, true, nil
	case resp.StatusCode != http.StatusOK:
		return nil, "", false, fmt.Errorf("JWKS fetch: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, r.cfg.MaxDocumentBytes+1))
	if err != nil {
		return nil, "", false, fmt.Errorf("JWKS read: %w", err)
	}
	if int64(len(body)) > r.cfg.MaxDocumentBytes {
		return nil, "", false, fmt.Errorf("JWKS document exceeds %d bytes", r.cfg.MaxDocumentBytes)
	}
	keys, err = ParseJWKS(body)
	if err != nil {
		return nil, "", false, err
	}
	return keys, resp.Header.Get("ETag"), false, nil
}
