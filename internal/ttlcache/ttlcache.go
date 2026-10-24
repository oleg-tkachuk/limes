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
	"math/rand/v2"
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

// hitStripes is how many counters the hits are spread over, each on a line
// of its own: callers of one hot key share its shard, and a single counter
// there would have them take turns on one more line for every hit.
const hitStripes = 16

// stripe is one of the counters hits are spread over.
type stripe struct {
	n atomic.Uint64
	_ [cacheLine - 8]byte
}

// Config is what a Cache runs on. Every field is required: the caller
// resolves its defaults.
type Config struct {
	// TTL is how long an answer is served, until SetTTL changes it.
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
	// ttl is how long an answer is served, in nanoseconds: Config.TTL until
	// SetTTL. Each answer is judged by the one in force when it is read.
	ttl atomic.Int64
	// epoch counts clears. A lookup that started before one may have read
	// the state it invalidated, so its answer is not cached.
	epoch atomic.Uint64
	// hits counts the questions answered from the cache, on a stripe each
	// caller picks at random.
	hits [hitStripes]stripe
}

// shard is the entries, lookups and expiry order of the keys that hash to
// it, under its own lock, bounded by its share of the cache's MaxEntries.
type shard[K comparable] struct {
	// mu is read-locked to serve a fresh answer, the common case, so
	// callers of one hot key do not take turns; anything that writes locks
	// it.
	mu         sync.RWMutex
	maxEntries int
	entries    map[K]cacheEntry[K]
	inflight   map[K]*lookupCall[K]
	// order holds the entries in the order they were cached, which every
	// entry being judged by one TTL makes the order they expire in, but for
	// an id seeded from a set's answer, which expires with that answer: sweeping
	// stops at the first place not yet expired, so such an id is dropped a
	// little late, and never served past its expiry. A key Forget dropped
	// can be cached again before its old place expires: that place then
	// names an entry no longer its own, and dropping it leaves the new one.
	order []queued[K]
	// misses and lookups count the shard's questions not answered from
	// it and the upstream lookups run for them; evictions the live entries
	// it dropped to stay within its bound, under mu.
	misses, lookups atomic.Uint64
	evictions       uint64
	_               [cacheLine]byte
}

type cacheEntry[K comparable] struct {
	revoked bool
	// at is when the answer was cached — when its lookup finished, or, for
	// an id seeded from a set's answer, when that set's lookup began:
	// fresh until the TTL in force has passed since.
	at time.Time
	// tags are the keys a "not revoked" answer also covers, which Forget
	// drops it with (GetTagged).
	tags []K
}

// queued is a key's place in Cache.order.
type queued[K comparable] struct {
	key K
	at  time.Time
}

type lookupCall[K comparable] struct {
	done    chan struct{}
	revoked bool
	tags    []K
	err     error
	// at is when the lookup started, no later than the entry finish caches:
	// its answer is fresh until the TTL in force has passed since.
	at time.Time
}

// errLookupAbandoned is what callers sharing a lookup receive when it ended
// without an answer — it panicked — rather than a "not revoked" nobody gave.
var errLookupAbandoned = errors.New("limes: revocation lookup ended without an answer")

// New is an empty cache running on cfg. MaxEntries is shared out among the
// shards, so together they never hold more.
func New[K comparable](cfg Config) *Cache[K] {
	n := min(maxShards, max(cfg.MaxEntries/minShardEntries, 1))
	c := &Cache[K]{cfg: cfg, seed: maphash.MakeSeed(), shards: make([]*shard[K], n)}
	c.ttl.Store(int64(cfg.TTL))
	for i := range c.shards {
		share := cfg.MaxEntries / n
		if i < cfg.MaxEntries%n {
			share++
		}
		c.shards[i] = &shard[K]{maxEntries: share, entries: make(map[K]cacheEntry[K]), inflight: make(map[K]*lookupCall[K])}
	}
	return c
}

// Now is the time by the cache's clock.
func (c *Cache[K]) Now() time.Time { return c.cfg.Now() }

// TTL is how long an answer is served now.
func (c *Cache[K]) TTL() time.Duration { return time.Duration(c.ttl.Load()) }

// SetTTL changes how long an answer is served, every answer cached already
// among them: a shorter TTL makes an answer read longer ago than it stale
// at once.
func (c *Cache[K]) SetTTL(ttl time.Duration) { c.ttl.Store(int64(ttl)) }

// fresh reports whether an answer read at at may be served at now.
func (c *Cache[K]) fresh(at, now time.Time) bool { return now.Before(at.Add(c.TTL())) }

// shardOf is the shard key belongs to.
func (c *Cache[K]) shardOf(key K) *shard[K] {
	return c.shards[maphash.Comparable(c.seed, key)%uint64(len(c.shards))]
}

