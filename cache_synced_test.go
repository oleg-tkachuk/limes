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
