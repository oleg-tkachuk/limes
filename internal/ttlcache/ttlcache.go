// Package ttlcache caches a yes/no answer per key for a TTL, bounded in
// size, with concurrent misses for one key sharing one lookup. limes's
// RevocationCache is two of these, one over each kind of key.
//
// The keys are spread over shards, each with a lock of its own, so callers
// asking about different keys do not queue on one lock: a verifier asks on
// every request, from every processor at once. Clears and Forgets are
// counted once for the whole cache, each Forget remembering the keys it
// outdated, so an answer read before one is never cached in any shard after
// it — a Clear's no answer, a Forget's no live answer about a key it named
// or tagged with one — while every other lookup in flight still caches its
// answer and still shares it with the callers that join it.
package ttlcache

import (
	"context"
	"errors"
	"github.com/oleg-tkachuk/limes/internal/errs"
	"hash/maphash"
	"math/rand/v2"
	"slices"
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
	// TTL is how long an answer is served, but for one an Extension
	// covers.
	TTL time.Duration
	// MaxEntries bounds the cache: past it, an entry makes room — the
	// oldest of its shard's, which in a cache big enough to have several
	// (minShardEntries each) is not always the oldest of all. At least one.
	MaxEntries int
	// Now is the clock.
	Now func() time.Time

	// MaxStale is how long past the TTL an answer is kept to stand in for
	// a lookup that fails: Get serves it, as read when it was, in place of
	// the failure. 0: an answer is served for the TTL only, and a failure
	// returned. A stale answer makes room before any fresh one does.
	MaxStale time.Duration
	// Answers reports an error a lookup returns as the upstream's answer
	// about the key, which no stale answer stands in for — not on record,
	// say — rather than a failure to give one. Nil: every error is a
	// failure. A caller whose own context has ended is given its error.
	Answers func(err error) bool
	// OnStale is told of every stale answer served: when it was read, and
	// the failure it stands in for. Nil: nothing is.
	OnStale func(at time.Time, cause error)
}

// Cache caches a yes/no answer per key for a TTL. The zero value is not
// usable: New makes one.
type Cache[K comparable] struct {
	cfg    Config
	seed   maphash.Seed
	shards []*shard[K]
	// ext is the Extension in force, nil for none. Each answer is judged
	// by the one in force when it is read.
	ext atomic.Pointer[Extension]
	// seq counts Clears and Forgets, each of which takes the next number;
	// a lookup, or a seed, starts in the one current when it began. Written
	// under forgetMu.
	seq atomic.Uint64
	// forgetMu orders the changes of seq, floor and recent, so an answer is
	// judged against a Forget whole or not at all.
	forgetMu sync.RWMutex
	// floor is the first seq an answer may have been read in to be cached:
	// a Clear moves it to its own, as does a Forget that overflows recent.
	floor uint64
	// recent holds, for each key a Forget named since floor, the seq of
	// the latest: a live answer read before it about the key, or tagged
	// with it, is not cached. At most maxRecentForgets keys.
	recent map[K]uint64
	// hits counts the questions answered from the cache, on a stripe each
	// caller picks at random.
	hits [hitStripes]stripe
	// lostUntil is until when, in nanoseconds of the clock, a revoked
	// answer dropped to make room would still have been kept; 0 for none.
	// Until then a stale live answer with tags fails rather than stand: the
	// cache cannot tell whether one of them was the revocation it let go.
	lostUntil atomic.Int64
}

// noteLost records that a revoked answer kept until until was dropped to
// make room.
func (c *Cache[K]) noteLost(until time.Time) {
	if until.IsZero() {
		return
	}
	for n := until.UnixNano(); ; {
		cur := c.lostUntil.Load()
		if cur >= n || c.lostUntil.CompareAndSwap(cur, n) {
			return
		}
	}
}

