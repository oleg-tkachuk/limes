package limes

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/uuid"
)

// ─── revocation cache ──────────────────────────────────────────────────────

type slowLookup struct {
	calls   atomic.Int32
	release chan struct{}
}

func (s *slowLookup) IsRevoked(context.Context, uuid.UUID) (bool, error) {
	s.calls.Add(1)
	<-s.release
	return true, nil
}

func TestRevocationCacheCoalescesConcurrentMisses(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		up := &slowLookup{release: make(chan struct{})}
		c := NewCachedRevocationChecker(up, time.Minute)
		id := uuid.New()

		var wg sync.WaitGroup
		for range 20 {
			wg.Go(func() {
				if r, err := c.IsRevoked(context.Background(), id); err != nil || !r {
					t.Errorf("IsRevoked = %v, %v", r, err)
				}
			})
		}
		// Every caller has joined the one lookup once all of them are blocked.
		synctest.Wait()
		close(up.release)
		wg.Wait()
		if n := up.calls.Load(); n != 1 {
			t.Errorf("upstream calls = %d, want 1", n)
		}
	})
}

// ctxLookup blocks until released and then reports its caller's context
// error, the way a database call does when its request is cancelled.
type ctxLookup struct {
	calls   atomic.Int32
	release chan struct{}
}

func (l *ctxLookup) IsRevoked(ctx context.Context, _ uuid.UUID) (bool, error) {
	if l.calls.Add(1) == 1 {
		<-l.release
		return false, ctx.Err()
	}
	return true, nil
}

// One client hanging up must not fail everyone else verifying the same
// capability at that moment.
func TestRevocationCacheLeaderCancellationDoesNotFailWaiters(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		up := &ctxLookup{release: make(chan struct{})}
		c := NewCachedRevocationChecker(up, time.Minute)
		id := uuid.New()

		leaderCtx, cancel := context.WithCancel(context.Background())
		leaderDone := make(chan error, 1)
		go func() {
			_, err := c.IsRevoked(leaderCtx, id)
			leaderDone <- err
		}()
		// The leader is in the lookup once it blocks there.
		synctest.Wait()

		waiterDone := make(chan struct{})
		var revoked bool
		var waiterErr error
		go func() {
			revoked, waiterErr = c.IsRevoked(context.Background(), id)
			close(waiterDone)
		}()
		// The waiter has joined once it blocks.
		synctest.Wait()
		cancel()
		close(up.release)

		if err := <-leaderDone; !errors.Is(err, context.Canceled) {
			t.Errorf("leader err = %v, want context.Canceled", err)
		}
		<-waiterDone
		if waiterErr != nil || !revoked {
			t.Fatalf("waiter = %v, %v; want its own upstream answer (true, nil)", revoked, waiterErr)
		}
	})
}

func TestRevocationCacheIsBoundedAndHonoursClock(t *testing.T) {
	store := newMemStore()
	now := time.Unix(1_800_000_000, 0)
	c := NewCachedRevocationChecker(store, time.Second,
		WithMaxEntries(10), withCacheClock(func() time.Time { return now }))
	ctx := context.Background()

	for range 100 {
		_, _ = c.IsRevoked(ctx, uuid.New())
	}
	if n := len(c.cache.entries); n > 10 {
		t.Errorf("cache holds %d entries, want ≤ 10", n)
	}

	id := uuid.New()
	_, _ = c.IsRevoked(ctx, id)
	store.revoked[id] = true
	if r, _ := c.IsRevoked(ctx, id); r {
		t.Error("fresh cached answer was not served")
	}
	now = now.Add(2 * time.Second)
	if r, _ := c.IsRevoked(ctx, id); !r {
		t.Error("expired cache entry was served instead of the upstream answer")
	}
}

func TestRevocationCacheClearForgetsEveryAnswer(t *testing.T) {
	store := newMemStore()
	c := NewCachedRevocationChecker(store, time.Hour)
	ctx := context.Background()
	ids := []uuid.UUID{uuid.New(), uuid.New()}
	for _, id := range ids {
		_, _ = c.IsRevoked(ctx, id)
		store.revoked[id] = true
	}
	c.Clear()
	for _, id := range ids {
		if r, _ := c.IsRevoked(ctx, id); !r {
			t.Errorf("%s: cached answer survived Clear", id)
		}
	}
}

// ─── JWKS ──────────────────────────────────────────────────────────────────

// twoRoundLookup blocks its first call until first is closed and answers it
// with the caller's context error; every later call blocks until second.
type twoRoundLookup struct {
	calls         atomic.Int32
	first, second chan struct{}
}

func (l *twoRoundLookup) IsRevoked(ctx context.Context, _ uuid.UUID) (bool, error) {
	if l.calls.Add(1) == 1 {
		<-l.first
		return false, ctx.Err()
	}
	<-l.second
	return true, nil
}

// When the leader's lookup ends with its own cancellation, the waiters retry
// together — one of them leads, the rest share its answer — rather than each
// going upstream on its own.
func TestRevocationCacheRetryAfterACancelledLeaderIsShared(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const waiters = 10
		up := &twoRoundLookup{first: make(chan struct{}), second: make(chan struct{})}
		c := NewCachedRevocationChecker(up, time.Minute)
		id := uuid.New()
		leaderCtx, cancel := context.WithCancel(context.Background())
		go func() { _, _ = c.IsRevoked(leaderCtx, id) }()
		synctest.Wait()

		var wg sync.WaitGroup
		for range waiters {
			wg.Go(func() {
				if r, err := c.IsRevoked(context.Background(), id); err != nil || !r {
					t.Errorf("waiter = %v, %v; want true", r, err)
				}
			})
		}
		synctest.Wait()
		cancel()
		close(up.first)
		synctest.Wait() // the waiters have retried and are waiting again
		close(up.second)
		wg.Wait()
		if n := up.calls.Load(); n != 2 {
			t.Errorf("%d upstream calls, want 2: the cancelled one and one shared retry", n)
		}
	})
}
