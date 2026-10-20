// Package ttlcache caches a yes/no answer per key for a TTL, bounded in
// size, with concurrent misses for one key sharing one lookup. limes's
// RevocationCache is two of these, one over each kind of key.
//
// The keys are spread over shards, each with a lock of its own, so callers
// asking about different keys do not queue on one lock: a verifier asks on
// every request, from every processor at once. Clears are counted once for
// the whole cache, so an answer read before one is never cached in any
// shard after it.
package ttlcache

import (
	"context"
	"errors"
	"hash/maphash"
	"sync"
	"sync/atomic"
	"time"
)

// maxShards is the most shards a cache spreads its keys over, and
// minShardEntries the fewest entries a shard is given: a cache too small to
// give every shard that many has fewer, down to one, whose oldest entry is
// then the oldest of all. Shared out finer, keys that happen to hash
// together would push each other out long before the cache is full.
const (
	maxShards       = 16
	minShardEntries = 256
)

// cacheLine is how far apart two shards' locks are kept, so a processor
// taking one does not take the other's cache line from another processor.
const cacheLine = 64

// Config is what a Cache runs on. Every field is required: the caller
// resolves its defaults.
type Config struct {
	// TTL is how long an answer is served.
	TTL time.Duration
	// MaxEntries bounds the cache: past it, an entry makes room — the
	// oldest of its shard's, which in a cache big enough to have several
	// (minShardEntries each) is not always the oldest of all. At least one.
	MaxEntries int
	// Now is the clock.
	Now func() time.Time
}

// Cache caches a yes/no answer per key for a TTL. The zero value is not
// usable: New makes one.
type Cache[K comparable] struct {
	cfg    Config
	seed   maphash.Seed
	shards []*shard[K]
	// epoch counts clears. A lookup that started before one may have read
	// the state it invalidated, so its answer is not cached.
	epoch atomic.Uint64
}

