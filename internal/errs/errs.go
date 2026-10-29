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

// modulePrefix opens the family's text, so a message that opens with it
// too is not named twice.
const modulePrefix = "limes: "

// wrap is family, then the message format makes of args.
func wrap(family error, format string, args []any) error {
	return fmt.Errorf("%w: %s", family, strings.TrimPrefix(fmt.Sprintf(format, args...), modulePrefix))
}
