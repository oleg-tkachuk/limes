// Package detail carries, beside an error's text, what a client may be shown
// of it: limes.ProblemOf's Detail. An error's text is for the server's log —
// it names kids, issuers, ids, the parent's caveats — and is never shown; a
// detail says only which part of the client's own request was refused and
// which rule it broke.
//
// A Text is safe by construction: a string constant converts to it, a
// string value does not, so nothing a request, a token or a store carries
// can reach one. No code outside this package converts to Text explicitly
// (TestNoTextConversions).
package detail

import "errors"

// Text is a detail a client may be shown: a constant, written where the
// refusal is.
type Text string

// Wrap is err carrying text: its own text and chain unchanged.
func Wrap(err error, text Text) error { return &detailed{err: err, text: text} }

// Of is the detail of the outermost error in err's chain that carries one.
func Of(err error) (Text, bool) {
	var d *detailed
	if !errors.As(err, &d) {
		return "", false
	}
	return d.text, true
}

// detailed is an error carrying a detail.
type detailed struct {
	err  error
	text Text
}

func (d *detailed) Error() string { return d.err.Error() }

// Unwrap is the error the detail was added to.
func (d *detailed) Unwrap() error { return d.err }
