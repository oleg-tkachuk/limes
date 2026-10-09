package limes

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
)

// RevocationLookup is the seam the cache wraps. A persistent store
// implements it; tests substitute a stub. Like Store.IsRevoked, it answers
// for the capability's whole delegation chain.
type RevocationLookup interface {
	IsRevoked(ctx context.Context, id uuid.UUID) (bool, error)
}

// DefaultRevocationCacheTTL bounds how long a cached answer is served. A
// revocation therefore takes effect within this window on every verifier
// that did not perform it.
const DefaultRevocationCacheTTL = 2 * time.Second

// DefaultRevocationCacheEntries bounds the cache. Past it, expired entries
// are dropped and, if that frees nothing, the cache is reset: a flood of
// distinct ids costs extra lookups, never unbounded memory.
const DefaultRevocationCacheEntries = 100_000

// CachedRevocationChecker is a small in-memory cache for IsRevoked answers.
//
// A cached answer about a capability is also an answer about its ancestors,
// so revoking a parent reaches a child's cached "live" entry only when that
// entry expires — within the TTL, like any other revocation seen from
// another process.
//
// Concurrent misses for the same id share one upstream call.
type CachedRevocationChecker struct {
	upstream RevocationLookup
	cache    *ttlCache[uuid.UUID]
}

// cacheConfig is what the CacheOptions set, for either checker.
type cacheConfig struct {
	ttl        time.Duration
	maxEntries int
	now        func() time.Time
}

// CacheOption configures a CachedRevocationChecker or a
// CachedBiscuitRevocationChecker.
type CacheOption func(*cacheConfig)

// WithCacheClock replaces time.Now; tests use it to step through expiry.
func WithCacheClock(now func() time.Time) CacheOption {
	return func(c *cacheConfig) { c.now = now }
}

// WithMaxEntries bounds the number of cached entries. ≤ 0 keeps the default.
func WithMaxEntries(n int) CacheOption {
	return func(c *cacheConfig) {
		if n > 0 {
			c.maxEntries = n
		}
	}
}

// newCacheConfig applies opts over the defaults. ttl of 0 falls back to
// DefaultRevocationCacheTTL; ttl < 0 disables caching.
func newCacheConfig(ttl time.Duration, opts []CacheOption) cacheConfig {
	if ttl == 0 {
		ttl = DefaultRevocationCacheTTL
	}
	cfg := cacheConfig{ttl: ttl, maxEntries: DefaultRevocationCacheEntries, now: time.Now}
	for _, o := range opts {
		o(&cfg)
	}
	return cfg
}

// NewCachedRevocationChecker wires a cache over an upstream lookup. ttl
// of 0 falls back to DefaultRevocationCacheTTL; ttl < 0 disables caching
// (every call hits upstream — useful for tests).
func NewCachedRevocationChecker(upstream RevocationLookup, ttl time.Duration, opts ...CacheOption) *CachedRevocationChecker {
	return &CachedRevocationChecker{upstream: upstream, cache: newTTLCache[uuid.UUID](newCacheConfig(ttl, opts))}
}

// IsRevoked serves a fresh cached answer, or asks upstream once on behalf of
// every concurrent caller asking about the same id. An upstream error is
// returned to all of them and is never cached.
func (c *CachedRevocationChecker) IsRevoked(ctx context.Context, id uuid.UUID) (bool, error) {
	return c.cache.get(ctx, id, func(ctx context.Context) (bool, error) { return c.upstream.IsRevoked(ctx, id) })
}

// Invalidate drops a single entry. Other processes catch up within the TTL
// unless they are told — see Clear, which a consumer wires to a revocation
// notification so they are.
func (c *CachedRevocationChecker) Invalidate(id uuid.UUID) { c.cache.invalidate(id) }

// Clear drops every entry. A consumer that learns of a revocation from
// elsewhere — a database notification, a message bus — calls it so the next
// check goes upstream at once instead of waiting out the TTL. It drops
// everything, not only the revoked id, because an answer cached for a
// descendant is also an answer about the revoked ancestor.
func (c *CachedRevocationChecker) Clear() { c.cache.clear() }

// Sweep drops every expired entry and returns how many it dropped.
// Optional housekeeping — lookups skip stale entries anyway.
func (c *CachedRevocationChecker) Sweep() int { return c.cache.sweep() }

