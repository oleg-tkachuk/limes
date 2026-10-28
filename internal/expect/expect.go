// Package expect reports a failed expectation of the contract suites,
// storetest and metertest, as one call rather than an if around an Errorf or
// a Fatalf: a suite's checks then read as the list of what a store must do,
// and a check costs no branch of its own in the suite's complexity.
package expect

// Reporter is the part of testing.TB a report needs.
type Reporter interface {
	Helper()
	Errorf(format string, args ...any)
}

// Stopper is the part of testing.TB a report that stops the test needs.
type Stopper interface {
	Helper()
	Fatalf(format string, args ...any)
}

// That reports format and args as an error of t unless ok, and carries on.
// The report names the caller's line.
func That(t Reporter, ok bool, format string, args ...any) {
	t.Helper()
	if !ok {
		t.Errorf(format, args...)
	}
}

// Must reports format and args as a fatal error of t unless ok, which stops
// the test: what follows needs ok. Called from the test's goroutine only, as
// t.Fatalf is.
func Must(t Stopper, ok bool, format string, args ...any) {
	t.Helper()
	if !ok {
		t.Fatalf(format, args...)
	}
}
