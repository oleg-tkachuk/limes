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
	"time"
)

// DefaultLimit is how long one check runs when its Env sets no limit: the
// slowest check makes a few hundred store calls, which a store over a
// database on a loaded CI runner finishes well inside it.
const DefaultLimit = time.Minute

// stackBuffer is how many bytes of goroutine stacks the dump keeps: enough
// for the hundreds of goroutines a concurrent check leaves behind.
const stackBuffer = 1 << 20

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