// ttlCache caches a yes/no answer per key for a TTL, bounded in size, with
// concurrent misses for one key sharing one lookup. Both revocation checkers
// are this cache over their own key.
type ttlCache[K comparable] struct {
	cfg cacheConfig

	mu       sync.Mutex
	entries  map[K]cacheEntry
	inflight map[K]*lookupCall
	// epoch counts invalidations. A lookup that started before one may
	// have read the state it invalidated, so its answer is not cached.
	epoch uint64
}

type cacheEntry struct {
	revoked bool
	expires time.Time
}

type lookupCall struct {
	done    chan struct{}
	revoked bool
	err     error
}

// errLookupAbandoned is what callers sharing a lookup receive when it ended
// without an answer — it panicked — rather than a "not revoked" nobody gave.
var errLookupAbandoned = errors.New("limes: revocation lookup ended without an answer")

func newTTLCache[K comparable](cfg cacheConfig) *ttlCache[K] {
	return &ttlCache[K]{cfg: cfg, entries: make(map[K]cacheEntry), inflight: make(map[K]*lookupCall)}
}

// get serves a fresh entry for key, or runs lookup once for every
// concurrent caller missing it. An error is returned and never cached.
func (c *ttlCache[K]) get(ctx context.Context, key K, lookup func(context.Context) (bool, error)) (bool, error) {
	if c.cfg.ttl < 0 {
		return lookup(ctx)
	}

	c.mu.Lock()
	if e, ok := c.entries[key]; ok && e.expires.After(c.cfg.now()) {
		c.mu.Unlock()
		return e.revoked, nil
	}
	if call, ok := c.inflight[key]; ok {
		c.mu.Unlock()
		select {
		case <-call.done:
			// The leader's request may have been cancelled or timed out on
			// its own account. That is no answer about the key, so a waiter
			// whose own context is still live asks for itself.
			if call.err != nil && isContextErr(call.err) && ctx.Err() == nil {
				return lookup(ctx)
			}
			return call.revoked, call.err
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
	call := &lookupCall{done: make(chan struct{}), err: errLookupAbandoned}
	c.inflight[key] = call
	epoch := c.epoch
	c.mu.Unlock()

	// Deferred so that a lookup that panics still releases its waiters and
	// the key.
	defer c.finish(key, call, epoch)
	call.revoked, call.err = lookup(ctx)
	return call.revoked, call.err
}

// finish publishes call's answer to its waiters and caches it, unless it
// failed or an invalidation happened while it ran.
func (c *ttlCache[K]) finish(key K, call *lookupCall, epoch uint64) {
	c.mu.Lock()
	if c.inflight[key] == call {
		delete(c.inflight, key)
	}
	if call.err == nil && c.epoch == epoch {
		c.makeRoomLocked()
		c.entries[key] = cacheEntry{revoked: call.revoked, expires: c.cfg.now().Add(c.cfg.ttl)}
	}
	c.mu.Unlock()
	close(call.done)
}

// makeRoomLocked keeps the cache under maxEntries: expired entries go
// first and, if that frees nothing, everything does.
func (c *ttlCache[K]) makeRoomLocked() {
	if len(c.entries) < c.cfg.maxEntries {
		return
	}
	c.sweepLocked()
	if len(c.entries) >= c.cfg.maxEntries {
		clear(c.entries)
	}
}

// invalidate drops key's entry and detaches a lookup of it in flight, so the
// next check asks upstream rather than taking an answer read before now.
// The epoch is shared: lookups of other keys in flight go uncached too,
// which costs them one extra lookup and keeps the bookkeeping to a counter.
func (c *ttlCache[K]) invalidate(key K) {
	c.mu.Lock()
	c.epoch++
	delete(c.entries, key)
	delete(c.inflight, key)
	c.mu.Unlock()
}

// clear drops every entry and detaches every lookup in flight.
func (c *ttlCache[K]) clear() {
	c.mu.Lock()
	c.epoch++
	clear(c.entries)
	clear(c.inflight)
	c.mu.Unlock()
}

func (c *ttlCache[K]) sweep() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sweepLocked()
}

func (c *ttlCache[K]) sweepLocked() int {
	now := c.cfg.now()
	dropped := 0
	for k, e := range c.entries {
		if !e.expires.After(now) {
			delete(c.entries, k)
			dropped++
		}
	}
	return dropped
}

func isContextErr(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
