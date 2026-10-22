package ttlcache

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
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

// shardedEntries is a bound big enough to give every shard its fill.
const shardedEntries = maxShards * minShardEntries

// keysInShards are two keys of c that land in different shards.
func keysInShards(t *testing.T, c *Cache[string]) (string, string) {
	t.Helper()
	first := "k0"
	for i := 1; i < 1000; i++ {
		k := "k" + strconv.Itoa(i)
		if c.shardOf(k) != c.shardOf(first) {
			return first, k
		}
	}
	t.Fatal("no two keys in different shards")
	return "", ""
}

func TestShardsShareOutTheBound(t *testing.T) {
	for _, c := range []struct{ maxEntries, shards int }{
		{1, 1}, {4, 1}, {minShardEntries*2 - 1, 1}, {minShardEntries * 2, 2}, {shardedEntries, maxShards}, {100_000, maxShards},
	} {
		cache := New[string](Config{TTL: testTTL, MaxEntries: c.maxEntries, Now: time.Now})
		total := 0
		for _, s := range cache.shards {
			total += s.maxEntries
		}
		if len(cache.shards) != c.shards || total != c.maxEntries {
			t.Errorf("MaxEntries %d: %d shards sharing %d, want %d sharing all of it", c.maxEntries, len(cache.shards), total, c.shards)
		}
	}
}

// A caller asking about a key of one shard does not wait on another shard's
// lock: what the shards are for.
func TestAShardDoesNotWaitOnAnother(t *testing.T) {
	c, _ := newCache(shardedEntries)
	busy, free := keysInShards(t, c)
	var calls atomic.Int32
	held := c.shardOf(busy)
	held.mu.Lock()
	done := make(chan error, 1)
	go func() {
		_, _, err := c.Get(t.Context(), free, counted(false, &calls))
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		held.mu.Unlock()
		t.Fatal("a key of another shard waited on the held shard's lock")
	}
	held.mu.Unlock()
}

// One Clear reaches every shard: an answer read before it is cached in none,
// and a set read before it seeds none.
func TestClearReachesEveryShard(t *testing.T) {
	c, clk := newCache(shardedEntries)
	a, b := keysInShards(t, c)
	var calls atomic.Int32
	for _, k := range []string{a, b} {
		_, _, _ = c.Get(t.Context(), k, counted(false, &calls))
	}
	c.Clear()
	if c.Len() != 0 {
		t.Fatalf("%d entries left after Clear", c.Len())
	}
	_, missing, epoch := c.Cached([]string{a, b})
	c.Clear()
	c.SeedLive(missing, epoch, clk.Now().Add(testTTL))
	if c.Len() != 0 {
		t.Errorf("a set read before a clear was seeded in %d shards' entries", c.Len())
	}
	// A lookup on one shard that a clear interrupts caches nothing there,
	// however far from the shard the clear began.
	lookup := func(context.Context) (bool, error) {
		c.Clear()
		return false, nil
	}
	for _, k := range []string{a, b} {
		if _, _, err := c.Get(t.Context(), k, lookup); err != nil {
			t.Fatal(err)
		}
	}
	if c.Len() != 0 {
		t.Errorf("an answer read before a clear was cached")
	}
}

// However the keys fall into shards, the cache never holds more than its
// bound.
func TestShardedCacheKeepsItsBound(t *testing.T) {
	c, _ := newCache(shardedEntries)
	var calls atomic.Int32
	for i := range 3 * shardedEntries {
		_, _, _ = c.Get(t.Context(), "k"+strconv.Itoa(i), counted(false, &calls))
	}
	if n := c.Len(); n > shardedEntries || n < shardedEntries/2 {
		t.Errorf("%d entries, want at most %d and most of it used", n, shardedEntries)
	}
}

// Every question is one hit or one miss, and the misses that find a lookup
// in flight run none of their own.
func TestStatsCountHitsMissesAndLookups(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, _ := newCache(10)
		var calls atomic.Int32
		for range 3 {
			_, _, _ = c.Get(t.Context(), "a", counted(false, &calls))
		}
		release := make(chan struct{})
		blocked := func(context.Context) (bool, error) {
			<-release
			return false, nil
		}
		const waiters = 4
		var wg sync.WaitGroup
		wg.Go(func() { _, _, _ = c.Get(t.Context(), "b", blocked) })
		synctest.Wait()
		for range waiters {
			wg.Go(func() { _, _, _ = c.Get(t.Context(), "b", counted(false, &calls)) })
		}
		synctest.Wait()
		close(release)
		wg.Wait()
		want := Stats{Hits: 2, Misses: 2 + waiters, Lookups: 2, Entries: 2}
		if got := c.Stats(); got != want || calls.Load() != 1 {
			t.Errorf("Stats = %+v, want %+v (%d counted lookups, want 1)", got, want, calls.Load())
		}
	})
}

// A waiter whose leader's own context ended asks again, and is still the one
// question: one miss, though it runs a lookup of its own.
func TestAWaiterThatAsksAgainIsOneMiss(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, _ := newCache(10)
		release := make(chan struct{})
		var calls atomic.Int32
		lookup := func(context.Context) (bool, error) {
			if calls.Add(1) == 1 {
				<-release
				return false, context.Canceled
			}
			return false, nil
		}
		var wg sync.WaitGroup
		wg.Go(func() { _, _, _ = c.Get(t.Context(), "a", lookup) })
		synctest.Wait()
		wg.Go(func() {
			if _, _, err := c.Get(t.Context(), "a", lookup); err != nil {
				t.Errorf("the waiter asking again: %v", err)
			}
		})
		synctest.Wait()
		close(release)
		wg.Wait()
		if got := c.Stats(); got.Misses != 2 || got.Lookups != 2 || got.Hits != 0 {
			t.Errorf("Stats = %+v, want 2 misses and 2 lookups", got)
		}
	})
}