// Get serves a fresh entry for key, or runs lookup once for every
// concurrent caller missing it, with when the answer stops being fresh. An
// error is returned and never cached. It counts one hit or one miss.
func (c *Cache[K]) Get(ctx context.Context, key K, lookup func(context.Context) (bool, error)) (bool, time.Time, error) {
	return c.GetTagged(ctx, key, func(ctx context.Context) (bool, []K, error) {
		revoked, err := lookup(ctx)
		return revoked, nil, err
	})
}

// GetTagged is Get with a lookup that also names the keys its answer
// covers, its tags: Forget of any of them drops a "not revoked" answer
// cached under key.
func (c *Cache[K]) GetTagged(ctx context.Context, key K, lookup func(context.Context) (bool, []K, error)) (bool, time.Time, error) {
	s := c.shardOf(key)
	s.mu.RLock()
	e, ok := s.entries[key]
	s.mu.RUnlock()
	if ok && c.fresh(e.at, c.cfg.Now()) {
		c.countHit()
		return e.revoked, e.at, nil
	}
	s.misses.Add(1)
	return c.get(ctx, s, key, lookup)
}

// get is Get past its first read, counting nothing more: a waiter that asks
// again is still the one question.
func (c *Cache[K]) get(ctx context.Context, s *shard[K], key K, lookup func(context.Context) (bool, []K, error)) (bool, time.Time, error) {
	s.mu.Lock()
	// Read again under the write lock: another caller may have cached it, or
	// started its lookup, since.
	if e, ok := s.entries[key]; ok && c.fresh(e.at, c.cfg.Now()) {
		s.mu.Unlock()
		return e.revoked, e.at, nil
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
				return c.get(ctx, s, key, lookup)
			}
			return call.revoked, call.at, call.err
		case <-ctx.Done():
			return false, time.Time{}, ctx.Err()
		}
	}
	call := &lookupCall[K]{done: make(chan struct{}), err: errLookupAbandoned, at: c.cfg.Now()}
	s.inflight[key] = call
	epoch := c.epoch.Load()
	s.mu.Unlock()
	s.lookups.Add(1)

	// Deferred so that a lookup that panics still releases its waiters and
	// the key.
	defer c.finish(s, key, call, epoch)
	call.revoked, call.tags, call.err = lookup(ctx)
	return call.revoked, call.at, call.err
}

// Cached reads the fresh answers keys have: whether any of them is revoked,
// the keys with none, and the epoch a lookup of those starts in. Answered
// in full, it counts a hit; otherwise the Get of the missing keys counts.
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
		case !ok || !c.fresh(e.at, now):
			missing = append(missing, k)
		case e.revoked:
			c.countHit()
			return true, nil, epoch
		}
	}
	if len(missing) == 0 {
		c.countHit()
	}
	return false, missing, epoch
}

// countHit counts a question answered from the cache.
func (c *Cache[K]) countHit() {
	c.hits[rand.Uint32()%hitStripes].n.Add(1) //nolint:gosec // G404: picks a stripe to spread contention, nothing to guess
}

// SeedLive caches keys as not revoked, as an answer about them all read at
// at, in epoch, said; nothing when a clear has happened since or the answer
// is no longer fresh. Seeded as read now, keys would outlive an answer
// served from the cache near its end. A key that has a fresh answer or a
// lookup of its own in flight keeps that.
func (c *Cache[K]) SeedLive(keys []K, epoch uint64, at time.Time) {
	now := c.cfg.Now()
	if !c.fresh(at, now) {
		return
	}
	ttl := c.TTL()
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
		if e, ok := s.entries[k]; !inflight && (!ok || !c.fresh(e.at, now)) {
			s.makeRoomLocked(now, ttl)
			s.entries[k] = cacheEntry[K]{at: at}
			s.order = append(s.order, queued[K]{key: k, at: at})
		}
		s.mu.Unlock()
	}
}

// finish publishes call's answer to its waiters and caches it in s, unless
// it failed or a clear happened while it ran.
func (c *Cache[K]) finish(s *shard[K], key K, call *lookupCall[K], epoch uint64) {
	s.mu.Lock()
	if s.inflight[key] == call {
		delete(s.inflight, key)
	}
	if call.err == nil && c.epoch.Load() == epoch {
		now := c.cfg.Now()
		s.makeRoomLocked(now, c.TTL())
		s.entries[key] = cacheEntry[K]{revoked: call.revoked, at: now, tags: call.tags}
		s.order = append(s.order, queued[K]{key: key, at: now})
	}
	s.mu.Unlock()
	close(call.done)
}

// orderSlack bounds the expiry order at this many places per entry the
// shard may hold: past it, the places no entry owns any more — left by
// Forget, or by a key cached again — are dropped.
const orderSlack = 2

