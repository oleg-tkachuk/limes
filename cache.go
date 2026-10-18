package limes

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
)

// RevocationLookup is the seam the cache wraps. A persistent store
// implements it; tests substitute a stub. Like Store.IsRevoked, it answers
// for the capability's whole delegation chain.
type RevocationLookup interface {
	// IsRevoked reports whether id, or any ancestor of it, is revoked. An
	// error is no answer: the verifier refuses rather than assume not.
	IsRevoked(ctx context.Context, id uuid.UUID) (bool, error)
}

// DefaultRevocationCacheTTL bounds how long a cached answer is served. A
// revocation therefore takes effect within this window on every verifier
// that did not perform it.
const DefaultRevocationCacheTTL = 2 * time.Second

// DefaultRevocationCacheEntries bounds the cache. Past it, the oldest entry
// makes room for the new one: a flood of distinct ids costs extra lookups,
// never unbounded memory, and never the answers cached for everyone else.
const DefaultRevocationCacheEntries = 100_000

// CachedRevocationChecker is a small in-memory cache for IsRevoked answers.
//
// A cached answer about a capability is also an answer about its ancestors,
// so revoking a parent reaches a child's cached "live" entry only when that
// entry expires — within the TTL, like any other revocation seen from
// another process — or when Clear drops it. There is no per-id drop: the
// cache does not know which entries are a revoked id's descendants.
//
// Concurrent misses for the same id share one upstream call.
type CachedRevocationChecker struct {
	upstream RevocationLookup
	cache    *ttlCache[uuid.UUID]
}

// RevocationCacheConfig configures a CachedRevocationChecker.
type RevocationCacheConfig struct {
	// Upstream answers what the cache does not hold. Required.
	Upstream RevocationLookup
	// TTL is how long an answer is served. Zero: DefaultRevocationCacheTTL.
	TTL time.Duration
	// MaxEntries bounds the cache. Zero: DefaultRevocationCacheEntries.
	MaxEntries int
	// Now is the clock. Nil: time.Now.
	Now func() time.Time
}

// NewCachedRevocationChecker wires a cache over cfg.Upstream. A negative TTL
// or bound is refused; a deployment that wants no cache passes its store to
// the verifier directly.
func NewCachedRevocationChecker(cfg RevocationCacheConfig) (*CachedRevocationChecker, error) {
	if cfg.Upstream == nil {
		return nil, errors.New("limes: RevocationCacheConfig.Upstream required")
	}
	c, err := newCacheConfig(cfg.TTL, cfg.MaxEntries, cfg.Now)
	if err != nil {
		return nil, err
	}
	return &CachedRevocationChecker{upstream: cfg.Upstream, cache: newTTLCache[uuid.UUID](c)}, nil
}

// cacheConfig is what either checker's cache runs on.
type cacheConfig struct {
	ttl        time.Duration
	maxEntries int
	now        func() time.Time
}

// newCacheConfig resolves a configuration's zero values to the defaults and
// refuses the negative ones.
func newCacheConfig(ttl time.Duration, maxEntries int, now func() time.Time) (cacheConfig, error) {
	if ttl < 0 {
		return cacheConfig{}, fmt.Errorf("limes: cache TTL %s is negative", ttl)
	}
	if maxEntries < 0 {
		return cacheConfig{}, fmt.Errorf("limes: cache bound %d is negative", maxEntries)
	}
	if ttl == 0 {
		ttl = DefaultRevocationCacheTTL
	}
	if maxEntries == 0 {
		maxEntries = DefaultRevocationCacheEntries
	}
	if now == nil {
		now = time.Now
	}
	return cacheConfig{ttl: ttl, maxEntries: maxEntries, now: now}, nil
}

// IsRevoked serves a fresh cached answer, or asks upstream once on behalf of
// every concurrent caller asking about the same id. An upstream error is
// returned to all of them and is never cached.
func (c *CachedRevocationChecker) IsRevoked(ctx context.Context, id uuid.UUID) (bool, error) {
	return c.cache.get(ctx, id, func(ctx context.Context) (bool, error) { return c.upstream.IsRevoked(ctx, id) })
}

// Clear drops every entry. A consumer that learns of a revocation from
// elsewhere — a database notification, a message bus — calls it so the next
// check goes upstream at once instead of waiting out the TTL. It drops
// everything, not only the revoked id, because an answer cached for a
// descendant is also an answer about the revoked ancestor.
func (c *CachedRevocationChecker) Clear() { c.cache.clear() }

// Sweep drops every expired entry and returns how many it dropped.
// Optional housekeeping — lookups skip stale entries, and every new answer
// drops the expired ones before it.
func (c *CachedRevocationChecker) Sweep() int { return c.cache.sweep() }

