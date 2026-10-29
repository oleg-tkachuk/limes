// Package errs holds the root error families every package of the limes
// module wraps, so that dpop and jwks, which limes imports and so cannot
// import it, refuse with the same sentinels limes exports
// (limes.ErrInvalidRequest, limes.ErrInvalidConfig, limes.ErrInternal):
// one value under each name, matched by errors.Is from any of them.
package errs

import (
	"errors"
	"fmt"
	"strings"
)

// The families.
var (
	// ErrInvalidRequest is a call malformed in itself.
	ErrInvalidRequest = errors.New("limes: invalid request")
	// ErrInvalidConfig is a component wired wrong: a constructor's config, a
	// key or a dependency it cannot work with.
	ErrInvalidConfig = errors.New("limes: invalid configuration")
	// ErrInternal is an invariant of the module broken: a bug, or a store or
	// key behaving otherwise than its contract says.
	ErrInternal = errors.New("limes: internal error")
)

// Request is an ErrInvalidRequest saying what was wrong.
func Request(format string, args ...any) error { return wrap(ErrInvalidRequest, format, args) }

// Config is an ErrInvalidConfig saying what was wrong.
func Config(format string, args ...any) error { return wrap(ErrInvalidConfig, format, args) }

// Broken is an ErrInternal saying which invariant broke.
func Broken(format string, args ...any) error { return wrap(ErrInternal, format, args) }

// modulePrefix opens the text of every error the module returns, and is
// named once in it however many of them one error wraps.
const modulePrefix = "limes: "

// wrap is family, then the message format makes of args.
func wrap(family error, format string, args []any) error {
	return Errorf("%w: %s", family, fmt.Sprintf(format, args...))
}

// Errorf is fmt.Errorf, its text naming modulePrefix once: the first time,
// wherever the format or a wrapped error names it. errors.Is and errors.As
// see what fmt.Errorf would wrap.
func Errorf(format string, args ...any) error {
	err := fmt.Errorf(format, args...)
	return &prefixedOnce{msg: prefixOnce(err.Error()), err: err}
}

// prefixedOnce is an error whose text names modulePrefix once.
type prefixedOnce struct {
	msg string
	err error
}

func (e *prefixedOnce) Error() string { return e.msg }

// Unwrap is what fmt.Errorf built, which wraps the format's %w operands.
func (e *prefixedOnce) Unwrap() error { return e.err }

// prefixOnce is msg with every modulePrefix after the first removed.
func prefixOnce(msg string) string {
	first := strings.Index(msg, modulePrefix)
	if first < 0 {
		return msg
	}
	rest := first + len(modulePrefix)
	return msg[:rest] + strings.ReplaceAll(msg[rest:], modulePrefix, "")
}
