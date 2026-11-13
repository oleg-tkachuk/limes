package limes

import (
	"os"
	"os/exec"
	"runtime/debug"
	"testing"
)

// The walk of TestADeepChainWalksInBoundedStack: a chain of deepChainLinks
// errors, walked on a stack held to walkMaxStack, in a process of its own
// (envDeepChainWalk), since a stack that runs out ends the process.
const (
	deepChainLinks   = 1 << 16
	walkMaxStack     = 1 << 20
	envDeepChainWalk = "LIMES_TEST_DEEP_CHAIN_WALK"
	envSet           = "1"
)

// chainLink is one error of a chain, wrapping the next.
type chainLink struct{ next error }

func (l chainLink) Error() string { return "link" }
func (l chainLink) Unwrap() error { return l.next }

// A chain of wrapped errors is walked for the code it names in a stack that
// does not grow with the chain, as errors.As walks it: a chain that loops
// back on itself would otherwise end the process when the stack ran out.
func TestADeepChainWalksInBoundedStack(t *testing.T) {
	if os.Getenv(envDeepChainWalk) == envSet {
		debug.SetMaxStack(walkMaxStack)
		var err error = coded{CodeExpired, ErrExpired}
		for range deepChainLinks {
			err = chainLink{err}
		}
		if code, ok := CodeOf(err); !ok || code != CodeExpired {
			t.Fatalf("CodeOf a deep chain = %q, %v; want %q", code, ok, CodeExpired)
		}
		return
	}
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestADeepChainWalksInBoundedStack$") //nolint:gosec // this test's own binary
	cmd.Env = append(os.Environ(), envDeepChainWalk+"="+envSet)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("the walk of a chain of %d errors on a %d-byte stack: %v\n%s", deepChainLinks, walkMaxStack, err, outputStart(out))
	}
}

// outputKept is how much of a failed walk's output a failure shows.
const outputKept = 2048

// outputStart is the start of out, which says why the walk failed: a
// stack that ran out is followed by every frame on it.
func outputStart(out []byte) []byte {
	if len(out) > outputKept {
		return out[:outputKept]
	}
	return out
}