// revocationLost reports whether a revoked answer dropped to make room
// would still be kept at now.
func (c *Cache[K]) revocationLost(now time.Time) bool {
	until := c.lostUntil.Load()
	return until != 0 && now.UnixNano() < until
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
	// order holds the entries' places sorted by when each answer was read,
	// oldest first, which is the order they expire in: every entry is
	// judged by one TTL, or a later one by an Extension's, never shorter
	// (Extension). Sweeping stops at the first place not yet
	// expired, and making room takes the head, so neither walks the
	// entries. An answer is read when its lookup began, so one that ran
	// long, or an id seeded from a set's answer, is placed behind the later
	// places already queued (enqueueLocked): no further back than the
	// answers cached while it ran. A key Forget dropped can be cached again
	// before its old place expires: that place then names an entry no
	// longer its own, and dropping it leaves the new one.
	order []queued[K]
	// tagged indexes the live answers by their tags: for each tag, the
	// keys of this shard whose live answer named it when cached, so Forget
	// reads only the answers a key may outdate rather than every entry. A
	// key whose entry has gone, or was cached again without the tag,
	// keeps its place until Forget reads it or the index is rebuilt
	// (compactTaggedLocked). indexed counts the places, and liveTags those
	// the live entries own: the sum of their tags.
	tagged            map[K][]K
	indexed, liveTags int
	// misses and lookups count the shard's questions not answered from
	// it and the upstream lookups run for them; evictions the live entries
	// it dropped to stay within its bound, under mu.
	misses, lookups atomic.Uint64
	// stale counts the stale answers served in place of a failed lookup.
	stale     atomic.Uint64
	evictions uint64
	_         [cacheLine]byte
}

type cacheEntry[K comparable] struct {
	revoked bool
	// at is when the answer was read — when its lookup began, or, for an
	// id seeded from a set's answer, when that set's lookup began: fresh
	// until the TTL in force has passed since. Counted from the start, an
	// answer is never served longer than the TTL after the upstream was
	// asked, however long it took to answer.
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
	// seq is the one the lookup started in, and discarded reports that
	// finish did not cache its answer as a Clear or Forget since outdates
	// it: a caller that joined after one asks again (get).
	seq       uint64
	discarded bool
	// at is when the lookup started, the time finish caches its answer at:
	// it is fresh until the TTL in force has passed since.
	at time.Time
}

// errLookupAbandoned is what callers sharing a lookup receive when it ended
// without an answer — it panicked — rather than a "not revoked" nobody gave.
var errLookupAbandoned = errs.Broken("revocation lookup ended without an answer")

// New is an empty cache running on cfg. MaxEntries is shared out among the
// shards, so together they never hold more.
func New[K comparable](cfg Config) *Cache[K] {
	n := min(maxShards, max(cfg.MaxEntries/minShardEntries, 1))
	c := &Cache[K]{cfg: cfg, seed: maphash.MakeSeed(), shards: make([]*shard[K], n), recent: make(map[K]uint64)}
	for i := range c.shards {
		share := cfg.MaxEntries / n
		if i < cfg.MaxEntries%n {
			share++
		}
		c.shards[i] = &shard[K]{
			maxEntries: share, entries: make(map[K]cacheEntry[K]),
			inflight: make(map[K]*lookupCall[K]), tagged: make(map[K][]K),
		}
	}
	return c
}

// Now is the time by the cache's clock.
func (c *Cache[K]) Now() time.Time { return c.cfg.Now() }

// Extension serves the answers read after Since for TTL in place of the
// cache's own: a revocation signal that listens vouches for them, as it
// cannot for one read before it began to, which may have missed a
// revocation made while nothing listened. One read at Since itself cannot
// be told from one read before, and is not covered. It holds while the
// clock is before Until, the lease the signals vouch for: past it, every
// answer is judged by the cache's TTL, as with none. TTL is no shorter than the
// cache's, so an answer read later never expires sooner, and the expiry
// order stays the order the answers were read in.
type Extension struct {
	TTL          time.Duration
	Since, Until time.Time
}

