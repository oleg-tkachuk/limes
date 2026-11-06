// Package errcode is how an error of a package limes cannot import names
// its code to limes: the meter's sentinels carry theirs, and limes answers
// them from its own table; the exchange's carry their whole answer, and
// register it, so a code of theirs needs no release of limes.
//
// It is a frozen surface. The nested modules (exchange, biscuit,
// redisreplay, pgstore) import it from production code, and a released
// version of each must still build against every later limes of the same
// major: nothing here is removed, renamed or changed in signature. CI holds
// it so (task verify:nested-internals, .github/ci/nested-internals.ts).
package errcode

import "sync"

// Coded is an error that names its own code.
type Coded interface {
	error
	LimesErrorCode() string
}

// Answered is a Coded that carries the rest of its answer: the HTTP status
// of its problem, its title, and its limes.Category, by name. limes answers
// a code its own table lacks from the error, so a component adds a code
// without a release of limes.
type Answered interface {
	Coded
	LimesErrorStatus() int
	LimesErrorTitle() string
	LimesErrorCategory() string
}

// ComponentExchange names the exchange's codes among those registered.
const ComponentExchange = "exchange"

// Registration is a component's error, registered for limes to answer its
// code without an error to hand.
type Registration struct {
	Component string
	Err       Answered
}

var (
	mu         sync.RWMutex
	registered []Registration
)

// Register makes the codes of errs, component's, known to limes by code
// alone: limes.Codes, ProblemOfCode and CategoryOfCode answer them. A
// package registers its sentinels as it initialises.
func Register(component string, errs ...Answered) {
	mu.Lock()
	defer mu.Unlock()
	for _, err := range errs {
		registered = append(registered, Registration{Component: component, Err: err})
	}
}

// Registered is every registration, in the order made.
func Registered() []Registration {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]Registration, len(registered))
	copy(out, registered)
	return out
}
