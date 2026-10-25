package limes

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes/internal/ttlcache"
)

// RevocationLookup is the seam the cache wraps. A persistent store
// implements it; tests substitute a stub. Like Store.IsRevoked, it answers
// for the capability's whole delegation chain.
type RevocationLookup interface {
	// IsRevoked reports whether id, or any ancestor of it, is revoked. An
	// error is no answer: the verifier refuses rather than assume not.
	IsRevoked(ctx context.Context, id uuid.UUID) (bool, error)
}

// RevocationLineageLookup is a RevocationLookup that also names the
// ancestors an answer covers. A RevocationCache over one keeps them beside
// each "not revoked" answer, so ClearRevoked drops only the answers a
// revocation outdates rather than every one. memstore's Store and pgstore's
// are each one.
type RevocationLineageLookup interface {
	RevocationLookup
	// RevocationLineage answers as IsRevoked does, and names the ancestors
	// of id still on record that the answer read: every one, when id is
	// not revoked. An id not on record is ErrNotFound: the cache answers
	// it not revoked, as IsRevoked does, but keeps no answer, which no
	// ancestor's revocation could reach. One that answers such an id not
	// revoked instead is cached as any other.
	RevocationLineage(ctx context.Context, id uuid.UUID) (RevocationAnswer, error)
}

// RevocationAnswer is RevocationLineageLookup's answer about a capability.
type RevocationAnswer struct {
	// Revoked is IsRevoked's answer: the capability, or an ancestor still
	// on record, is revoked.
	Revoked bool
	// Ancestors are the ancestors still on record the answer read, nearest
	// first: every one, when the capability is not revoked. A revoked
	// answer needs none, as no revocation is undone.
	Ancestors []uuid.UUID
	_         noCompare
}

// ErrNoRevocationLineage is NewRevocationCache refusing, under
// RevocationCacheConfig.RequireLineage, a Capabilities upstream that is no
// RevocationLineageLookup.
var ErrNoRevocationLineage = errors.New("limes: the revocation upstream names no lineage")

// DefaultRevocationCacheTTL bounds how long a cached answer is served. A
// revocation therefore takes effect within this window on every verifier
// that did not perform it and hears no signal of it. It suits the requests
// of one exchange; a token verified less often — once per step of an agent
// — wants a longer bound while a revocation signal vouches for the cache:
// RevocationCacheConfig.SyncedTTL.
const DefaultRevocationCacheTTL = 2 * time.Second

// MaxRevocationCacheSyncedTTL bounds RevocationCacheConfig.SyncedTTL. A
// signal that listens and still carries nothing — a notification its
// source never sends — leaves a revocation unseen for as long as the bound
// in force, so even a synced cache asks again within a day.
const MaxRevocationCacheSyncedTTL = 24 * time.Hour

// DefaultRevocationCacheEntries bounds the cache. Past it, the oldest entry
// makes room for the new one: a flood of distinct ids costs extra lookups,
// never unbounded memory, and never the answers cached for everyone else.
const DefaultRevocationCacheEntries = 100_000

// RevocationCache is a small in-memory cache for revocation answers: of a
// capability, IsRevoked, and of a Biscuit copy, IsBiscuitRevoked. One cache
// serves both, so one Clear, wired to a revocation notification, reaches
// every answer a verifier holds.
//
// A cached answer about a capability is also an answer about its ancestors,
// so revoking a parent reaches a child's cached "live" entry only when that
// entry expires — within the TTL, like any other revocation seen from
// another process — or when Clear drops it. ClearRevoked drops only the
// answers a revocation of some ids outdates, when the capabilities' upstream
// is a RevocationLineageLookup and the cache knows each answer's ancestors;
// over any other upstream it is Clear.
//
// Concurrent misses for the same question share one upstream call.
type RevocationCache struct {
	capabilities RevocationLookup
	// lineage is capabilities as a RevocationLineageLookup; nil when it is
	// not one.
	lineage RevocationLineageLookup
	// biscuits is nil for a cache that answers no Biscuit question.
	biscuits BiscuitRevocationLookup
	caps     *ttlcache.Cache[uuid.UUID]
	copies   *ttlcache.Cache[string]
	clears   atomic.Uint64
	// ttl and syncedTTL are how long an answer is served without a
	// revocation signal and while one vouches for the cache (synced).
	ttl, syncedTTL time.Duration
	synced         atomic.Bool
	// open is how many RevocationSignals are open, and listening how many
	// of them listen, under syncMu, which orders the changes of synced and
	// of the TTL in force.
	syncMu          sync.Mutex
	open, listening int
	// syncs counts the signals' Listening, syncsLost their NotListening,
	// and lastSyncLoss is the time of the last: nil, none yet.
	syncs, syncsLost atomic.Uint64
	lastSyncLoss     atomic.Pointer[time.Time]
	// signals counts the signals' Revoked and RevokedAll, and signalsWhole
	// the RevokedAll.
	signals, signalsWhole atomic.Uint64
	// narrowClears counts the calls to ClearRevoked that did not Clear, and
	// clearsWithoutLineage those that did.
	narrowClears, clearsWithoutLineage atomic.Uint64
}