// Extend puts ext in force, every answer cached already judged by it; nil
// ends the one in force, which makes every answer older than the cache's
// own TTL stale at once.
func (c *Cache[K]) Extend(ext *Extension) { c.ext.Store(ext) }

// judge is how the cache judges an answer's age at one moment.
type judge struct {
	now           time.Time
	ttl, maxStale time.Duration
	ext           *Extension
}

// judge is the cache's judge at now.
func (c *Cache[K]) judge(now time.Time) judge {
	return judge{now: now, ttl: c.cfg.TTL, maxStale: c.cfg.MaxStale, ext: c.ext.Load()}
}

// ttlOf is how long an answer read at at is served.
func (j judge) ttlOf(at time.Time) time.Duration {
	if j.ext != nil && j.now.Before(j.ext.Until) && at.After(j.ext.Since) {
		return j.ext.TTL
	}
	return j.ttl
}

// fresh reports whether an answer read at at may be served.
func (j judge) fresh(at time.Time) bool { return j.now.Before(at.Add(j.ttlOf(at))) }

// kept reports whether an answer read at at is kept: for its TTL, and
// MaxStale past it to stand in for a failed lookup.
func (j judge) kept(at time.Time) bool {
	return j.now.Before(at.Add(j.ttlOf(at) + j.maxStale))
}

// fresh reports whether an answer read at at may be served at now.
func (c *Cache[K]) fresh(at, now time.Time) bool { return c.judge(now).fresh(at) }

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
	revoked, _, at, err := c.GetWithTags(ctx, key, lookup)
	return revoked, at, err
}

// GetWithTags is GetTagged, also returning the tags of the answer it
// serves: the lookup's, or those cached beside it. A revoked answer served
// from a tag the cache holds revoked has none.
func (c *Cache[K]) GetWithTags(ctx context.Context, key K, lookup func(context.Context) (bool, []K, error)) (bool, []K, time.Time, error) {
	s := c.shardOf(key)
	s.mu.RLock()
	e, ok := s.entries[key]
	s.mu.RUnlock()
	if ok && c.fresh(e.at, c.cfg.Now()) {
		c.countHit()
		return e.revoked, slices.Clone(e.tags), e.at, nil
	}
	s.misses.Add(1)
	revoked, tags, at, err := c.get(ctx, s, key, lookup)
	if err != nil {
		return c.staleOr(ctx, s, key, revoked, tags, at, err)
	}
	return revoked, tags, at, nil
}

// staleOr serves the answer key had before its lookup failed with err, if
// it is no older than the TTL and MaxStale, and otherwise returns the
// lookup's result: err is the upstream's answer, the caller has gone, or
// there is no such answer.
func (c *Cache[K]) staleOr(ctx context.Context, s *shard[K], key K, revoked bool, tags []K, at time.Time, err error) (bool, []K, time.Time, error) {
	if c.cfg.MaxStale == 0 || ctx.Err() != nil || (c.cfg.Answers != nil && c.cfg.Answers(err)) {
		return revoked, tags, at, err
	}
	s.mu.RLock()
	e, ok := s.entries[key]
	s.mu.RUnlock()
	now := c.cfg.Now()
	if !ok || !c.judge(now).kept(e.at) {
		return revoked, tags, at, err
	}
	// A live answer covers its tags; with a revocation let go to make room,
	// the cache cannot say it still does.
	if !e.revoked && len(e.tags) > 0 && c.revocationLost(now) {
		return revoked, tags, at, err
	}
	s.stale.Add(1)
	if c.cfg.OnStale != nil {
		c.cfg.OnStale(e.at, err)
	}
	// A live answer covers its tags too; one the cache holds revoked, read
	// since, is not undone by it.
	if !e.revoked && c.HeldRevoked(e.tags) {
		return true, nil, e.at, nil
	}
	return e.revoked, slices.Clone(e.tags), e.at, nil
}

