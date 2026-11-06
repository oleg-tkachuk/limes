package limes_test

import (
	"context"
	"sync"
	"testing"
	"time"
	"uuid"

	"github.com/oleg-tkachuk/limes"
)

// The bounds of the synced-TTL checks: a short TTL without a signal, a long
// one while a signal vouches for the cache.
const (
	unsyncedTTL = 2 * time.Second
	syncedTTL   = time.Hour
)

// testSignalLease is the lease the tests' signals listen with: longer than
// any of them moves the clock, but for those that let it lapse.
const testSignalLease = 2 * limes.MaxRevocationCacheSyncedTTL

// testClock is a time that moves only when told to.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// A revocation made while no signal reaches the cache shows within TTL,
// however long SyncedTTL is: before the signal's first Listening, and after
// NotListening, which also cuts short the answers cached while synced.
// While synced, an answer is served for SyncedTTL.
func TestSyncedTTLHoldsOnlyWhileASignalVouches(t *testing.T) {
	ctx := context.Background()
	s := newExampleStack()
	clk := &testClock{now: time.Now()}
	cache := newRevocationCache(t, limes.RevocationCacheConfig{Capabilities: s.records, TTL: unsyncedTTL, SyncedTTL: syncedTTL, Now: clk.Now})
	signal := cache.NewSignal()
	revokedAfter := func(c limes.Capability, wait time.Duration) bool {
		t.Helper()
		if revoked, err := cache.IsRevoked(ctx, c.ID); err != nil || revoked {
			t.Fatalf("before the revocation: %v, %v", revoked, err)
		}
		if _, err := s.records.Revoke(ctx, limes.RevokeRequest{CapabilityID: c.ID}); err != nil {
			t.Fatal(err)
		}
		clk.advance(wait)
		revoked, err := cache.IsRevoked(ctx, c.ID)
		if err != nil {
			t.Fatal(err)
		}
		return revoked
	}

	// No signal yet: TTL bounds the answer.
	first, _ := s.issue()
	if !revokedAfter(first, unsyncedTTL) {
		t.Error("before Listening: a revocation unseen past TTL")
	}
	if st := cache.Stats(); st.Synced {
		t.Error("Synced before Listening")
	}

	signal.Listening(testSignalLease)
	if st := cache.Stats(); !st.Synced || st.Syncs != 1 || st.Clears != 0 {
		t.Errorf("after Listening: Stats = %+v; want synced, one sync, no clear", st)
	}
	// Read after Listening: SyncedTTL covers no answer read at its instant.
	clk.advance(time.Millisecond)
	// Synced: the answer is served past TTL, a signal being what ends it.
	second, _ := s.issue()
	if revokedAfter(second, unsyncedTTL) {
		t.Error("while synced: an answer not served past TTL")
	}

	// The signal stops: the answer cached while synced, older than TTL,
	// is stale at once, and the revocation shows.
	signal.NotListening()
	if revoked, err := cache.IsRevoked(ctx, second.ID); err != nil || !revoked {
		t.Errorf("after NotListening: %v, %v; want the revocation seen", revoked, err)
	}
	if st := cache.Stats(); st.Synced {
		t.Error("Synced after NotListening")
	}
	third, _ := s.issue()
	if !revokedAfter(third, unsyncedTTL) {
		t.Error("after NotListening: a revocation unseen past TTL")
	}
}