// RevocationCacheConfig configures a RevocationCache.
type RevocationCacheConfig struct {
	// Capabilities answers IsRevoked for what the cache does not hold.
	// Required. When it is a RevocationLineageLookup the cache asks
	// RevocationLineage in place of IsRevoked, and ClearRevoked drops only
	// what a revocation outdates. A decorator of a store keeps that only by
	// having the method itself: a struct embedding limes.Store has no
	// RevocationLineage to promote, so it forwards one —
	//
	//	func (c counting) RevocationLineage(ctx context.Context, id uuid.UUID) (limes.RevocationAnswer, error) {
	//		return c.lineage.RevocationLineage(ctx, id)
	//	}
	//
	// — and counts there what it counts in IsRevoked.
	Capabilities RevocationLookup
	// RequireLineage refuses a Capabilities that is no
	// RevocationLineageLookup, with ErrNoRevocationLineage, where the cache
	// would otherwise clear whole on every ClearRevoked.
	RequireLineage bool
	// Biscuits answers IsBiscuitRevoked for what the cache does not hold.
	// Optional: without it the cache refuses every Biscuit question, and
	// NewVerifier refuses it as BiscuitConfig.Revocations. A store that
	// implements both, as pgstore's does, is passed as both. A nil pointer
	// held here is refused.
	Biscuits BiscuitRevocationLookup
	// TTL is how long an answer is served. Zero: DefaultRevocationCacheTTL.
	// It bounds how long a revocation may go unseen where no revocation
	// signal reaches the cache.
	TTL time.Duration
	// SyncedTTL is how long an answer is served while revocation signals
	// vouch for the cache: while at least one RevocationSignal is open and
	// every open one listens, as pgstore.RevocationWatcher's does while
	// its connection listens. A revocation then reaches the cache at once,
	// so the TTL can be long. Otherwise TTL bounds every answer, those
	// cached under SyncedTTL among them. Zero: TTL; shorter than TTL, or
	// longer than MaxRevocationCacheSyncedTTL, is refused.
	SyncedTTL time.Duration
	// MaxEntries bounds each kind of answer on its own, so a flood of one
	// never evicts the other. Zero: DefaultRevocationCacheEntries. A Biscuit
	// answer is one entry per revocation id, and one per set of ids asked
	// about together, whose key holds every id of the set — up to
	// MaxBiscuitBlocks+1 block signatures: size memory for MaxEntries times
	// the largest such key, not one id.
	MaxEntries int
	// Now is the clock. Nil: time.Now.
	Now func() time.Time
}

// NewRevocationCache wires a cache over cfg's upstreams. A negative TTL or
// bound is refused; a deployment that wants no cache passes its store to the
// verifier directly.
func NewRevocationCache(cfg RevocationCacheConfig) (*RevocationCache, error) {
	if isNilValue(cfg.Capabilities) {
		return nil, errors.New("limes: RevocationCacheConfig.Capabilities required")
	}
	// An interface holding a nil pointer is not nil: unchecked, it would read
	// as an upstream here and panic at the first Biscuit presented.
	if cfg.Biscuits != nil && isNilValue(cfg.Biscuits) {
		return nil, errors.New("limes: RevocationCacheConfig.Biscuits holds a nil value")
	}
	c, err := newCacheConfig(cfg.TTL, cfg.MaxEntries, cfg.Now)
	if err != nil {
		return nil, err
	}
	synced := cfg.SyncedTTL
	switch {
	case synced == 0:
		synced = c.TTL
	case synced < c.TTL:
		return nil, fmt.Errorf("limes: cache SyncedTTL %s is shorter than its TTL %s", synced, c.TTL)
	case synced > MaxRevocationCacheSyncedTTL:
		return nil, fmt.Errorf("limes: cache SyncedTTL %s is longer than %s", synced, MaxRevocationCacheSyncedTTL)
	}
	lineage, _ := cfg.Capabilities.(RevocationLineageLookup)
	if cfg.RequireLineage && lineage == nil {
		return nil, fmt.Errorf("%w: %T", ErrNoRevocationLineage, cfg.Capabilities)
	}
	return &RevocationCache{
		capabilities: cfg.Capabilities,
		lineage:      lineage,
		biscuits:     cfg.Biscuits,
		caps:         ttlcache.New[uuid.UUID](c),
		copies:       ttlcache.New[string](c),
		ttl:          c.TTL,
		syncedTTL:    synced,
	}, nil
}

