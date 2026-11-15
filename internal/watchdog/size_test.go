package watchdog

import (
	"sync"
	"testing"
	"time"
)

// A check's limit grows with the store calls it declares, one DefaultLimit
// for every OrdinaryCalls or part of them; an undeclared check is ordinary.
// A limit the env sets is the caller's own bound, and every check gets it as
// it is.
func TestLimitOfScalesWithTheDeclaredCalls(t *testing.T) {
	const envLimit = 5 * time.Minute
	for _, tc := range []struct {
		name  string
		env   time.Duration
		calls int
		want  time.Duration
	}{
		{"an ordinary check", 0, 0, DefaultLimit},
		{"as many calls as an ordinary check makes", 0, OrdinaryCalls, DefaultLimit},
		{"one call past an ordinary check", 0, OrdinaryCalls + 1, 2 * DefaultLimit},
		{"four ordinary checks and one call", 0, 4*OrdinaryCalls + 1, 5 * DefaultLimit},
		{"a negative env limit is unset", -time.Second, 2 * OrdinaryCalls, 2 * DefaultLimit},
		{"the env's limit, for an ordinary check", envLimit, 0, envLimit},
		{"the env's limit, for a large check too", envLimit, 4*OrdinaryCalls + 1, envLimit},
	} {
		if got := limitOf(tc.env, tc.calls); got != tc.want {
			t.Errorf("%s: limitOf(%v, %d) = %v, want %v", tc.name, tc.env, tc.calls, got, tc.want)
		}
	}
}

// RunChecks bounds each check by the limit its own declared calls give: a
// large check runs past an ordinary one's limit, and an ordinary check that
// takes as long is named.
func TestRunChecksBoundsEachCheckByItsOwnSize(t *testing.T) {
	// The check runs past base by half, and the large check's limit is four
	// bases: wide margins either side, as a loaded runner wakes timers late.
	const base = 100 * time.Millisecond
	slow := func(*testing.T, struct{}) { time.Sleep(3 * base / 2) }
	var mu sync.Mutex
	var fired []string
	runChecks(t, func(*testing.T) struct{} { return struct{}{} }, func(struct{}) time.Duration { return 0 }, []Check[struct{}]{
		{Name: "Large", Run: slow, Calls: 4 * OrdinaryCalls},
		{Name: "Ordinary", Run: slow},
	}, base, func(name string, _ time.Duration, _ []byte) {
		// The watchdog fires on a timer goroutine of its own.
		mu.Lock()
		defer mu.Unlock()
		fired = append(fired, name)
	})
	mu.Lock()
	defer mu.Unlock()
	if len(fired) != 1 || fired[0] != "Ordinary" {
		t.Errorf("the watchdog fired on %v, want only the ordinary check", fired)
	}
}