// SyncedTTL zero is TTL; shorter than TTL, or longer than
// MaxRevocationCacheSyncedTTL, is refused; either bound itself is accepted.
func TestSyncedTTLBounds(t *testing.T) {
	s := newExampleStack()
	if _, err := limes.NewRevocationCache(limes.RevocationCacheConfig{Capabilities: s.records, TTL: unsyncedTTL, SyncedTTL: unsyncedTTL / 2}); err == nil {
		t.Error("a SyncedTTL shorter than TTL accepted")
	}
	if _, err := limes.NewRevocationCache(limes.RevocationCacheConfig{Capabilities: s.records, SyncedTTL: limes.MaxRevocationCacheSyncedTTL + time.Nanosecond}); err == nil {
		t.Error("a SyncedTTL past MaxRevocationCacheSyncedTTL accepted")
	}
	if _, err := limes.NewRevocationCache(limes.RevocationCacheConfig{Capabilities: s.records, TTL: unsyncedTTL, SyncedTTL: unsyncedTTL}); err != nil {
		t.Errorf("SyncedTTL equal to TTL: %v", err)
	}
	if _, err := limes.NewRevocationCache(limes.RevocationCacheConfig{Capabilities: s.records, SyncedTTL: limes.MaxRevocationCacheSyncedTTL}); err != nil {
		t.Errorf("SyncedTTL at MaxRevocationCacheSyncedTTL: %v", err)
	}
	ctx := context.Background()
	clk := &testClock{now: time.Now()}
	cache := newRevocationCache(t, limes.RevocationCacheConfig{Capabilities: s.records, TTL: unsyncedTTL, Now: clk.Now})
	cache.NewSignal().Listening(testSignalLease)
	c, _ := s.issue()
	if _, err := cache.IsRevoked(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.records.Revoke(ctx, limes.RevokeRequest{CapabilityID: c.ID}); err != nil {
		t.Fatal(err)
	}
	clk.advance(unsyncedTTL)
	if revoked, err := cache.IsRevoked(ctx, c.ID); err != nil || !revoked {
		t.Errorf("SyncedTTL unset, synced: %v, %v; want the revocation seen past TTL", revoked, err)
	}
}

// Stats tells each cause of a whole clear apart: the signal's
// notifications naming no id (SignalsWhole), ClearRevoked without lineage
// (ClearsWithoutLineage) and the consumer's own, the rest of Clears; and
// counts the signal's connections (Syncs), none a clear, and when it was
// last lost.
func TestStatsTellTheCausesOfAClear(t *testing.T) {
	ctx := context.Background()
	s := newExampleStack()
	clk := &testClock{now: time.Now()}
	cache := newRevocationCache(t, limes.RevocationCacheConfig{Capabilities: s.records, Now: clk.Now})
	c, _ := s.issue()
	signal := cache.NewSignal()
	signal.Listening(testSignalLease)
	if _, err := cache.IsRevoked(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	other, _ := s.issue()
	signal.Revoked(other.ID)
	if st := cache.Stats(); st.Capabilities.Entries != 1 || st.NarrowClears != 1 {
		t.Errorf("a signal naming an unrelated id: Stats = %+v; want the answer kept, one narrow clear", st)
	}
	signal.RevokedAll()
	clk.advance(time.Second)
	signal.NotListening()
	cache.Clear()
	st := cache.Stats()
	if st.Signals != 2 || st.SignalsWhole != 1 || st.Syncs != 1 || st.SyncsLost != 1 || !st.LastSyncLoss.Equal(clk.Now()) {
		t.Errorf("Stats = %+v; want 2 signals, 1 whole, 1 sync, 1 lost at %s", st, clk.Now())
	}
	if own := st.Clears - st.SignalsWhole - st.ClearsWithoutLineage; own != 1 {
		t.Errorf("Clears %d less the causes counted = %d, want the 1 asked for", st.Clears, own)
	}
	if fresh := newRevocationCache(t, limes.RevocationCacheConfig{Capabilities: s.records}); !fresh.Stats().LastSyncLoss.IsZero() {
		t.Error("LastSyncLoss set before any loss")
	}
}

// statsReads is how many times the concurrent check reads Stats.
const statsReads = 20_000

// Read while the cache clears for every cause at once, the parts of Clears
// never add up to more than Clears: each is counted after its Clear and
// read before the whole.
func TestStatsPartsNeverExceedClears(t *testing.T) {
	s := newExampleStack()
	cache := newRevocationCache(t, limes.RevocationCacheConfig{Capabilities: isRevokedOnly{s.records}})
	signal := cache.NewSignal()
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for _, clear := range []func(){func() { signal.Listening(testSignalLease) }, signal.RevokedAll, func() { cache.ClearRevoked(uuid.New()) }} {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
					clear()
				}
			}
		})
	}
	defer func() {
		close(stop)
		wg.Wait()
	}()
	for range statsReads {
		st := cache.Stats()
		if parts := st.SignalsWhole + st.ClearsWithoutLineage; parts > st.Clears {
			t.Fatalf("Stats = %+v: its parts add up to %d, past Clears", st, parts)
		}
		if st.SignalsWhole > st.Signals {
			t.Fatalf("Stats = %+v: more whole signals than signals", st)
		}
	}
}

