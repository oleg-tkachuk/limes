package limes_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/oleg-tkachuk/limes"
)

// The bounds of the synced-TTL checks: a short TTL without a signal, a long
// one while a signal vouches for the cache.
const (
	unsyncedTTL = 2 * time.Second
	syncedTTL   = time.Hour
)

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
// however long SyncedTTL is: before the first Listening, and after
// NotListening, which also cuts short the answers cached while synced.
// While synced, an answer is served for SyncedTTL.
func TestSyncedTTLHoldsOnlyWhileASignalVouches(t *testing.T) {
	ctx := context.Background()
	s := newExampleStack()
	clk := &testClock{now: time.Now()}
	cache := newRevocationCache(t, limes.RevocationCacheConfig{Capabilities: s.records, TTL: unsyncedTTL, SyncedTTL: syncedTTL, Now: clk.Now})
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

	cache.Listening()
	if st := cache.Stats(); !st.Synced || st.Syncs != 1 || st.Clears != 1 {
		t.Errorf("after Listening: Stats = %+v; want synced, one sync, one clear", st)
	}
	// Synced: the answer is served past TTL, a signal being what ends it.
	second, _ := s.issue()
	if revokedAfter(second, unsyncedTTL) {
		t.Error("while synced: an answer not served past TTL")
	}

	// The signal stops: the answer cached while synced, older than TTL,
	// is stale at once, and the revocation shows.
	cache.NotListening()
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

// SyncedTTL zero is TTL; shorter than TTL is refused.
func TestSyncedTTLBounds(t *testing.T) {
	s := newExampleStack()
	if _, err := limes.NewRevocationCache(limes.RevocationCacheConfig{Capabilities: s.records, TTL: unsyncedTTL, SyncedTTL: unsyncedTTL / 2}); err == nil {
		t.Error("a SyncedTTL shorter than TTL accepted")
	}
	ctx := context.Background()
	clk := &testClock{now: time.Now()}
	cache := newRevocationCache(t, limes.RevocationCacheConfig{Capabilities: s.records, TTL: unsyncedTTL, Now: clk.Now})
	cache.Listening()
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

// Stats tells each cause of a whole clear apart: the signal's connections
// (Syncs), its notifications naming no id (SignalsWhole), ClearRevoked
// without lineage (ClearsWithoutLineage) and the consumer's own, the rest
// of Clears; and when the signal was last lost.
func TestStatsTellTheCausesOfAClear(t *testing.T) {
	ctx := context.Background()
	s := newExampleStack()
	clk := &testClock{now: time.Now()}
	cache := newRevocationCache(t, limes.RevocationCacheConfig{Capabilities: s.records, Now: clk.Now})
	c, _ := s.issue()
	cache.Listening()
	if _, err := cache.IsRevoked(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	other, _ := s.issue()
	cache.Signal(other.ID)
	if st := cache.Stats(); st.Capabilities.Entries != 1 || st.NarrowClears != 1 {
		t.Errorf("a signal naming an unrelated id: Stats = %+v; want the answer kept, one narrow clear", st)
	}
	cache.Signal()
	clk.advance(time.Second)
	cache.NotListening()
	cache.Clear()
	st := cache.Stats()
	if st.Signals != 2 || st.SignalsWhole != 1 || st.Syncs != 1 || st.SyncsLost != 1 || !st.LastSyncLoss.Equal(clk.Now()) {
		t.Errorf("Stats = %+v; want 2 signals, 1 whole, 1 sync, 1 lost at %s", st, clk.Now())
	}
	if own := st.Clears - st.Syncs - st.SignalsWhole - st.ClearsWithoutLineage; own != 1 {
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
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for _, clear := range []func(){cache.Listening, func() { cache.Signal() }, func() { cache.ClearRevoked() }} {
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
		if parts := st.Syncs + st.SignalsWhole + st.ClearsWithoutLineage; parts > st.Clears {
			t.Fatalf("Stats = %+v: its parts add up to %d, past Clears", st, parts)
		}
	}
}

// A loss at the Unix epoch is a loss: LastSyncLoss is unset only before
// the first, whatever the clock says.
func TestLastSyncLossAtTheEpoch(t *testing.T) {
	s := newExampleStack()
	epoch := time.Unix(0, 0).UTC()
	cache := newRevocationCache(t, limes.RevocationCacheConfig{Capabilities: s.records, Now: func() time.Time { return epoch }})
	cache.NotListening()
	if st := cache.Stats(); st.SyncsLost != 1 || st.LastSyncLoss.IsZero() || !st.LastSyncLoss.Equal(epoch) {
		t.Errorf("Stats after a loss at the epoch = %+v; want LastSyncLoss %s", st, epoch)
	}
}

// twoSignals is how many watchers the multi-signal check feeds a cache
// from.
const twoSignals = 2

// A cache fed by two signals is synced only while both listen: one alone,
// or one reconnecting while the other is down, leaves TTL in force, since a
// revocation the other would carry goes unheard.
func TestSyncedOnlyWhileEverySignalListens(t *testing.T) {
	s := newExampleStack()
	cache := newRevocationCache(t, limes.RevocationCacheConfig{Capabilities: s.records, TTL: unsyncedTTL, SyncedTTL: syncedTTL, Signals: twoSignals})
	synced := func() bool { return cache.Stats().Synced }
	cache.Listening() // a
	if synced() {
		t.Error("synced with one of two signals listening")
	}
	cache.Listening() // b
	if !synced() {
		t.Error("not synced with both signals listening")
	}
	cache.NotListening() // b lost
	if synced() {
		t.Error("synced after one of two signals was lost")
	}
	cache.NotListening() // a reconnects
	cache.Listening()
	if synced() {
		t.Error("synced after the other signal reconnected while the lost one is still down")
	}
	cache.NotListening()
	cache.NotListening() // one more loss than listens: no count below none
	cache.Listening()
	cache.Listening()
	if !synced() {
		t.Error("not synced once both signals listen again")
	}
	if _, err := limes.NewRevocationCache(limes.RevocationCacheConfig{Capabilities: s.records, Signals: -1}); err == nil {
		t.Error("a negative Signals accepted")
	}
}
