package watchdog

import (
	"strings"
	"testing"
	"time"
)

// limit is short enough to keep the test quick, and far longer than a check
// that returns at once takes.
const limit = 50 * time.Millisecond

func TestRunFiresOnAHungCheck(t *testing.T) {
	fired := make(chan []byte, 1)
	release := make(chan struct{})
	run(limit, func() { <-release }, func(stack []byte) {
		fired <- stack
		close(release)
	})
	select {
	case stack := <-fired:
		if !strings.Contains(string(stack), "TestRunFiresOnAHungCheck") {
			t.Errorf("the dump does not name the hung goroutine:\n%s", stack)
		}
	default:
		t.Fatal("the watchdog did not fire on a check that outlived its limit")
	}
}

func TestRunStaysQuietWhenTheCheckReturns(t *testing.T) {
	fired := make(chan struct{}, 1)
	run(limit, func() {}, func([]byte) { fired <- struct{}{} })
	time.Sleep(2 * limit)
	select {
	case <-fired:
		t.Fatal("the watchdog fired after the check returned")
	default:
	}
}

func TestMessageNamesTheCheckAndItsLimit(t *testing.T) {
	got := message("PurgeWorksInBatches", 0, []byte("goroutine 1 [chan receive]"))
	for _, want := range []string{"PurgeWorksInBatches", DefaultLimit.String(), "goroutine 1 [chan receive]"} {
		if !strings.Contains(got, want) {
			t.Errorf("message %q does not carry %q", got, want)
		}
	}
}

func TestEffectiveLimit(t *testing.T) {
	for _, tc := range []struct {
		in, want time.Duration
	}{
		{0, DefaultLimit},
		{-time.Second, DefaultLimit},
		{time.Second, time.Second},
	} {
		if got := effective(tc.in); got != tc.want {
			t.Errorf("effective(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// RunChecks runs each check as its own subtest, in order, each on an env of
// its own from setup.
func TestRunChecksGivesEachCheckItsOwnSubtestAndEnv(t *testing.T) {
	type env struct{ n int }
	setups := 0
	var ran []string
	record := func(t *testing.T, e *env) {
		e.n++
		ran = append(ran, t.Name())
		if e.n != 1 {
			t.Errorf("%s shares an env: %d checks on it", t.Name(), e.n)
		}
	}
	limits := 0
	limitOf := func(e *env) time.Duration {
		limits++
		if e.n != 0 {
			t.Error("the limit was read from an env a check had used")
		}
		return limit
	}
	RunChecks(t, func(*testing.T) *env { setups++; return &env{} }, limitOf, []Check[*env]{{"First", record}, {"Second", record}})
	want := []string{t.Name() + "/First", t.Name() + "/Second"}
	if setups != len(want) || limits != len(want) || strings.Join(ran, ",") != strings.Join(want, ",") {
		t.Errorf("%d setups, %d limits, ran %v; want %d each, %v", setups, limits, ran, len(want), want)
	}
}