// makeRoomLocked keeps the shard under its share of MaxEntries: expired
// entries go first — those the expiry order reaches, then any other, such
// as an id seeded behind a later answer — and, if that frees nothing, the
// oldest live one does.
func (s *shard[K]) makeRoomLocked(now time.Time, ttl time.Duration) {
	s.compactLocked()
	s.sweepLocked(now, ttl)
	if len(s.entries) >= s.maxEntries {
		s.dropExpiredLocked(now, ttl)
	}
	for len(s.entries) >= s.maxEntries {
		before := len(s.entries)
		if !s.popLocked() {
			return
		}
		if len(s.entries) < before {
			s.evictions++
		}
	}
}

// dropExpiredLocked drops every expired entry, wherever its place in the
// order: one seeded with an older answer's time sits behind later places,
// where sweepLocked does not reach it. It returns how many it dropped.
func (s *shard[K]) dropExpiredLocked(now time.Time, ttl time.Duration) int {
	dropped := 0
	for key, e := range s.entries {
		if !now.Before(e.at.Add(ttl)) {
			delete(s.entries, key)
			dropped++
		}
	}
	return dropped
}

// compactLocked drops the places of the expiry order no entry owns, once
// they outnumber the shard's bound orderSlack times over, so the order
// stays bounded however many answers Forget drops.
func (s *shard[K]) compactLocked() {
	if len(s.order) <= orderSlack*s.maxEntries {
		return
	}
	kept := s.order[:0]
	for _, q := range s.order {
		if e, ok := s.entries[q.key]; ok && e.at.Equal(q.at) {
			kept = append(kept, q)
		}
	}
	clear(s.order[len(kept):])
	s.order = kept
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
	if e, ok := s.entries[q.key]; ok && e.at.Equal(q.at) {
		delete(s.entries, q.key)
	}
	return true
}

// sweepLocked drops every expired entry of the shard, oldest first, and
// returns how many it dropped. It stops at the first place not yet expired:
// every place after it expires later.
func (s *shard[K]) sweepLocked(now time.Time, ttl time.Duration) int {
	dropped := 0
	for len(s.order) > 0 && !now.Before(s.order[0].at.Add(ttl)) {
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

// Forget drops the "not revoked" answers keys outdate — each key's own, and
// every one tagged with a key — and detaches every lookup in flight, so the
// next check of those asks upstream. It is counted as a clear is, so a
// lookup that started before it caches nothing: it may have read the state
// a revocation of keys changed, about a key it does not name yet. A
// "revoked" answer stays: no revocation is undone. It returns how many
// answers it dropped.
func (c *Cache[K]) Forget(keys []K) int {
	gone := make(map[K]struct{}, len(keys))
	for _, k := range keys {
		gone[k] = struct{}{}
	}
	outdated := func(key K, e cacheEntry[K]) bool {
		if e.revoked {
			return false
		}
		if _, ok := gone[key]; ok {
			return true
		}
		for _, t := range e.tags {
			if _, ok := gone[t]; ok {
				return true
			}
		}
		return false
	}
	c.epoch.Add(1)
	dropped := 0
	for _, s := range c.shards {
		s.mu.Lock()
		clear(s.inflight)
		for key, e := range s.entries {
			if outdated(key, e) {
				delete(s.entries, key)
				dropped++
			}
		}
		s.compactLocked()
		s.mu.Unlock()
	}
	return dropped
}

// Sweep drops every expired entry and returns how many it dropped.
func (c *Cache[K]) Sweep() int {
	now, ttl := c.cfg.Now(), c.TTL()
	dropped := 0
	for _, s := range c.shards {
		s.mu.Lock()
		dropped += s.sweepLocked(now, ttl)
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
		s.mu.RLock()
		n += count(s)
		s.mu.RUnlock()
	}
	return n
}

// Stats is what a Cache has counted since New, and what it holds now.
type Stats struct {
	// Hits and Misses count the questions answered from the cache and not.
	Hits, Misses uint64
	// Lookups counts the upstream lookups run for the misses: fewer than
	// Misses when concurrent ones share a lookup.
	Lookups uint64
	// Evictions counts the live entries dropped to stay within MaxEntries.
	Evictions uint64
	// Entries is Len.
	Entries int
}

// Stats sums every shard's counts, each shard read under its lock.
func (c *Cache[K]) Stats() Stats {
	var st Stats
	for i := range c.hits {
		st.Hits += c.hits[i].n.Load()
	}
	for _, s := range c.shards {
		s.mu.RLock()
		st.Misses += s.misses.Load()
		st.Lookups += s.lookups.Load()
		st.Evictions += s.evictions
		st.Entries += len(s.entries)
		s.mu.RUnlock()
	}
	return st
}
