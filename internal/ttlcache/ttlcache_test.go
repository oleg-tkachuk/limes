package ttlcache

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// testTTL is how long the test caches serve an answer.
const testTTL = time.Minute

// clock is a time that moves only when told to.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func newCache(maxEntries int) (*Cache[string], *clock) {
	clk := &clock{now: time.Unix(0, 0)}
	return New[string](Config{TTL: testTTL, MaxEntries: maxEntries, Now: clk.Now}), clk
}

// counted is a lookup answering revoked, counting its calls.
func counted(revoked bool, calls *atomic.Int32) func(context.Context) (bool, error) {
	return func(context.Context) (bool, error) {
		calls.Add(1)
		return revoked, nil
	}
}

func TestGetServesAnAnswerForItsTTL(t *testing.T) {
	c, clk := newCache(10)
	var calls atomic.Int32
	for range 3 {
		if revoked, _, err := c.Get(t.Context(), "a", counted(true, &calls)); err != nil || !revoked {
			t.Fatalf("Get = %v, %v; want revoked", revoked, err)
		}
	}
	if calls.Load() != 1 {
		t.Errorf("%d lookups inside the TTL, want 1", calls.Load())
	}
	clk.advance(testTTL)
	if _, _, err := c.Get(t.Context(), "a", counted(true, &calls)); err != nil || calls.Load() != 2 {
		t.Errorf("after the TTL: %d lookups, %v; want a second lookup", calls.Load(), err)
	}
}

func TestGetCachesNoError(t *testing.T) {
	c, _ := newCache(10)
	errDown := errors.New("down")
	if _, _, err := c.Get(t.Context(), "a", func(context.Context) (bool, error) { return false, errDown }); !errors.Is(err, errDown) {
		t.Fatalf("Get = %v, want the lookup's error", err)
	}
	if c.Len() != 0 {
		t.Errorf("an error left %d entries, want none", c.Len())
	}
}

func TestConcurrentMissesShareOneLookup(t *testing.T) {
	c, _ := newCache(10)
	var calls atomic.Int32
	entered, release := make(chan struct{}), make(chan struct{})
	lookup := func(context.Context) (bool, error) {
		if calls.Add(1) == 1 {
			close(entered)
		}
		<-release
		return false, nil
	}
	const waiters = 7
	var wg sync.WaitGroup
	wg.Go(func() { _, _, _ = c.Get(t.Context(), "a", lookup) })
	// The first caller is inside its lookup, so the others find it in flight.
	<-entered
	for range waiters {
		wg.Go(func() { _, _, _ = c.Get(t.Context(), "a", lookup) })
	}
	close(release)
	wg.Wait()
	if calls.Load() != 1 {
		t.Errorf("%d lookups for one key missed together, want 1", calls.Load())
	}
}

func TestMaxEntriesDropsTheOldest(t *testing.T) {
	const bound = 2
	c, _ := newCache(bound)
	var calls atomic.Int32
	for _, k := range []string{"a", "b", "c"} {
		if _, _, err := c.Get(t.Context(), k, counted(false, &calls)); err != nil {
			t.Fatal(err)
		}
	}
	if c.Len() != bound || c.Queued() != bound {
		t.Fatalf("%d entries, %d places; want %d of each", c.Len(), c.Queued(), bound)
	}
	_, _, _ = c.Get(t.Context(), "a", counted(false, &calls))
	if calls.Load() != 4 {
		t.Errorf("the oldest key was served from the cache after it was full")
	}
}

func TestClearDuringALookupCachesNothing(t *testing.T) {
	c, _ := newCache(10)
	lookup := func(context.Context) (bool, error) {
		c.Clear()
		return false, nil
	}
	if _, _, err := c.Get(t.Context(), "a", lookup); err != nil {
		t.Fatal(err)
	}
	if c.Len() != 0 {
		t.Errorf("an answer read before a clear was cached")
	}
}

func TestCachedAndSeedLive(t *testing.T) {
	c, clk := newCache(10)
	var calls atomic.Int32
	if _, _, err := c.Get(t.Context(), "set", counted(false, &calls)); err != nil {
		t.Fatal(err)
	}
	revoked, missing, epoch := c.Cached([]string{"a", "b"})
	if revoked || len(missing) != 2 {
		t.Fatalf("Cached = %v, %v; want none revoked, both missing", revoked, missing)
	}
	c.SeedLive(missing, epoch, clk.Now().Add(testTTL))
	if _, missing, _ := c.Cached([]string{"a", "b"}); len(missing) != 0 {
		t.Errorf("seeded keys still missing: %v", missing)
	}
	// A seed read before a clear is dropped.
	_, missing, epoch = c.Cached([]string{"c"})
	c.Clear()
	c.SeedLive(missing, epoch, clk.Now().Add(testTTL))
	if c.Len() != 0 {
		t.Errorf("a seed read before a clear was cached")
	}
}

func TestCachedReportsARevokedKey(t *testing.T) {
	c, _ := newCache(10)
	var calls atomic.Int32
	if _, _, err := c.Get(t.Context(), "a", counted(true, &calls)); err != nil {
		t.Fatal(err)
	}
	if revoked, _, _ := c.Cached([]string{"b", "a"}); !revoked {
		t.Error("Cached missed a cached revocation")
	}
}

func TestSweepDropsTheExpired(t *testing.T) {
	c, clk := newCache(10)
	var calls atomic.Int32
	_, _, _ = c.Get(t.Context(), "a", counted(false, &calls))
	clk.advance(testTTL / 2)
	_, _, _ = c.Get(t.Context(), "b", counted(false, &calls))
	clk.advance(testTTL / 2)
	if n := c.Sweep(); n != 1 || c.Len() != 1 {
		t.Errorf("Sweep = %d, %d left; want 1 dropped, 1 left", n, c.Len())
	}
}

func TestAPanickingLookupReleasesItsKey(t *testing.T) {
	c, _ := newCache(10)
	func() {
		defer func() { _ = recover() }()
		_, _, _ = c.Get(t.Context(), "a", func(context.Context) (bool, error) { panic("lookup") })
	}()
	var calls atomic.Int32
	if _, _, err := c.Get(t.Context(), "a", counted(false, &calls)); err != nil || calls.Load() != 1 {
		t.Errorf("after a panic: %d lookups, %v; want the key asked again", calls.Load(), err)
	}
}