// Cached counts a hit for a question it answers in full, and nothing for one
// it leaves keys of: the Get of those counts it.
func TestCachedCountsAHitOnlyWhenAnswered(t *testing.T) {
	c, clk := newCache(10)
	_, _, epoch := c.Cached([]string{"a", "b"})
	c.SeedLive([]string{"a", "b"}, epoch, clk.Now().Add(testTTL))
	if _, missing, _ := c.Cached([]string{"a", "b"}); len(missing) != 0 {
		t.Fatalf("missing %v after seeding", missing)
	}
	_, _, _ = c.Cached([]string{"a", "c"})
	if got := c.Stats(); got.Hits != 1 || got.Misses != 0 {
		t.Errorf("Stats = %+v, want 1 hit and no miss", got)
	}
}

// An eviction is a live entry dropped to stay within the bound; an expired
// one swept is none.
func TestEvictionsCountOnlyLiveDrops(t *testing.T) {
	c, clk := newCache(1)
	var calls atomic.Int32
	_, _, _ = c.Get(t.Context(), "a", counted(false, &calls))
	_, _, _ = c.Get(t.Context(), "b", counted(false, &calls))
	clk.advance(testTTL)
	_, _, _ = c.Get(t.Context(), "c", counted(false, &calls))
	if got := c.Stats(); got.Evictions != 1 || got.Entries != 1 {
		t.Errorf("Stats = %+v, want 1 eviction and 1 entry", got)
	}
}

// detachWait bounds how long a caller after Forget may wait on a lookup.
const detachWait = time.Second

// tagged is a lookup answering revoked with tags, counting its calls.
func tagged(revoked bool, calls *atomic.Int32, tags ...string) func(context.Context) (bool, []string, error) {
	return func(context.Context) (bool, []string, error) {
		calls.Add(1)
		return revoked, tags, nil
	}
}

// Forget drops a key's own "not revoked" answer and every one tagged with
// it, keeps a "revoked" answer and the others, and counts no eviction.
func TestForgetDropsTheAnswersAKeyOutdates(t *testing.T) {
	c, _ := newCache(10)
	var calls atomic.Int32
	ctx := t.Context()
	for key, tags := range map[string][]string{"parent": nil, "child": {"parent"}, "grandchild": {"child", "parent"}, "other": {"elsewhere"}} {
		if _, _, err := c.GetTagged(ctx, key, tagged(false, &calls, tags...)); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := c.GetTagged(ctx, "revoked", tagged(true, &calls, "parent")); err != nil {
		t.Fatal(err)
	}
	if dropped := c.Forget([]string{"parent"}); dropped != 3 {
		t.Errorf("Forget dropped %d answers, want 3", dropped)
	}
	before := calls.Load()
	for key, lookups := range map[string]int32{"other": 0, "revoked": 0, "parent": 1, "child": 1, "grandchild": 1} {
		n := calls.Load()
		if _, _, err := c.GetTagged(ctx, key, tagged(false, &calls)); err != nil {
			t.Fatal(err)
		}
		if got := calls.Load() - n; got != lookups {
			t.Errorf("%s after Forget: %d lookups, want %d", key, got, lookups)
		}
	}
	if calls.Load()-before != 3 {
		t.Errorf("%d lookups after Forget, want 3", calls.Load()-before)
	}
	if st := c.Stats(); st.Evictions != 0 {
		t.Errorf("Forget counted %d evictions", st.Evictions)
	}
}

// A lookup in flight when Forget runs may have read what the revocation
// changed, about a key it does not name: its answer is served to whoever
// asked, cached for nobody, and shared with no caller who asks after.
func TestForgetDetachesALookupInFlight(t *testing.T) {
	c, _ := newCache(10)
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan bool)
	go func() {
		revoked, _, _ := c.GetTagged(t.Context(), "child", func(context.Context) (bool, []string, error) {
			close(entered)
			<-release
			return false, []string{"parent"}, nil
		})
		done <- revoked
	}()
	<-entered
	c.Forget([]string{"unrelated"})
	var calls atomic.Int32
	// Bounded: joined to the detached lookup, it would wait on it for good.
	ctx, cancel := context.WithTimeout(t.Context(), detachWait)
	defer cancel()
	if _, _, err := c.GetTagged(ctx, "child", tagged(true, &calls)); err != nil || calls.Load() != 1 {
		t.Errorf("a caller after Forget: %d lookups, %v; want one of its own", calls.Load(), err)
	}
	close(release)
	if <-done {
		t.Error("the detached lookup's caller got another's answer")
	}
	if revoked, _, _ := c.GetTagged(t.Context(), "child", tagged(false, &calls)); !revoked {
		t.Error("the detached lookup's answer replaced the one asked after Forget")
	}
}

// After Forget a key is cached again while its old place in the expiry
// order is still queued: that place, once it expires, leaves the new entry.
func TestARecachedKeyOutlivesItsOldPlace(t *testing.T) {
	c, clk := newCache(10)
	var calls atomic.Int32
	if _, _, err := c.GetTagged(t.Context(), "a", tagged(false, &calls)); err != nil {
		t.Fatal(err)
	}
	clk.advance(testTTL / 2)
	c.Forget([]string{"a"})
	if _, _, err := c.GetTagged(t.Context(), "a", tagged(false, &calls)); err != nil {
		t.Fatal(err)
	}
	clk.advance(testTTL / 2)
	c.Sweep()
	if _, _, err := c.GetTagged(t.Context(), "a", tagged(false, &calls)); err != nil || calls.Load() != 2 {
		t.Errorf("after the old place expired: %d lookups, %v; want the second answer served", calls.Load(), err)
	}
}
