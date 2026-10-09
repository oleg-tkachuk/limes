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

// cacheTTL is the lifetime of an entry in the tests below; they step a clock
// past it rather than sleeping.
const cacheTTL = time.Minute

// checkDeadline bounds a check that must not wait on another caller's
// lookup, so a regression fails the test instead of hanging it.
const checkDeadline = time.Second

// slowBiscuitLookup blocks until released, counting its calls.
type slowBiscuitLookup struct {
	calls   atomic.Int32
	release chan struct{}
}

func (s *slowBiscuitLookup) IsBiscuitRevoked(context.Context, [][]byte) (bool, error) {
	s.calls.Add(1)
	<-s.release
	return true, nil
}

// The Biscuit cache shares concurrent misses for one token as the
// capability cache does.
func TestBiscuitRevocationCacheCoalescesConcurrentMisses(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		up := &slowBiscuitLookup{release: make(chan struct{})}
		c := NewCachedBiscuitRevocationChecker(up, cacheTTL)
		ids := [][]byte{[]byte("a"), []byte("b")}

		var wg sync.WaitGroup
		for range 20 {
			wg.Go(func() {
				if r, err := c.IsBiscuitRevoked(context.Background(), ids); err != nil || !r {
					t.Errorf("IsBiscuitRevoked = %v, %v", r, err)
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

// Sweep drops what has expired and keeps what has not, in both caches.
func TestRevocationCachesSweep(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	clock := WithCacheClock(func() time.Time { return now })
	caps := NewCachedRevocationChecker(newMemStore(), cacheTTL, clock)
	copies := NewCachedBiscuitRevocationChecker(&countingBiscuitLookup{}, cacheTTL, clock)

	old := uuid.New()
	_, _ = caps.IsRevoked(ctx, old)
	_, _ = copies.IsBiscuitRevoked(ctx, [][]byte{[]byte("old")})
	now = now.Add(cacheTTL)
	_, _ = caps.IsRevoked(ctx, uuid.New())
	_, _ = copies.IsBiscuitRevoked(ctx, [][]byte{[]byte("new")})

	if n := caps.Sweep(); n != 1 || len(caps.cache.entries) != 1 {
		t.Errorf("capability cache Sweep = %d, %d left; want 1 dropped, 1 left", n, len(caps.cache.entries))
	}
	if n := copies.Sweep(); n != 1 || len(copies.cache.entries) != 1 {
		t.Errorf("Biscuit cache Sweep = %d, %d left; want 1 dropped, 1 left", n, len(copies.cache.entries))
	}
	if n := caps.Sweep(); n != 0 {
		t.Errorf("a second Sweep dropped %d", n)
	}
}

// Invalidate drops one entry, so the next check of it asks upstream.
func TestRevocationCacheInvalidate(t *testing.T) {
	ctx := context.Background()
	store := newMemStore()
	c := NewCachedRevocationChecker(store, cacheTTL)
	id, other := uuid.New(), uuid.New()
	_, _ = c.IsRevoked(ctx, id)
	_, _ = c.IsRevoked(ctx, other)
	store.revoked[id] = true
	if r, _ := c.IsRevoked(ctx, id); r {
		t.Fatal("a cached answer should stand until invalidated")
	}
	c.Invalidate(id)
	if r, _ := c.IsRevoked(ctx, id); !r {
		t.Error("after Invalidate the revocation must be seen")
	}
	if len(c.cache.entries) != 2 {
		t.Errorf("Invalidate of one id left %d entries, want both ids cached again", len(c.cache.entries))
	}
}

// staleThenRevoked answers its first call with a "live" read that blocks
// until released — a lookup that began before a revocation — and every
// later call with "revoked".
type staleThenRevoked struct {
	calls   atomic.Int32
	entered chan struct{}
	release chan struct{}
}

func newStaleThenRevoked() *staleThenRevoked {
	return &staleThenRevoked{entered: make(chan struct{}), release: make(chan struct{})}
}

func (s *staleThenRevoked) IsRevoked(context.Context, uuid.UUID) (bool, error) {
	if s.calls.Add(1) == 1 {
		close(s.entered)
		<-s.release
		return false, nil
	}
	return true, nil
}

// A Clear or Invalidate that lands while a lookup is in flight is not undone
// by that lookup's answer: the next check goes upstream and sees the
// revocation, and a check arriving meanwhile does not join the stale lookup.
func TestRevocationCacheDropDuringLookupIsNotUndone(t *testing.T) {
	drops := map[string]func(*CachedRevocationChecker, uuid.UUID){
		"Clear":      func(c *CachedRevocationChecker, _ uuid.UUID) { c.Clear() },
		"Invalidate": func(c *CachedRevocationChecker, id uuid.UUID) { c.Invalidate(id) },
	}
	for name, drop := range drops {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			up := newStaleThenRevoked()
			c := NewCachedRevocationChecker(up, cacheTTL)
			id := uuid.New()

			stale := make(chan bool)
			go func() {
				r, _ := c.IsRevoked(ctx, id)
				stale <- r
			}()
			<-up.entered
			drop(c, id)

			bounded, cancel := context.WithTimeout(ctx, checkDeadline)
			defer cancel()
			if r, err := c.IsRevoked(bounded, id); err != nil || !r {
				t.Errorf("check after %s, lookup still in flight = %v, %v; want revoked", name, r, err)
			}
			close(up.release)
			<-stale
			if r, err := c.IsRevoked(ctx, id); err != nil || !r {
				t.Errorf("check after the stale lookup finished = %v, %v; want revoked", r, err)
			}
		})
	}
}

// panickingLookup panics once released; a caller that recovers sees it.
type panickingLookup struct {
	calls   atomic.Int32
	entered chan struct{}
	release chan struct{}
}

func (p *panickingLookup) IsRevoked(context.Context, uuid.UUID) (bool, error) {
	if p.calls.Add(1) == 1 {
		close(p.entered)
		<-p.release
		panic("upstream fault")
	}
	return false, nil
}

// A lookup that panics releases the callers waiting on it with an error —
// never with a "live" answer nobody gave — and leaves the key free for the
// next check.
func TestRevocationCacheLookupPanicReleasesWaiters(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		up := &panickingLookup{entered: make(chan struct{}), release: make(chan struct{})}
		c := NewCachedRevocationChecker(up, cacheTTL)
		id := uuid.New()

		go func() {
			defer func() { _ = recover() }()
			_, _ = c.IsRevoked(context.Background(), id)
		}()
		<-up.entered

		type answer struct {
			revoked bool
			err     error
		}
		waiter := make(chan answer)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), checkDeadline)
			defer cancel()
			r, err := c.IsRevoked(ctx, id)
			waiter <- answer{r, err}
		}()
		// The waiter has joined once it blocks.
		synctest.Wait()
		close(up.release)

		got := <-waiter
		if got.err == nil || errors.Is(got.err, context.DeadlineExceeded) {
			t.Errorf("waiter on a panicked lookup = %v, %v; want released with an error", got.revoked, got.err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), checkDeadline)
		defer cancel()
		if _, err := c.IsRevoked(ctx, id); err != nil {
			t.Errorf("check after the panic = %v; want a fresh lookup", err)
		}
	})
}