// get is Get past its first read, counting nothing more: a waiter that asks
// again is still the one question.
func (c *Cache[K]) get(ctx context.Context, s *shard[K], key K, lookup func(context.Context) (bool, []K, error)) (bool, []K, time.Time, error) {
	s.mu.Lock()
	// Read again under the write lock: another caller may have cached it, or
	// started its lookup, since.
	if e, ok := s.entries[key]; ok && c.fresh(e.at, c.cfg.Now()) {
		s.mu.Unlock()
		return e.revoked, slices.Clone(e.tags), e.at, nil
	}
	if call, ok := s.inflight[key]; ok {
		joined := c.seq.Load()
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
			// A Clear or Forget outdated the answer, and happened before
			// this caller joined: it asked after the revocation was heard,
			// so it is owed an answer read after it, as a caller that
			// started a lookup of its own then would have been.
			if call.discarded && joined > call.seq && ctx.Err() == nil {
				return c.get(ctx, s, key, lookup)
			}
			return call.revoked, slices.Clone(call.tags), call.at, call.err
		case <-ctx.Done():
			return false, nil, time.Time{}, ctx.Err()
		}
	}
	call := &lookupCall[K]{done: make(chan struct{}), err: errLookupAbandoned, at: c.cfg.Now(), seq: c.seq.Load()}
	s.inflight[key] = call
	s.mu.Unlock()
	s.lookups.Add(1)

	// Deferred so that a lookup that panics still releases its waiters and
	// the key.
	defer c.finish(s, key, call)
	call.revoked, call.tags, call.err = lookup(ctx)
	return call.revoked, slices.Clone(call.tags), call.at, call.err
}