// A loss at the Unix epoch is a loss: LastSyncLoss is unset only before
// the first, whatever the clock says.
func TestLastSyncLossAtTheEpoch(t *testing.T) {
	s := newExampleStack()
	epoch := time.Unix(0, 0).UTC()
	cache := newRevocationCache(t, limes.RevocationCacheConfig{Capabilities: s.records, Now: func() time.Time { return epoch }})
	signal := cache.NewSignal()
	signal.Listening(testSignalLease)
	signal.NotListening()
	if st := cache.Stats(); st.SyncsLost != 1 || st.LastSyncLoss.IsZero() || !st.LastSyncLoss.Equal(epoch) {
		t.Errorf("Stats after a loss at the epoch = %+v; want LastSyncLoss %s", st, epoch)
	}
}

// A cache fed by two signals is synced only while both listen: one alone,
// or one reconnecting while the other is down, leaves TTL in force, since a
// revocation the other would carry goes unheard. A reconnect counts once,
// a loss of a signal not listening counts none, and a closed signal no
// longer counts: the cache is synced while the one left listens, and on
// TTL once none is open.
func TestSyncedOnlyWhileEverySignalListens(t *testing.T) {
	s := newExampleStack()
	cache := newRevocationCache(t, limes.RevocationCacheConfig{Capabilities: s.records, TTL: unsyncedTTL, SyncedTTL: syncedTTL})
	synced := func() bool { return cache.Stats().Synced }
	if synced() {
		t.Error("synced with no signal open")
	}
	a, b := cache.NewSignal(), cache.NewSignal()
	a.Listening(testSignalLease)
	if synced() {
		t.Error("synced with one of two signals listening")
	}
	b.Listening(testSignalLease)
	if !synced() {
		t.Error("not synced with both signals listening")
	}
	b.NotListening()
	if synced() {
		t.Error("synced after one of two signals was lost")
	}
	a.NotListening()
	a.Listening(testSignalLease)
	if synced() {
		t.Error("synced after the other signal reconnected while the lost one is still down")
	}
	lost := cache.Stats().SyncsLost
	b.NotListening() // not listening: no loss to count
	if st := cache.Stats(); st.SyncsLost != lost {
		t.Errorf("a loss of a signal not listening counted: SyncsLost %d, want %d", st.SyncsLost, lost)
	}
	a.Listening(testSignalLease) // a reconnect, counted once
	b.Listening(testSignalLease)
	if !synced() {
		t.Error("not synced once both signals listen again")
	}
	b.Close()
	if !synced() {
		t.Error("not synced with the one open signal listening")
	}
	a.Close()
	if synced() {
		t.Error("synced with no signal open")
	}
	syncs := cache.Stats().Syncs
	a.Listening(testSignalLease)
	if st := cache.Stats(); st.Synced || st.Syncs != syncs {
		t.Errorf("a closed signal's Listening: Stats = %+v; want nothing changed", st)
	}
}

// A signal naming no id, a ClearRevoked naming none and a closed signal's
// word drop nothing and count nothing, with lineage or without.
func TestEmptyRevocationsDropNothing(t *testing.T) {
	ctx := context.Background()
	s := newExampleStack()
	for name, upstream := range map[string]limes.RevocationLookup{"lineage": s.records, "no lineage": isRevokedOnly{s.records}} {
		c, _ := s.issue()
		t.Run(name, func(t *testing.T) {
			cache := newRevocationCache(t, limes.RevocationCacheConfig{Capabilities: upstream})
			if _, err := cache.IsRevoked(ctx, c.ID); err != nil {
				t.Fatal(err)
			}
			signal := cache.NewSignal()
			signal.Revoked()
			cache.ClearRevoked()
			signal.Close()
			signal.Revoked(c.ID)
			signal.RevokedAll()
			st := cache.Stats()
			if st.Capabilities.Entries != 1 || st.Clears != 0 || st.NarrowClears != 0 || st.Signals != 0 {
				t.Errorf("Stats = %+v; want the answer kept and nothing counted", st)
			}
		})
	}
}

