package ttlcache

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// testMaxStale is how long past the TTL the stale test caches may stand
// in for an upstream that fails.
const testMaxStale = 10 * time.Minute

var (
	errDown     = errors.New("down")
	errAnswered = errors.New("not on record")
)

// staleCache is a cache over testMaxStale that takes errAnswered as an
// answer, recording every stale answer it serves.
func staleCache(maxEntries int) (*Cache[string], *clock, *[]error) {
	clk := &clock{now: time.Unix(0, 0)}
	var served []error
	c := New[string](Config{
		TTL: testTTL, MaxEntries: maxEntries, Now: clk.Now,
		MaxStale: testMaxStale,
		Answers:  func(err error) bool { return errors.Is(err, errAnswered) },
		OnStale:  func(_ time.Time, cause error) { served = append(served, cause) },
	})
	return c, clk, &served
}

func failing(err error) func(context.Context) (bool, error) {
	return func(context.Context) (bool, error) { return false, err }
}

func TestAFailedLookupIsCoveredByAStaleAnswer(t *testing.T) {
	for _, revoked := range []bool{false, true} {
		c, clk, served := staleCache(10)
		var calls atomic.Int32
		if _, _, err := c.Get(t.Context(), "a", counted(revoked, &calls)); err != nil {
			t.Fatal(err)
		}
		clk.advance(testTTL + testMaxStale - time.Second)
		got, at, err := c.Get(t.Context(), "a", failing(errDown))
		if err != nil || got != revoked || !at.Equal(time.Unix(0, 0)) {
			t.Fatalf("Get = %v, %v, %v; want the stale answer %v read at 0", got, at, err, revoked)
		}
		if len(*served) != 1 || !errors.Is((*served)[0], errDown) {
			t.Errorf("OnStale saw %v, want the one failure", *served)
		}
		if st := c.Stats(); st.Stale != 1 || st.Misses != 2 || st.Lookups != 2 {
			t.Errorf("Stats = %+v, want 1 stale answer of 2 misses", st)
		}
	}
}

func TestAStaleAnswerEndsAtMaxStale(t *testing.T) {
	c, clk, served := staleCache(10)
	var calls atomic.Int32
	if _, _, err := c.Get(t.Context(), "a", counted(false, &calls)); err != nil {
		t.Fatal(err)
	}
	clk.advance(testTTL + testMaxStale)
	if _, _, err := c.Get(t.Context(), "a", failing(errDown)); !errors.Is(err, errDown) {
		t.Fatalf("Get = %v, want the failure past MaxStale", err)
	}
	if len(*served) != 0 || c.Stats().Stale != 0 {
		t.Errorf("served %v stale past MaxStale", *served)
	}
}

func TestNoStaleAnswerWithoutMaxStale(t *testing.T) {
	c, clk := newCache(10)
	var calls atomic.Int32
	if _, _, err := c.Get(t.Context(), "a", counted(false, &calls)); err != nil {
		t.Fatal(err)
	}
	clk.advance(testTTL)
	if _, _, err := c.Get(t.Context(), "a", failing(errDown)); !errors.Is(err, errDown) {
		t.Fatalf("Get = %v, want the failure", err)
	}
}

func TestAnUpstreamAnswerIsNotCoveredByAStaleOne(t *testing.T) {
	c, clk, served := staleCache(10)
	var calls atomic.Int32
	if _, _, err := c.Get(t.Context(), "a", counted(false, &calls)); err != nil {
		t.Fatal(err)
	}
	clk.advance(testTTL)
	if _, _, err := c.Get(t.Context(), "a", failing(errAnswered)); !errors.Is(err, errAnswered) {
		t.Fatalf("Get = %v, want the upstream's answer", err)
	}
	if len(*served) != 0 {
		t.Errorf("served %v stale over an answer", *served)
	}
}