// Cached reads the fresh answers keys have: whether any of them is revoked,
// the keys with none, and the epoch a lookup of those starts in. Answered
// in full, it counts a hit; otherwise the Get of the missing keys counts.
func (c *Cache[K]) Cached(keys []K) (revoked bool, missing []K, epoch uint64) {
	// Read first: a Clear while the keys are read makes this epoch stale,
	// and SeedLive then seeds nothing; a Forget, nothing it named.
	epoch = c.seq.Load()
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

// HeldRevoked reports whether the cache holds a revoked answer about any of
// keys, fresh or kept past the TTL for MaxStale: no revocation is undone, so
// a stale live answer about a question that spans one of them must not
// stand for it.
func (c *Cache[K]) HeldRevoked(keys []K) bool {
	j := c.judge(c.cfg.Now())
	for _, k := range keys {
		s := c.shardOf(k)
		s.mu.RLock()
		e, ok := s.entries[k]
		s.mu.RUnlock()
		if ok && e.revoked && j.kept(e.at) {
			return true
		}
	}
	return false
}

// countHit counts a question answered from the cache.
func (c *Cache[K]) countHit() {
	c.hits[rand.Uint32()%hitStripes].n.Add(1) //nolint:gosec // G404: picks a stripe to spread contention, nothing to guess
}

// SeedRevoked caches key as revoked, as an answer about a question that
// spans it, read now, in epoch, said; nothing when a Clear has happened
// since. A revoked answer is never undone, so it replaces whatever key held,
// and a Forget, which drops no revoked answer, does not keep it out.
func (c *Cache[K]) SeedRevoked(key K, epoch uint64) {
	s := c.shardOf(key)
	s.mu.Lock()
	defer s.mu.Unlock()
	// Checked under the shard's lock: a Clear empties the shard under it,
	// after counting itself.
	if !c.mayCacheLocked(epoch, true, key, nil) {
		return
	}
	now := c.cfg.Now()
	c.noteLost(s.makeRoomLocked(c.judge(now)))
	s.putLocked(key, cacheEntry[K]{revoked: true, at: now})
	s.enqueueLocked(key, now)
}

// SeedLive caches keys as not revoked, as an answer about them all read at
// at, in epoch, said; nothing when a Clear has happened since or the answer
// is no longer fresh, and no key a Forget since named. Seeded as read now,
// keys would outlive an answer served from the cache near its end. A key
// that has a fresh answer or a lookup of its own in flight keeps that.
func (c *Cache[K]) SeedLive(keys []K, epoch uint64, at time.Time) {
	j := c.judge(c.cfg.Now())
	if !j.fresh(at) {
		return
	}
	for _, k := range keys {
		s := c.shardOf(k)
		s.mu.Lock()
		// Checked under each shard's lock: a Clear or Forget drops what it
		// outdates from the shard under it, after counting itself.
		if !c.mayCacheLocked(epoch, false, k, nil) {
			s.mu.Unlock()
			continue
		}
		_, inflight := s.inflight[k]
		if e, ok := s.entries[k]; !inflight && (!ok || !j.fresh(e.at)) {
			c.noteLost(s.makeRoomLocked(j))
			s.putLocked(k, cacheEntry[K]{at: at})
			s.enqueueLocked(k, at)
		}
		s.mu.Unlock()
	}
}

// finish publishes call's answer to its waiters and caches it in s, unless
// it failed or a Clear or Forget that outdates it happened while it ran.
func (c *Cache[K]) finish(s *shard[K], key K, call *lookupCall[K]) {
	s.mu.Lock()
	if s.inflight[key] == call {
		delete(s.inflight, key)
	}
	// A live answer read before a revocation the cache learned while the
	// lookup ran does not replace it: no revocation is undone.
	held, ok := s.entries[key]
	undoes := ok && held.revoked && !call.revoked && !held.at.Before(call.at)
	call.discarded = call.err == nil && !c.mayCacheLocked(call.seq, call.revoked, key, call.tags)
	if call.err == nil && !call.discarded && !undoes {
		c.noteLost(s.makeRoomLocked(c.judge(c.cfg.Now())))
		s.putLocked(key, cacheEntry[K]{revoked: call.revoked, at: call.at, tags: call.tags})
		s.enqueueLocked(key, call.at)
	}
	s.mu.Unlock()
	close(call.done)
}

// orderSlack bounds the expiry order at this many places per entry the
// shard may hold: past it, the places no entry owns any more — left by
// Forget, or by a key cached again — are dropped.
const orderSlack = 2

// makeRoomLocked keeps the shard under its share of MaxEntries: entries
// past keep go first; then the oldest, which the order holds at its head —
// one past ttl, a stale one kept to stand in for a failed lookup, before
// any live one, which is counted an eviction. Each step takes the head of
// the order, so making room costs no walk of the entries. It returns until
// when the latest revoked answer it dropped would still have been kept,
// zero for none.
func (s *shard[K]) makeRoomLocked(j judge) time.Time {
	var lost time.Time
	s.compactLocked()
	// Once room is made: what it dropped no longer counts as owned.
	defer s.compactTaggedLocked()
	s.sweepLocked(j)
	for len(s.entries) >= s.maxEntries {
		if len(s.order) == 0 {
			return lost
		}
		q := s.order[0]
		e, ok := s.entries[q.key]
		s.popLocked()
		if !ok || !e.at.Equal(q.at) {
			continue
		}
		if until := e.at.Add(j.ttlOf(e.at) + j.maxStale); e.revoked && j.now.Before(until) && until.After(lost) {
			lost = until
		}
		if j.fresh(e.at) {
			s.evictions++
		}
	}
	return lost
}

// enqueueLocked places key, read at at, in the order: behind every place
// read no later, ahead of those read after it. A lookup's answer is read
// when it began, so it goes behind no more places than were queued while
// it ran; most go at the tail.
func (s *shard[K]) enqueueLocked(key K, at time.Time) {
	i := len(s.order)
	for i > 0 && s.order[i-1].at.After(at) {
		i--
	}
	s.order = slices.Insert(s.order, i, queued[K]{key: key, at: at})
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
		s.deleteLocked(q.key)
	}
	return true
}