// Closing the signal that kept the cache on TTL — lost while another
// listens — makes the cache synced again for the answers read from then
// on only: an answer cached while it was down may predate a revocation it
// would have carried, so it stays on TTL, and the revocation shows within
// it.
func TestClosingALostSignalKeepsTheOlderAnswersOnTTL(t *testing.T) {
	ctx := context.Background()
	s := newExampleStack()
	clk := &testClock{now: time.Now()}
	cache := newRevocationCache(t, limes.RevocationCacheConfig{Capabilities: s.records, TTL: unsyncedTTL, SyncedTTL: syncedTTL, Now: clk.Now})
	kept, lost := cache.NewSignal(), cache.NewSignal()
	kept.Listening(testSignalLease)
	lost.Listening(testSignalLease)
	lost.NotListening()
	c, _ := s.issue()
	if revoked, err := cache.IsRevoked(ctx, c.ID); err != nil || revoked {
		t.Fatalf("before the revocation: %v, %v", revoked, err)
	}
	// Revoked where only the lost signal would have said so.
	if _, err := s.records.Revoke(ctx, limes.RevokeRequest{CapabilityID: c.ID}); err != nil {
		t.Fatal(err)
	}
	syncs := cache.Stats().Syncs
	lost.Close()
	st := cache.Stats()
	if !st.Synced || st.Syncs != syncs+1 {
		t.Errorf("after the lost signal closed: Stats = %+v; want synced, one sync more", st)
	}
	clk.advance(unsyncedTTL)
	if revoked, err := cache.IsRevoked(ctx, c.ID); err != nil || !revoked {
		t.Errorf("a TTL after the lost signal closed: %v, %v; want the revocation seen", revoked, err)
	}
}

// cachedBeforeReconnect is how many answers the reconnect check caches.
const cachedBeforeReconnect = 50

// A signal that is lost and listens again drops nothing: every answer read
// within TTL is still served, so a connection that flaps sends no verifier
// to the store at once. An answer read before the gap is served for TTL
// from when it was read, not SyncedTTL, so a revocation made in the gap
// shows within it; one read after the reconnect is served for SyncedTTL.
func TestAReconnectDropsNothingAndKeepsTheGapOnTTL(t *testing.T) {
	ctx := context.Background()
	s := newExampleStack()
	clk := &testClock{now: time.Now()}
	cache := newRevocationCache(t, limes.RevocationCacheConfig{Capabilities: s.records, TTL: unsyncedTTL, SyncedTTL: syncedTTL, Now: clk.Now})
	signal := cache.NewSignal()
	signal.Listening(testSignalLease)
	ask := func(c limes.Capability) bool {
		t.Helper()
		revoked, err := cache.IsRevoked(ctx, c.ID)
		if err != nil {
			t.Fatal(err)
		}
		return revoked
	}
	held := make([]limes.Capability, cachedBeforeReconnect)
	for i := range held {
		held[i], _ = s.issue()
		ask(held[i])
	}
	clk.advance(unsyncedTTL / 2)
	signal.NotListening()
	// Revoked while nothing listens: no signal will say so.
	if _, err := s.records.Revoke(ctx, limes.RevokeRequest{CapabilityID: held[0].ID}); err != nil {
		t.Fatal(err)
	}
	signal.Listening(testSignalLease)
	clk.advance(time.Millisecond)
	lookups := cache.Stats().Capabilities.Lookups
	for _, c := range held[1:] {
		ask(c)
	}
	if st := cache.Stats(); st.Capabilities.Lookups != lookups || st.Clears != 0 {
		t.Errorf("after the reconnect: %d lookups, %d clears; want every answer within TTL served", st.Capabilities.Lookups-lookups, st.Clears)
	}
	after, _ := s.issue()
	ask(after)
	clk.advance(unsyncedTTL / 2)
	if !ask(held[0]) {
		t.Error("an answer read before the gap served past TTL over a revocation made in it")
	}
	if _, err := s.records.Revoke(ctx, limes.RevokeRequest{CapabilityID: after.ID}); err != nil {
		t.Fatal(err)
	}
	clk.advance(unsyncedTTL)
	if ask(after) {
		t.Error("an answer read after the reconnect not served for SyncedTTL")
	}
}

// shortLease is the lease the lapse checks listen with: past TTL, short of
// SyncedTTL.
const shortLease = 10 * unsyncedTTL