func TestACancelledCallerGetsNoStaleAnswer(t *testing.T) {
	c, clk, served := staleCache(10)
	var calls atomic.Int32
	if _, _, err := c.Get(t.Context(), "a", counted(false, &calls)); err != nil {
		t.Fatal(err)
	}
	clk.advance(testTTL)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := c.Get(ctx, "a", func(ctx context.Context) (bool, error) { return false, ctx.Err() }); !errors.Is(err, context.Canceled) {
		t.Fatalf("Get = %v, want the caller's cancellation", err)
	}
	if len(*served) != 0 {
		t.Errorf("served %v stale to a caller that left", *served)
	}
}

func TestAnUpstreamTimeoutIsCoveredByAStaleAnswer(t *testing.T) {
	c, clk, served := staleCache(10)
	var calls atomic.Int32
	if _, _, err := c.Get(t.Context(), "a", counted(false, &calls)); err != nil {
		t.Fatal(err)
	}
	clk.advance(testTTL)
	if _, _, err := c.Get(t.Context(), "a", failing(context.DeadlineExceeded)); err != nil {
		t.Fatalf("Get = %v, want the stale answer over the upstream's own timeout", err)
	}
	if len(*served) != 1 {
		t.Errorf("OnStale saw %v, want the timeout", *served)
	}
}

func TestSweepKeepsAStaleAnswerUntilMaxStale(t *testing.T) {
	c, clk, _ := staleCache(10)
	var calls atomic.Int32
	if _, _, err := c.Get(t.Context(), "a", counted(false, &calls)); err != nil {
		t.Fatal(err)
	}
	clk.advance(testTTL)
	if n := c.Sweep(); n != 0 || c.Len() != 1 {
		t.Fatalf("Sweep past the TTL dropped %d, left %d; want the stale answer kept", n, c.Len())
	}
	clk.advance(testMaxStale)
	if n := c.Sweep(); n != 1 || c.Len() != 0 {
		t.Errorf("Sweep past MaxStale dropped %d, left %d; want it gone", n, c.Len())
	}
}

func TestClearDropsTheStaleAnswers(t *testing.T) {
	c, clk, _ := staleCache(10)
	var calls atomic.Int32
	if _, _, err := c.Get(t.Context(), "a", counted(false, &calls)); err != nil {
		t.Fatal(err)
	}
	clk.advance(testTTL)
	c.Clear()
	if _, _, err := c.Get(t.Context(), "a", failing(errDown)); !errors.Is(err, errDown) {
		t.Fatalf("Get after Clear = %v, want the failure", err)
	}
}

func TestForgetDropsAStaleAnswer(t *testing.T) {
	c, clk, _ := staleCache(10)
	var calls atomic.Int32
	if _, _, err := c.Get(t.Context(), "a", counted(false, &calls)); err != nil {
		t.Fatal(err)
	}
	clk.advance(testTTL)
	c.Forget([]string{"a"})
	if _, _, err := c.Get(t.Context(), "a", failing(errDown)); !errors.Is(err, errDown) {
		t.Fatalf("Get after Forget = %v, want the failure", err)
	}
}

func TestAStaleAnswerMakesRoomBeforeAFreshOne(t *testing.T) {
	c, clk, _ := staleCache(2)
	var calls atomic.Int32
	if _, _, err := c.Get(t.Context(), "stale", counted(false, &calls)); err != nil {
		t.Fatal(err)
	}
	clk.advance(testTTL)
	for _, key := range []string{"fresh", "new"} {
		if _, _, err := c.Get(t.Context(), key, counted(false, &calls)); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := c.Get(t.Context(), "fresh", failing(errDown)); err != nil {
		t.Errorf("the fresh answer was evicted: %v", err)
	}
	if _, _, err := c.Get(t.Context(), "stale", failing(errDown)); !errors.Is(err, errDown) {
		t.Errorf("Get of the stale answer that made room = %v, want the failure", err)
	}
	if ev := c.Stats().Evictions; ev != 0 {
		t.Errorf("%d evictions, want none: a stale answer is not live", ev)
	}
}