// indexedTags are the tags of e the index holds it under: a live answer's,
// none for a revoked one, which Forget never drops.
func indexedTags[K comparable](e cacheEntry[K]) []K {
	if e.revoked {
		return nil
	}
	return e.tags
}

// putLocked caches e under key, in place of what key held, and indexes it
// by its tags.
func (s *shard[K]) putLocked(key K, e cacheEntry[K]) {
	s.deleteLocked(key)
	s.entries[key] = e
	tags := indexedTags(e)
	for _, t := range tags {
		s.tagged[t] = append(s.tagged[t], key)
	}
	s.indexed += len(tags)
	s.liveTags += len(tags)
}

// deleteLocked drops key's entry. Its places in the index stay until
// Forget reads them or compactTaggedLocked drops them.
func (s *shard[K]) deleteLocked(key K) {
	if e, ok := s.entries[key]; ok {
		s.liveTags -= len(indexedTags(e))
		delete(s.entries, key)
	}
}

// taggedSlack bounds the index at this many places per place a live entry
// owns, plus the shard's bound: past it, the index is rebuilt from the
// entries. The rebuild drops at least as many places as it reads entries,
// so it costs each place cached a constant share.
const taggedSlack = 2

// compactTaggedLocked rebuilds the index from the live entries once its
// places pass taggedSlack per place they own plus the shard's bound, so the
// index stays bounded however many tagged answers expire, make room or are
// replaced.
func (s *shard[K]) compactTaggedLocked() {
	if s.indexed <= taggedSlack*s.liveTags+s.maxEntries {
		return
	}
	clear(s.tagged)
	for key, e := range s.entries {
		for _, t := range indexedTags(e) {
			s.tagged[t] = append(s.tagged[t], key)
		}
	}
	s.indexed = s.liveTags
}

// forgetLocked drops the live answer of key, if it is one, and reports
// whether it did.
func (s *shard[K]) forgetLocked(key K) bool {
	if e, ok := s.entries[key]; ok && !e.revoked {
		s.deleteLocked(key)
		return true
	}
	return false
}

// forgetTaggedLocked drops every live answer of the shard tagged with tag,
// and its places in the index, and returns how many it dropped. A place
// whose entry has gone, or no longer names tag, drops nothing.
func (s *shard[K]) forgetTaggedLocked(tag K) int {
	keys, ok := s.tagged[tag]
	if !ok {
		return 0
	}
	dropped := 0
	for _, key := range keys {
		if e, ok := s.entries[key]; ok && !e.revoked && slices.Contains(e.tags, tag) {
			s.deleteLocked(key)
			dropped++
		}
	}
	delete(s.tagged, tag)
	s.indexed -= len(keys)
	return dropped
}

// sweepLocked drops every entry of the shard past its TTL and MaxStale,
// oldest first, and returns how many it dropped. It stops at the first
// place still kept: every place after it was read later, and an Extension
// serves no later answer for less, so it is kept too.
func (s *shard[K]) sweepLocked(j judge) int {
	dropped := 0
	for len(s.order) > 0 && !j.kept(s.order[0].at) {
		before := len(s.entries)
		s.popLocked()
		dropped += before - len(s.entries)
	}
	return dropped
}

// maxRecentForgets bounds the keys the cache remembers Forgets of. Past
// it, it forgets them all and moves the floor instead, as a Clear does: a
// flood of revocations keeps every answer read before it out of the cache,
// as every Forget once did, rather than grow the memory without bound.
const maxRecentForgets = 1 << 12

// mayCacheLocked reports whether an answer about key read in seq, revoked
// or live with tags, may be cached: no Clear has happened since, and, for a
// live one, no Forget since named key or any of tags. A revoked answer
// passes any Forget, which drops none. The caller holds key's shard lock:
// a Clear or Forget counted after this read drops what it outdates from
// the shard only once that lock is released.
func (c *Cache[K]) mayCacheLocked(seq uint64, revoked bool, key K, tags []K) bool {
	c.forgetMu.RLock()
	defer c.forgetMu.RUnlock()
	if seq < c.floor {
		return false
	}
	if revoked || seq == c.seq.Load() {
		return true
	}
	if c.recent[key] > seq {
		return false
	}
	for _, t := range tags {
		if c.recent[t] > seq {
			return false
		}
	}
	return true
}

