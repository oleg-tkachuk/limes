// Package limestest holds a consumer's mapping of limes's errors to the
// error contract it was written against (docs/compatibility.md). A code
// answered with another status or category is a break, yet it compiles: a
// consumer mapping categories onto its transport goes on building, and
// answers otherwise. Pin what the consumer's own transport answers each
// code with, and a limes that makes it answer otherwise fails the
// consumer's test instead of its traffic:
//
//	func TestErrorContract(t *testing.T) {
//		limestest.CheckMapping(t, map[limes.Code]connect.Code{
//			"revoked": connect.CodeUnauthenticated,
//			// …
//		}, func(c limes.Code) connect.Code {
//			p, _ := limes.ProblemOfCode(c)
//			return connectCode(p)
//		}, limestest.WithoutExchange())
//	}
//
// CheckCodes is CheckMapping of every code to its HTTP status and
// category, for a consumer that answers in HTTP's terms.
package limestest

import (
	"fmt"
	"reflect"
	"slices"
	"sort"
	"testing"

	"github.com/oleg-tkachuk/limes"
)

// Option narrows the codes a check walks.
type Option func(*checked)

// checked is the codes a check walks, and those it leaves out.
type checked struct {
	without []limes.Code
}

// WithoutExchange leaves the exchange's codes (limes.ExchangeCodes) out of
// a check, for a consumer that runs no exchange: it never meets them, and
// pins none.
func WithoutExchange() Option {
	return func(c *checked) { c.without = append(c.without, limes.ExchangeCodes()...) }
}

// codesFor are the codes of limes.Codes that opts leave in.
func codesFor(opts []Option) []limes.Code {
	var c checked
	for _, opt := range opts {
		opt(&c)
	}
	return slices.DeleteFunc(limes.Codes(), func(code limes.Code) bool { return slices.Contains(c.without, code) })
}

// CheckMapping fails t for every way the consumer's answer to limes's codes
// — every one, but those opts leave out — differs from pinned, its record of
// the answers its mapping was written to give: a code answer maps otherwise
// than pinned, a code not pinned, and a code pinned that is not checked —
// which limes no longer answers, or opts left out. Each failure names the
// entry to pin, so the first failing run writes the table.
func CheckMapping[T comparable](t testing.TB, pinned map[limes.Code]T, answer func(limes.Code) T, opts ...Option) {
	t.Helper()
	codes := codesFor(opts)
	for _, code := range codes {
		got := answer(code)
		want, ok := pinned[code]
		switch {
		case !ok:
			t.Errorf("limestest: %q is not pinned; pin it, mapped: %q: %s,", code, code, literal(got))
		case want != got:
			t.Errorf("limestest: %q is answered %s; pinned %s: map it anew, then pin %q: %s,", code, literal(got), literal(want), code, literal(got))
		}
	}
	answered := limes.Codes()
	var extra []string
	for code := range pinned {
		if !slices.Contains(codes, code) {
			extra = append(extra, string(code))
		}
	}
	sort.Strings(extra)
	for _, code := range extra {
		if slices.Contains(answered, limes.Code(code)) {
			t.Errorf("limestest: %q is pinned, and the check leaves it out", code)
		} else {
			t.Errorf("limestest: %q is pinned, and limes no longer answers it", code)
		}
	}
}

// literal is v in Go's syntax and, for a v that is no string and names
// itself (fmt.Stringer), its name beside it: a transport's codes are
// integers, whose literal alone says nothing.
func literal[T any](v T) string {
	if named, ok := any(v).(fmt.Stringer); ok && reflect.ValueOf(v).Kind() != reflect.String {
		return fmt.Sprintf("%#v /* %s */", v, named.String())
	}
	return fmt.Sprintf("%#v", v)
}

// Answer is how limes answers a code in HTTP's terms.
type Answer struct {
	// Status is the HTTP status, Problem.Status.
	Status int
	// Category is the code's category, Problem.Category.
	Category limes.Category
}

// CheckCodes is CheckMapping of limes's codes, the meter's and the
// exchange's among them but for those opts leave out, to their Answer.
func CheckCodes(t testing.TB, pinned map[limes.Code]Answer, opts ...Option) {
	t.Helper()
	CheckMapping(t, pinned, answerOf, opts...)
}

// answerOf is code's Answer.
func answerOf(code limes.Code) Answer {
	p, _ := limes.ProblemOfCode(code)
	return Answer{Status: p.Status, Category: p.Category}
}

// CheckCategories fails t for every category of limes.Categories() that
// mapped reports it does not map: one added after the consumer's mapping
// was written, which that mapping answers by its default until it maps it
// (docs/errors.md, "Categories").
func CheckCategories(t testing.TB, mapped func(limes.Category) bool) {
	t.Helper()
	for _, c := range limes.Categories() {
		if !mapped(c) {
			t.Errorf("limestest: the mapping does not map category %q; until it does, its default answers it", c)
		}
	}
}