// shard is the entries, lookups and expiry order of the keys that hash to
// it, under its own lock, bounded by its share of the cache's MaxEntries.
type shard[K comparable] struct {
	// mu is read-locked to serve a fresh answer, the common case, so
	// callers of one hot key do not take turns; anything that writes locks
	// it.
	mu         sync.RWMutex
	maxEntries int
	entries    map[K]cacheEntry
	inflight   map[K]*lookupCall
	// order holds the entries in the order they were cached, which every
	// entry sharing one TTL makes the order they expire in, but for an id
	// seeded from a set's answer, which expires with that answer: sweeping
	// stops at the first place not yet expired, so such an id is dropped a
	// little late, and never served past its expiry. One place per entry,
	// since a key is cached again only once its place has expired and been
	// dropped.
	order []queued[K]
	_     [cacheLine]byte
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

// New is an empty cache running on cfg. MaxEntries is shared out among the
// shards, so together they never hold more.
func New[K comparable](cfg Config) *Cache[K] {
	n := min(maxShards, max(cfg.MaxEntries/minShardEntries, 1))
	c := &Cache[K]{cfg: cfg, seed: maphash.MakeSeed(), shards: make([]*shard[K], n)}
	for i := range c.shards {
		share := cfg.MaxEntries / n
		if i < cfg.MaxEntries%n {
			share++
		}
		c.shards[i] = &shard[K]{maxEntries: share, entries: make(map[K]cacheEntry), inflight: make(map[K]*lookupCall)}
	}
	return c
}

// shardOf is the shard key belongs to.
func (c *Cache[K]) shardOf(key K) *shard[K] {
	return c.shards[maphash.Comparable(c.seed, key)%uint64(len(c.shards))]
}

// Get serves a fresh entry for key, or runs lookup once for every
// concurrent caller missing it, with when the answer stops being fresh. An
// error is returned and never cached.
func (c *Cache[K]) Get(ctx context.Context, key K, lookup func(context.Context) (bool, error)) (bool, time.Time, error) {
	s := c.shardOf(key)
	s.mu.RLock()
	e, ok := s.entries[key]
	s.mu.RUnlock()
	if ok && e.expires.After(c.cfg.Now()) {
		return e.revoked, e.expires, nil
	}
	s.mu.Lock()
	// Read again under the write lock: another caller may have cached it, or
	// started its lookup, since.
	if e, ok := s.entries[key]; ok && e.expires.After(c.cfg.Now()) {
		s.mu.Unlock()
		return e.revoked, e.expires, nil
	}
	if call, ok := s.inflight[key]; ok {
		s.mu.Unlock()
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
	s.inflight[key] = call
	epoch := c.epoch.Load()
	s.mu.Unlock()

	// Deferred so that a lookup that panics still releases its waiters and
	// the key.
	defer c.finish(s, key, call, epoch)
	call.revoked, call.err = lookup(ctx)
	return call.revoked, call.expires, call.err
}

// Cached reads the fresh answers keys have: whether any of them is revoked,
// the keys with none, and the epoch a lookup of those starts in.
func (c *Cache[K]) Cached(keys []K) (revoked bool, missing []K, epoch uint64) {
	// Read first: a clear while the keys are read makes this epoch stale,
	// and SeedLive then seeds nothing.
	epoch = c.epoch.Load()
	now := c.cfg.Now()
	for _, k := range keys {
		s := c.shardOf(k)
		s.mu.RLock()
		e, ok := s.entries[k]
		s.mu.RUnlock()
		switch {
		case !ok || !e.expires.After(now):
			missing = append(missing, k)
		case e.revoked:
			return true, nil, epoch
		}
	}
	return false, missing, epoch
}

// SeedLive caches keys as not revoked until expires, as an answer about them
// all, read in epoch and fresh until then, said; nothing when a clear has
// happened since or the answer is no longer fresh. Seeded with a TTL of
// their own, keys would outlive an answer served from the cache near its
// end. A key that has a fresh answer or a lookup of its own in flight keeps
// that.
func (c *Cache[K]) SeedLive(keys []K, epoch uint64, expires time.Time) {
	now := c.cfg.Now()
	if !expires.After(now) {
		return
	}
	for _, k := range keys {
		s := c.shardOf(k)
		s.mu.Lock()
		// Checked under each shard's lock: a clear empties the shard under
		// it, after counting itself.
		if c.epoch.Load() != epoch {
			s.mu.Unlock()
			return
		}
		_, inflight := s.inflight[k]
		if e, ok := s.entries[k]; !inflight && (!ok || !e.expires.After(now)) {
			s.makeRoomLocked(now)
			s.entries[k] = cacheEntry{expires: expires}
			s.order = append(s.order, queued[K]{key: k, expires: expires})
		}
		s.mu.Unlock()
	}
}

// finish publishes call's answer to its waiters and caches it in s, unless
// it failed or a clear happened while it ran.
func (c *Cache[K]) finish(s *shard[K], key K, call *lookupCall, epoch uint64) {
	s.mu.Lock()
	if s.inflight[key] == call {
		delete(s.inflight, key)
	}
	if call.err == nil && c.epoch.Load() == epoch {
		now := c.cfg.Now()
		s.makeRoomLocked(now)
		expires := now.Add(c.cfg.TTL)
		s.entries[key] = cacheEntry{revoked: call.revoked, expires: expires}
		s.order = append(s.order, queued[K]{key: key, expires: expires})
	}
	s.mu.Unlock()
	close(call.done)
}

// makeRoomLocked keeps the shard under its share of MaxEntries: expired
// entries go first and, if that frees nothing, the oldest live one does.
func (s *shard[K]) makeRoomLocked(now time.Time) {
	s.sweepLocked(now)
	for len(s.entries) >= s.maxEntries && s.popLocked() {
	}
}

// popLocked drops the oldest place in order, and its entry if the place is
// still the entry's own. It reports whether there was a place to drop.
func (s *shard[K]) popLocked() bool {
	if len(s.order) == 0 {
		return false
	}
	q := s.order[0]
	s.order[0] = queued[K]{} // let the key go
	s.order = s.order[1:]
	if e, ok := s.entries[q.key]; ok && e.expires.Equal(q.expires) {
		delete(s.entries, q.key)
	}
	return true
}

// sweepLocked drops every expired entry of the shard, oldest first, and
// returns how many it dropped. It stops at the first place not yet expired:
// every place after it expires later.
func (s *shard[K]) sweepLocked(now time.Time) int {
	dropped := 0
	for len(s.order) > 0 && !s.order[0].expires.After(now) {
		before := len(s.entries)
		s.popLocked()
		dropped += before - len(s.entries)
	}
	return dropped
}

// Clear drops every entry and detaches every lookup in flight, so the next
// check asks upstream rather than taking an answer read before now. A caller
// already waiting on a detached lookup still gets its answer: it asked before
// the clear, and the answer is not cached. The clear is counted before any
// shard is emptied, so a lookup that finishes in a shard not yet emptied
// caches nothing the emptying would miss.
func (c *Cache[K]) Clear() {
	c.epoch.Add(1)
	for _, s := range c.shards {
		s.mu.Lock()
		clear(s.entries)
		clear(s.inflight)
		s.order = nil
		s.mu.Unlock()
	}
}

// Sweep drops every expired entry and returns how many it dropped.
func (c *Cache[K]) Sweep() int {
	now := c.cfg.Now()
	dropped := 0
	for _, s := range c.shards {
		s.mu.Lock()
		dropped += s.sweepLocked(now)
		s.mu.Unlock()
	}
	return dropped
}

func isContextErr(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// Len is how many answers the cache holds, fresh or not yet swept.
func (c *Cache[K]) Len() int {
	return c.sum(func(s *shard[K]) int { return len(s.entries) })
}

// Queued is how many places the expiry order holds: never fewer than Len.
func (c *Cache[K]) Queued() int {
	return c.sum(func(s *shard[K]) int { return len(s.order) })
}

// sum is count over every shard, each read under its lock.
func (c *Cache[K]) sum(count func(*shard[K]) int) int {
	n := 0
	for _, s := range c.shards {
		s.mu.Lock()
		n += count(s)
		s.mu.Unlock()
	}
	return n
}