// count takes the next seq for a Clear (all) or a Forget of gone, and
// records what it outdates: gone's keys in recent, or, for a Clear or a
// Forget recent has no room for, the floor.
func (c *Cache[K]) count(all bool, gone map[K]struct{}) {
	c.forgetMu.Lock()
	defer c.forgetMu.Unlock()
	n := c.seq.Load() + 1
	c.seq.Store(n)
	if all || len(c.recent)+len(gone) > maxRecentForgets {
		c.floor = n
		clear(c.recent)
		return
	}
	for k := range gone {
		c.recent[k] = n
	}
}

// Clear drops every entry and detaches every lookup in flight, so the next
// check asks upstream rather than taking an answer read before now. A caller
// already waiting on a detached lookup still gets its answer: it asked before
// the clear, and the answer is not cached. The clear is counted before any
// shard is emptied, so a lookup that finishes in a shard not yet emptied
// caches nothing the emptying would miss.
func (c *Cache[K]) Clear() {
	c.count(true, nil)
	for _, s := range c.shards {
		s.mu.Lock()
		clear(s.entries)
		clear(s.inflight)
		clear(s.tagged)
		s.indexed, s.liveTags = 0, 0
		s.order = nil
		s.mu.Unlock()
	}
}

// Forget drops the "not revoked" answers keys outdate — each key's own, and
// every one tagged with a key — and detaches the lookups in flight of keys,
// so the next check of those asks upstream. A lookup of another key that
// started before it may have read the state a revocation of keys changed,
// about an ancestor it names only once it answers: Forget remembers keys,
// and such a lookup's live answer, tagged with one, is served to the
// callers that joined before and cached for nobody, while one that joined
// after asks again. Every other answer is cached as though no Forget had
// happened. A "revoked" answer stays: no revocation is undone. It returns
// how many answers it dropped. It reads only the answers keys may outdate,
// through each shard's index of tags, so it costs as much as keys and what
// they drop, whatever the cache holds.
func (c *Cache[K]) Forget(keys []K) int {
	gone := make(map[K]struct{}, len(keys))
	own := make(map[*shard[K]][]K, len(c.shards))
	for _, k := range keys {
		if _, dup := gone[k]; dup {
			continue
		}
		gone[k] = struct{}{}
		s := c.shardOf(k)
		own[s] = append(own[s], k)
	}
	// Counted before any shard is walked, as a Clear is: a lookup finishing
	// in a shard not yet walked either sees the count or is dropped by the
	// walk.
	c.count(false, gone)
	dropped := 0
	for _, s := range c.shards {
		s.mu.Lock()
		// A key's own answer and lookup are in its shard; an answer tagged
		// with it may be in any.
		for _, k := range own[s] {
			delete(s.inflight, k)
			if s.forgetLocked(k) {
				dropped++
			}
		}
		for k := range gone {
			dropped += s.forgetTaggedLocked(k)
		}
		s.compactLocked()
		s.mu.Unlock()
	}
	return dropped
}

// Sweep drops every entry past the TTL and MaxStale, and returns how many
// it dropped.
func (c *Cache[K]) Sweep() int {
	j := c.judge(c.cfg.Now())
	dropped := 0
	for _, s := range c.shards {
		s.mu.Lock()
		dropped += s.sweepLocked(j)
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
	// Stale counts the stale answers served in place of a failed lookup.
	Stale uint64
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
		st.Stale += s.stale.Load()
		st.Evictions += s.evictions
		st.Entries += len(s.entries)
		s.mu.RUnlock()
	}
	return st
}