// A signal that stops renewing without a word lets its lease lapse: the
// cache falls back to TTL then, the lapse is counted once, as lost when it
// lapsed, and a NotListening after counts no second loss.
func TestALapsedLeaseFallsBackToTTL(t *testing.T) {
	ctx := context.Background()
	s := newExampleStack()
	clk := &testClock{now: time.Now()}
	cache := newRevocationCache(t, limes.RevocationCacheConfig{Capabilities: s.records, TTL: unsyncedTTL, SyncedTTL: syncedTTL, Now: clk.Now})
	signal := cache.NewSignal()
	signal.Listening(shortLease)
	lapse := clk.Now().Add(shortLease)
	clk.advance(time.Millisecond)
	c, _ := s.issue()
	if revoked, err := cache.IsRevoked(ctx, c.ID); err != nil || revoked {
		t.Fatalf("before the revocation: %v, %v", revoked, err)
	}
	if _, err := s.records.Revoke(ctx, limes.RevokeRequest{CapabilityID: c.ID}); err != nil {
		t.Fatal(err)
	}
	clk.advance(shortLease)
	if revoked, err := cache.IsRevoked(ctx, c.ID); err != nil || !revoked {
		t.Errorf("past the lease: %v, %v; want the revocation seen, TTL in force", revoked, err)
	}
	st := cache.Stats()
	if st.Synced || st.SyncsLost != 1 || !st.LastSyncLoss.Equal(lapse) {
		t.Errorf("past the lease: Stats = %+v; want not synced, one loss at %s", st, lapse)
	}
	signal.NotListening()
	if st := cache.Stats(); st.SyncsLost != 1 {
		t.Errorf("a NotListening after the lapse: SyncsLost %d, want the one lapse", st.SyncsLost)
	}
}

// Renewed within its lease, a signal keeps the cache synced with no new
// sync, its answers served on; renewed after it lapsed, it counts as a
// Listening: synced again, one sync more, and the answers read before on
// TTL.
func TestRenewHoldsTheLeaseOrListensAgain(t *testing.T) {
	ctx := context.Background()
	s := newExampleStack()
	clk := &testClock{now: time.Now()}
	cache := newRevocationCache(t, limes.RevocationCacheConfig{Capabilities: s.records, TTL: unsyncedTTL, SyncedTTL: syncedTTL, Now: clk.Now})
	signal := cache.NewSignal()
	signal.Listening(shortLease)
	clk.advance(time.Millisecond)
	c, _ := s.issue()
	if _, err := cache.IsRevoked(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		clk.advance(shortLease / 2)
		signal.Renew()
	}
	lookups := cache.Stats().Capabilities.Lookups
	if _, err := cache.IsRevoked(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	if st := cache.Stats(); !st.Synced || st.Syncs != 1 || st.SyncsLost != 0 || st.Capabilities.Lookups != lookups {
		t.Errorf("renewed in time: Stats = %+v; want synced, one sync, the answer served", st)
	}
	clk.advance(shortLease)
	signal.Renew()
	if st := cache.Stats(); !st.Synced || st.Syncs != 2 || st.SyncsLost != 1 {
		t.Errorf("renewed after a lapse: Stats = %+v; want synced, two syncs, one loss", st)
	}
	if _, err := cache.IsRevoked(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	if st := cache.Stats(); st.Capabilities.Lookups != lookups+1 {
		t.Errorf("renewed after a lapse: %d lookups; want the answer read before asked again", st.Capabilities.Lookups-lookups)
	}
}

// A lease that is not positive vouches for nothing, and a signal not
// listening renews nothing.
func TestNoLeaseNoSync(t *testing.T) {
	s := newExampleStack()
	cache := newRevocationCache(t, limes.RevocationCacheConfig{Capabilities: s.records, TTL: unsyncedTTL, SyncedTTL: syncedTTL})
	signal := cache.NewSignal()
	signal.Renew()
	if cache.Stats().Synced {
		t.Error("synced by a Renew before Listening")
	}
	signal.Listening(0)
	signal.Renew()
	if st := cache.Stats(); st.Synced || st.Syncs != 1 {
		t.Errorf("a lease of zero, renewed: Stats = %+v; want not synced, the one sync", st)
	}
}