// isNilValue reports whether v is nil, or an interface holding a nil
// pointer, map, slice, function or channel — a typed nil, which compares
// unequal to nil and panics at its first call.
func isNilValue(v any) bool {
	if v == nil {
		return true
	}
	switch r := reflect.ValueOf(v); r.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan, reflect.Interface:
		return r.IsNil()
	default:
		return false
	}
}

// newCacheConfig resolves a configuration's zero values to the defaults and
// refuses the negative ones.
func newCacheConfig(ttl time.Duration, maxEntries int, now func() time.Time) (ttlcache.Config, error) {
	if ttl < 0 {
		return ttlcache.Config{}, fmt.Errorf("limes: cache TTL %s is negative", ttl)
	}
	if maxEntries < 0 {
		return ttlcache.Config{}, fmt.Errorf("limes: cache bound %d is negative", maxEntries)
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
	return ttlcache.Config{TTL: ttl, MaxEntries: maxEntries, Now: now}, nil
}

// IsRevoked serves a fresh cached answer, or asks upstream once on behalf of
// every concurrent caller asking about the same id. An upstream error is
// returned to all of them and is never cached.
func (c *RevocationCache) IsRevoked(ctx context.Context, id uuid.UUID) (bool, error) {
	if c.lineage == nil {
		revoked, _, err := c.caps.Get(ctx, id, func(ctx context.Context) (bool, error) { return c.capabilities.IsRevoked(ctx, id) })
		return revoked, err
	}
	revoked, _, err := c.caps.GetTagged(ctx, id, func(ctx context.Context) (bool, []uuid.UUID, error) {
		a, err := c.lineage.RevocationLineage(ctx, id)
		return a.Revoked, a.Ancestors, err
	})
	// Not on record: not revoked, as IsRevoked answers it, and nothing
	// cached, an error being kept by no cache.
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	return revoked, err
}

// Clear drops every entry. A consumer that learns of a revocation from
// elsewhere — a database notification, a message bus — calls it so the next
// check goes upstream at once instead of waiting out the TTL. It drops
// everything, not only the revoked id, because an answer cached for a
// descendant is also an answer about the revoked ancestor.
func (c *RevocationCache) Clear() {
	c.clears.Add(1)
	c.caps.Clear()
	c.copies.Clear()
}

// ClearRevoked drops what a revocation of ids outdates: each one's cached
// "not revoked" answer and that of every descendant the cache holds one
// for, which it knows by the ancestors its upstream named. Every other
// answer stays, Biscuit copies' among them — revoking a capability revokes
// no copy's own id — so a consumer that revokes often keeps the answers it
// did not touch. A lookup in flight when it runs caches nothing, as with
// Clear. Over an upstream that is no RevocationLineageLookup it is Clear:
// the cache cannot tell a descendant from any other id. A Biscuit copy's
// revocation still takes Clear. Named no id, it drops nothing.
func (c *RevocationCache) ClearRevoked(ids ...uuid.UUID) {
	if len(ids) == 0 {
		return
	}
	if c.lineage == nil {
		// Counted after the Clear it is a part of: Stats reads the parts
		// before Clears, so no part ever reads more than the whole.
		c.Clear()
		c.clearsWithoutLineage.Add(1)
		return
	}
	c.narrowClears.Add(1)
	c.caps.Forget(ids)
}

// RevocationSignalTarget is a cache a revocation signal keeps in step
// through a RevocationSignal of its own: pgstore.RevocationWatcher takes
// one from each such cache it serves. RevocationCache is one.
type RevocationSignalTarget interface {
	NewSignal() *RevocationSignal
}

var _ RevocationSignalTarget = (*RevocationCache)(nil)

// RevocationSignal is one revocation signal's hold on a RevocationCache —
// a watcher's: it says through it when it listens and what it hears, and
// closes it when it stops for good. The cache serves SyncedTTL while at
// least one signal is open and every open one listens, since a revocation
// the one not listening would have carried goes unheard. A signal opened
// later is not waited for: start the watchers that feed one cache
// together. After Close, every call does nothing.
type RevocationSignal struct {
	c *RevocationCache
	// listening and closed are under c.syncMu.
	listening, closed bool
}

// NewSignal opens a revocation signal on the cache. Not listening yet, it
// keeps the cache on TTL until it listens or closes.
func (c *RevocationCache) NewSignal() *RevocationSignal {
	c.syncMu.Lock()
	defer c.syncMu.Unlock()
	c.open++
	c.setSyncedLocked()
	return &RevocationSignal{c: c}
}

// Listening says the signal reaches the cache from now on, at each
// connection a watcher opens: it clears the cache — a revocation made while
// nothing listened sent a signal nobody heard — and counts toward synced.
// Again while listening, it is a reconnect: it clears, and counts once.
func (s *RevocationSignal) Listening() {
	c := s.c
	c.syncMu.Lock()
	defer c.syncMu.Unlock()
	if s.closed {
		return
	}
	c.Clear()
	c.syncs.Add(1)
	if !s.listening {
		s.listening = true
		c.listening++
	}
	c.setSyncedLocked()
}

// NotListening says the signal stopped: a revocation may now go unheard, so
// every answer, those cached already among them, is served for TTL alone
// until it listens again. Not listening, it does nothing.
func (s *RevocationSignal) NotListening() {
	c := s.c
	c.syncMu.Lock()
	defer c.syncMu.Unlock()
	if s.closed || !s.listening {
		return
	}
	c.syncsLost.Add(1)
	at := c.caps.Now()
	c.lastSyncLoss.Store(&at)
	s.listening = false
	c.listening--
	c.setSyncedLocked()
}

// Revoked is the signal's word that ids were revoked: ClearRevoked of
// them. Named no id, it does nothing; a revocation it cannot name is
// RevokedAll.
func (s *RevocationSignal) Revoked(ids ...uuid.UUID) {
	if len(ids) == 0 || s.isClosed() {
		return
	}
	// Counted after the clear it caused: Stats never counts a signal whose
	// clear has not happened yet.
	s.c.ClearRevoked(ids...)
	s.c.signals.Add(1)
}

// RevokedAll is the signal's word of a revocation it cannot name — a
// Biscuit copy's, or too many ids to name: Clear.
func (s *RevocationSignal) RevokedAll() {
	if s.isClosed() {
		return
	}
	c := s.c
	// Counted after the Clear it caused and is a part of, as in
	// ClearRevoked: the signal, then the part, which Stats reads first.
	c.Clear()
	c.signals.Add(1)
	c.signalsWhole.Add(1)
}

// Close ends the signal: it no longer counts toward synced, and the cache
// is synced again only while another open signal listens. Closing the one
// signal that kept the cache on TTL — lost, or never listening — clears
// it before SyncedTTL holds again, as Listening does: what was cached while
// it was down may miss a revocation it would have carried. Once is enough;
// again, it does nothing.
func (s *RevocationSignal) Close() {
	c := s.c
	c.syncMu.Lock()
	defer c.syncMu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	if s.listening {
		s.listening = false
		c.listening--
	}
	c.open--
	if !c.synced.Load() && c.open > 0 && c.listening == c.open {
		c.Clear()
		c.syncs.Add(1)
	}
	c.setSyncedLocked()
}

// isClosed reports whether Close ran.
func (s *RevocationSignal) isClosed() bool {
	s.c.syncMu.Lock()
	defer s.c.syncMu.Unlock()
	return s.closed
}

// setSyncedLocked makes the cache synced, and SyncedTTL the bound, while
// a signal is open and every open one listens, and TTL otherwise; syncMu
// is held.
func (c *RevocationCache) setSyncedLocked() {
	synced := c.open > 0 && c.listening == c.open
	c.synced.Store(synced)
	if synced {
		c.setTTL(c.syncedTTL)
		return
	}
	c.setTTL(c.ttl)
}

// setTTL is the TTL each kind of answer is served for.
func (c *RevocationCache) setTTL(ttl time.Duration) {
	c.caps.SetTTL(ttl)
	c.copies.SetTTL(ttl)
}

// Sweep drops every expired entry, of either kind, and returns how many it
// dropped. Optional housekeeping — lookups skip stale entries, and every new
// answer drops the expired ones before it.
func (c *RevocationCache) Sweep() int { return c.caps.Sweep() + c.copies.Sweep() }

// RevocationCacheStats is what a RevocationCache has counted since it was
// made, and what it holds now: of each kind of answer, and its clears. The
// counts only grow, so a metrics exporter reads them as counters
// (otelmetric.InstrumentRevocationCache).
type RevocationCacheStats struct {
	// Capabilities counts the IsRevoked questions.
	Capabilities RevocationCacheCounts
	// Biscuits counts the IsBiscuitRevoked questions.
	Biscuits RevocationCacheCounts
	// Clears counts the calls to Clear, and NarrowClears those to
	// ClearRevoked that dropped only what a revocation outdates; one over
	// an upstream that names no ancestors is a Clear.
	Clears, NarrowClears uint64
	// ClearsWithoutLineage counts the calls to ClearRevoked that cleared
	// whole, as the upstream names no lineage: a part of Clears, apart from
	// a Clear asked for. Growing, narrow invalidation is off (Lineage).
	ClearsWithoutLineage uint64
	// Synced reports that revocation signals vouch for the cache now, and
	// SyncedTTL bounds its answers; false, TTL does. Syncs counts the
	// signals' Listening, and the Close that left every open signal
	// listening, each a Clear, among Clears: the watchers' connections,
	// their first and every reconnect.
	Synced bool
	Syncs  uint64
	// SyncsLost counts the signals' NotListening, and LastSyncLoss is the
	// time of the last: zero, none yet. A count growing as fast as Syncs
	// says the watcher keeps losing its connection.
	SyncsLost    uint64
	LastSyncLoss time.Time
	// Signals counts the notifications revocation signals passed on
	// (Revoked, RevokedAll), and SignalsWhole those that named no id and
	// cleared whole (RevokedAll), a part of Clears. Clears less Syncs, SignalsWhole and
	// ClearsWithoutLineage is the Clears the consumer asked for itself.
	Signals, SignalsWhole uint64
	// Lineage reports that the cache's upstream names each answer's
	// ancestors, so ClearRevoked drops only what a revocation outdates;
	// false, every ClearRevoked is a Clear.
	Lineage bool
	_       noCompare
}

// RevocationCacheCounts is one kind of answer's share of
// RevocationCacheStats.
type RevocationCacheCounts struct {
	// Hits counts the questions answered from the cache, Misses the ones
	// that waited on an upstream lookup: each question is one or the
	// other. A Biscuit question is a hit only when every id of the token
	// has a fresh answer.
	Hits, Misses uint64
	// Lookups counts the upstream lookups run for the misses: fewer than
	// Misses when concurrent misses share one.
	Lookups uint64
	// Evictions counts the live answers dropped to stay within
	// RevocationCacheConfig.MaxEntries: a count that keeps growing says
	// the bound is too small for the ids in use.
	Evictions uint64
	// Entries is how many answers the cache holds, fresh or not yet
	// swept.
	Entries int
	_       noCompare
}

// Stats reads the cache's counts. Each kind is read shard by shard, without
// stopping the cache, so the counts of a cache in use are each a moment
// apart.
func (c *RevocationCache) Stats() RevocationCacheStats {
	// The parts of Clears first, then Clears: each part is counted after
	// its Clear, so none reads more than the whole read after it; Signals
	// after SignalsWhole, which RevokedAll counts after it, for the same
	// reason.
	st := RevocationCacheStats{
		ClearsWithoutLineage: c.clearsWithoutLineage.Load(),
		Syncs:                c.syncs.Load(),
		SignalsWhole:         c.signalsWhole.Load(),
	}
	st.Clears = c.clears.Load()
	st.Capabilities = countsOf(c.caps.Stats())
	st.Biscuits = countsOf(c.copies.Stats())
	st.NarrowClears = c.narrowClears.Load()
	st.Synced = c.synced.Load()
	st.SyncsLost = c.syncsLost.Load()
	if at := c.lastSyncLoss.Load(); at != nil {
		st.LastSyncLoss = *at
	}
	st.Signals = c.signals.Load()
	st.Lineage = c.lineage != nil
	return st
}

func countsOf(st ttlcache.Stats) RevocationCacheCounts {
	return RevocationCacheCounts{Hits: st.Hits, Misses: st.Misses, Lookups: st.Lookups, Evictions: st.Evictions, Entries: st.Entries}
}
