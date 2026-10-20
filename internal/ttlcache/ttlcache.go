// Package ttlcache caches a yes/no answer per key for a TTL, bounded in
// size, with concurrent misses for one key sharing one lookup. limes's two
// revocation checkers, RevocationCache and BiscuitRevocationCache, are this
// cache over their own key.
package ttlcache

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Config is what a Cache runs on. Every field is required: the caller
// resolves its defaults.
type Config struct {
	// TTL is how long an answer is served.
	TTL time.Duration
	// MaxEntries bounds the cache: past it, the oldest entry makes room.
	MaxEntries int
	// Now is the clock.
	Now func() time.Time
}

// Cache caches a yes/no answer per key for a TTL. The zero value is not
// usable: New makes one.
type Cache[K comparable] struct {
	cfg Config

	mu       sync.Mutex
	entries  map[K]cacheEntry
	inflight map[K]*lookupCall
	// order holds the entries in the order they were cached, which every
	// entry sharing one TTL makes the order they expire in, but for an id
	// seeded from a set's answer, which expires with that answer: sweeping
	// stops at the first place not yet expired, so such an id is dropped a
	// little late, and never served past its expiry. One place per entry,
	// since a key is cached again only once its place has expired and been
	// dropped.
	order []queued[K]
	// epoch counts clears. A lookup that started before one may
	// have read the state it invalidated, so its answer is not cached.
	epoch uint64
}

type cacheEntry struct {
	revoked bool
	expires time.Time
}

// queued is a key's place in Cache.order.
type queued[K comparable] struct {
	key     K
	expires time.Time
}

type lookupCall struct {
	done    chan struct{}
	revoked bool
	err     error
	// expires is when the answer stops being fresh: a TTL from when the
	// lookup started, no later than the entry finish caches.
	expires time.Time
}

// errLookupAbandoned is what callers sharing a lookup receive when it ended
// without an answer — it panicked — rather than a "not revoked" nobody gave.
var errLookupAbandoned = errors.New("limes: revocation lookup ended without an answer")

// New is an empty cache running on cfg.
func New[K comparable](cfg Config) *Cache[K] {
	return &Cache[K]{cfg: cfg, entries: make(map[K]cacheEntry), inflight: make(map[K]*lookupCall)}
}

// Get serves a fresh entry for key, or runs lookup once for every
// concurrent caller missing it, with when the answer stops being fresh. An
// error is returned and never cached.
func (c *Cache[K]) Get(ctx context.Context, key K, lookup func(context.Context) (bool, error)) (bool, time.Time, error) {
	c.mu.Lock()
	if e, ok := c.entries[key]; ok && e.expires.After(c.cfg.Now()) {
		c.mu.Unlock()
		return e.revoked, e.expires, nil
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
				return c.Get(ctx, key, lookup)
			}
			return call.revoked, call.expires, call.err
		case <-ctx.Done():
			return false, time.Time{}, ctx.Err()
		}
	}
	call := &lookupCall{done: make(chan struct{}), err: errLookupAbandoned, expires: c.cfg.Now().Add(c.cfg.TTL)}
	c.inflight[key] = call
	epoch := c.epoch
	c.mu.Unlock()

	// Deferred so that a lookup that panics still releases its waiters and
	// the key.
	defer c.finish(key, call, epoch)
	call.revoked, call.err = lookup(ctx)
	return call.revoked, call.expires, call.err
}

// Cached reads the fresh answers keys have: whether any of them is revoked,
// the keys with none, and the epoch a lookup of those starts in.
func (c *Cache[K]) Cached(keys []K) (revoked bool, missing []K, epoch uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.cfg.Now()
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

// SeedLive caches keys as not revoked until expires, as an answer about them
// all, read in epoch and fresh until then, said; nothing when a clear has
// happened since or the answer is no longer fresh. Seeded with a TTL of
// their own, keys would outlive an answer served from the cache near its
// end. A key that has a fresh answer or a lookup of its own in flight keeps
// that.
func (c *Cache[K]) SeedLive(keys []K, epoch uint64, expires time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.cfg.Now()
	if c.epoch != epoch || !expires.After(now) {
		return
	}
	for _, k := range keys {
		if e, ok := c.entries[k]; ok && e.expires.After(now) {
			continue
		}
		if _, ok := c.inflight[k]; ok {
			continue
		}
		c.makeRoomLocked()
		c.entries[k] = cacheEntry{expires: expires}
		c.order = append(c.order, queued[K]{key: k, expires: expires})
	}
}

// finish publishes call's answer to its waiters and caches it, unless it
// failed or a clear happened while it ran.
func (c *Cache[K]) finish(key K, call *lookupCall, epoch uint64) {
	c.mu.Lock()
	if c.inflight[key] == call {
		delete(c.inflight, key)
	}
	if call.err == nil && c.epoch == epoch {
		c.makeRoomLocked()
		expires := c.cfg.Now().Add(c.cfg.TTL)
		c.entries[key] = cacheEntry{revoked: call.revoked, expires: expires}
		c.order = append(c.order, queued[K]{key: key, expires: expires})
	}
	c.mu.Unlock()
	close(call.done)
}

// makeRoomLocked keeps the cache under maxEntries: expired entries go
// first and, if that frees nothing, the oldest live one does.
func (c *Cache[K]) makeRoomLocked() {
	c.sweepLocked()
	for len(c.entries) >= c.cfg.MaxEntries && c.popLocked() {
	}
}

// popLocked drops the oldest place in order, and its entry if the place is
// still the entry's own. It reports whether there was a place to drop.
func (c *Cache[K]) popLocked() bool {
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

// Clear drops every entry and detaches every lookup in flight, so the next
// check asks upstream rather than taking an answer read before now. A caller
// already waiting on a detached lookup still gets its answer: it asked before
// the clear, and the answer is not cached.
func (c *Cache[K]) Clear() {
	c.mu.Lock()
	c.epoch++
	clear(c.entries)
	clear(c.inflight)
	c.order = nil
	c.mu.Unlock()
}

// Sweep drops every expired entry and returns how many it dropped.
func (c *Cache[K]) Sweep() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sweepLocked()
}

// sweepLocked drops every expired entry, oldest first, and returns how many
// it dropped. It stops at the first place not yet expired: every place after
// it expires later.
func (c *Cache[K]) sweepLocked() int {
	now := c.cfg.Now()
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

// Len is how many answers the cache holds, fresh or not yet swept.
func (c *Cache[K]) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

// Queued is how many places the expiry order holds: never fewer than Len.
func (c *Cache[K]) Queued() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.order)
}
