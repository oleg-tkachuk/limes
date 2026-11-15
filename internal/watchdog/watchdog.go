// Package watchdog bounds one check of a contract suite. A store that
// deadlocks — two connections each waiting for the other, a pool drained by
// calls that each wait for a second connection — blocks a check forever, and
// go test then waits for its own -timeout, ten minutes by default, before it
// names the test that hung. The watchdog names the check within its limit,
// and dumps every goroutine's stack, the store's blocked calls among them.
package watchdog

import (
	"fmt"
	"runtime"
	"testing"
	"time"
)

// DefaultLimit is how long an ordinary check — one making at most
// OrdinaryCalls store calls — runs when its Env sets no limit: a store over a
// database on a loaded CI runner finishes that many well inside it.
const DefaultLimit = time.Minute

// OrdinaryCalls is how many store calls DefaultLimit is sized for. A check
// that makes more declares them in Check.Calls and gets one DefaultLimit for
// every OrdinaryCalls or part of them: bounded by its whole run, a check
// seeding a thousand rows one call each on a loaded host is slow, not stuck,
// and a limit sized for the ordinary checks reads it as deadlocked.
const OrdinaryCalls = 300

// stackBuffer is how many bytes of goroutine stacks the dump keeps: enough
// for the hundreds of goroutines a concurrent check leaves behind.
const stackBuffer = 1 << 20

// Check is one check of a contract suite: its subtest's name, the check run
// on the env the suite's setup gives, and the store calls it makes when they
// are more than OrdinaryCalls — zero for an ordinary check.
type Check[E any] struct {
	Name string
	Run  func(*testing.T, E)
	// Calls is how many store calls the check makes, from the constants its
	// loops run over, when that is more than OrdinaryCalls; zero otherwise.
	Calls int
}

// RunChecks runs each check as a subtest, in order, on an env of its own from
// setup, under Run with the limit envLimit reads from that env: as it is when
// the env sets one, and otherwise DefaultLimit scaled to the check's Calls.
func RunChecks[E any](t *testing.T, setup func(*testing.T) E, envLimit func(E) time.Duration, checks []Check[E]) {
	t.Helper()
	runChecks(t, setup, envLimit, checks, DefaultLimit, func(name string, limit time.Duration, stack []byte) {
		panic(message(name, limit, stack))
	})
}

// runChecks is RunChecks on base in place of DefaultLimit, with what a
// timeout does left to the caller.
func runChecks[E any](t *testing.T, setup func(*testing.T) E, envLimit func(E) time.Duration, checks []Check[E], base time.Duration, expired func(name string, limit time.Duration, stack []byte)) {
	t.Helper()
	for _, c := range checks {
		t.Run(c.Name, func(t *testing.T) {
			env := setup(t)
			limit := scaled(envLimit(env), c.Calls, base)
			run(limit, func() { c.Run(t, env) }, func(stack []byte) { expired(c.Name, limit, stack) })
		})
	}
}

// limitOf is the limit of a check making calls store calls under an env
// whose own limit is env: env when it is set, otherwise DefaultLimit for
// every OrdinaryCalls or part of them, and at least one.
func limitOf(env time.Duration, calls int) time.Duration {
	return scaled(env, calls, DefaultLimit)
}

// scaled is limitOf on base in place of DefaultLimit.
func scaled(env time.Duration, calls int, base time.Duration) time.Duration {
	if env > 0 {
		return env
	}
	return base * time.Duration(max(1, (calls+OrdinaryCalls-1)/OrdinaryCalls))
}

// Run runs check, and panics with every goroutine's stack if it has not
// returned within limit — DefaultLimit when limit is zero or negative. A
// panic, not a test failure: a check that never returns cannot be failed, and
// the panic stops the binary at once, as go test's own -timeout would.
func Run(name string, limit time.Duration, check func()) {
	run(limit, check, func(stack []byte) { panic(message(name, limit, stack)) })
}

// message is what Run panics with.
func message(name string, limit time.Duration, stack []byte) string {
	return fmt.Sprintf("check %s did not finish within %v: is the implementation under test deadlocked? Goroutines:\n%s", name, effective(limit), stack)
}

// run is Run with what a timeout does left to the caller.
func run(limit time.Duration, check func(), expired func(stack []byte)) {
	timer := time.AfterFunc(effective(limit), func() {
		buf := make([]byte, stackBuffer)
		expired(buf[:runtime.Stack(buf, true)])
	})
	defer timer.Stop()
	check()
}

func effective(limit time.Duration) time.Duration {
	if limit <= 0 {
		return DefaultLimit
	}
	return limit
}