// ttlCache caches a yes/no answer per key for a TTL, bounded in size, with
// concurrent misses for one key sharing one lookup. Both revocation checkers
// are this cache over their own key.
type ttlCache[K comparable] struct {
	cfg cacheConfig

	mu       sync.Mutex
	entries  map[K]cacheEntry
	inflight map[K]*lookupCall
	// order holds the entries in the order they were cached, which every
	// entry sharing one TTL makes the order they expire in: one place per
	// entry, since a key is cached again only once its place has expired
	// and been dropped.
	order []queued[K]
	// epoch counts clears. A lookup that started before one may
	// have read the state it invalidated, so its answer is not cached.
	epoch uint64
}

type cacheEntry struct {
	revoked bool
	expires time.Time
}

// queued is a key's place in ttlCache.order.
type queued[K comparable] struct {
	key     K
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
			// whose own context is still live asks again — through the
			// cache, so the waiters retrying together share one lookup.
			if call.err != nil && isContextErr(call.err) && ctx.Err() == nil {
				return c.get(ctx, key, lookup)
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

// cached reads the fresh answers keys have: whether any of them is revoked,
// the keys with none, and the epoch a lookup of those starts in.
func (c *ttlCache[K]) cached(keys []K) (revoked bool, missing []K, epoch uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.cfg.now()
	for _, k := range keys {
		e, ok := c.entries[k]
		switch {
		case !ok || !e.expires.After(now):
			missing = append(missing, k)
		case e.revoked:
			return true, nil, c.epoch
		}
	}
	return false, missing, c.epoch
}

// seedLive caches keys as not revoked, as a lookup of them all that started
// in epoch answered, unless a clear has happened since; a key that has a
// fresh answer or a lookup of its own in flight keeps that.
func (c *ttlCache[K]) seedLive(keys []K, epoch uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.epoch != epoch {
		return
	}
	now := c.cfg.now()
	for _, k := range keys {
		if e, ok := c.entries[k]; ok && e.expires.After(now) {
			continue
		}
		if _, ok := c.inflight[k]; ok {
			continue
		}
		c.makeRoomLocked()
		expires := now.Add(c.cfg.ttl)
		c.entries[k] = cacheEntry{expires: expires}
		c.order = append(c.order, queued[K]{key: k, expires: expires})
	}
}

// finish publishes call's answer to its waiters and caches it, unless it
// failed or a clear happened while it ran.
func (c *ttlCache[K]) finish(key K, call *lookupCall, epoch uint64) {
	c.mu.Lock()
	if c.inflight[key] == call {
		delete(c.inflight, key)
	}
	if call.err == nil && c.epoch == epoch {
		c.makeRoomLocked()
		expires := c.cfg.now().Add(c.cfg.ttl)
		c.entries[key] = cacheEntry{revoked: call.revoked, expires: expires}
		c.order = append(c.order, queued[K]{key: key, expires: expires})
	}
	c.mu.Unlock()
	close(call.done)
}

// makeRoomLocked keeps the cache under maxEntries: expired entries go
// first and, if that frees nothing, the oldest live one does.
func (c *ttlCache[K]) makeRoomLocked() {
	c.sweepLocked()
	for len(c.entries) >= c.cfg.maxEntries && c.popLocked() {
	}
}

// popLocked drops the oldest place in order, and its entry if the place is
// still the entry's own. It reports whether there was a place to drop.
func (c *ttlCache[K]) popLocked() bool {
	if len(c.order) == 0 {
		return false
	}
	q := c.order[0]
	c.order[0] = queued[K]{} // let the key go
	c.order = c.order[1:]
	if e, ok := c.entries[q.key]; ok && e.expires.Equal(q.expires) {
		delete(c.entries, q.key)
	}
	return true
}

// clear drops every entry and detaches every lookup in flight, so the next
// check asks upstream rather than taking an answer read before now.
func (c *ttlCache[K]) clear() {
	c.mu.Lock()
	c.epoch++
	clear(c.entries)
	clear(c.inflight)
	c.order = nil
	c.mu.Unlock()
}

func (c *ttlCache[K]) sweep() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sweepLocked()
}

// sweepLocked drops every expired entry, oldest first, and returns how many
// it dropped. It stops at the first place not yet expired: every place after
// it expires later.
func (c *ttlCache[K]) sweepLocked() int {
	now := c.cfg.now()
	dropped := 0
	for len(c.order) > 0 && !c.order[0].expires.After(now) {
		before := len(c.entries)
		c.popLocked()
		dropped += before - len(c.entries)
	}
	return dropped
}

func isContextErr(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
