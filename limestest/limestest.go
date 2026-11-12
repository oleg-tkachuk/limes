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
	"strings"
	"testing"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/internal/link"
)

// Option loosens a check, each in one way a consumer asks for by name. They
// are of two kinds, which do different things:
//
//   - Without… choose the codes a check walks: each leaves out the codes of
//     one component the consumer does not run. They narrow what is checked,
//     and check the rest as strictly.
//   - Accept… loosen how a code walked and not pinned is judged:
//     AcceptNewByCategory passes such a code that the consumer answers as
//     its category. It checks every code, and fails on fewer.
//
// None leaves out a code alone. With none, a check walks every code, and a
// code limes adds fails it until it is pinned: that default is the
// strictest there is, and no option will change it.
type Option func(*checked)

// checked is how a check is loosened: the codes it leaves out, and whether
// it takes a code not pinned by its category.
type checked struct {
	without    []limes.Code
	byCategory bool
}

// leaveOut adds codes, strings of limes.Code, to those a check leaves out.
func (c *checked) leaveOut(codes []string) {
	for _, code := range codes {
		c.without = append(c.without, limes.Code(code))
	}
}

// WithoutExchange leaves the exchange's codes out of a check, for a
// consumer that runs no exchange (module exchange): it never meets them,
// and pins none.
func WithoutExchange() Option {
	return func(c *checked) { c.leaveOut(link.ExchangeCodes()) }
}

// WithoutMeter leaves the meter's codes out of a check, for a consumer
// that meters nothing (package meter, a usage store): it never meets them,
// and pins none.
func WithoutMeter() Option {
	return func(c *checked) { c.leaveOut(link.MeterCodes()) }
}

// AcceptNewByCategory passes a code that is not pinned when the consumer
// answers it as it answers every code pinned in its category, and there is
// one: a code limes adds to a category the consumer maps whole then passes,
// while a pinned code moved to another category still fails, as does a code
// in a category with none pinned, or one whose pinned codes are answered
// apart.
//
// A code it passes so fails nothing, and the check names it only in its
// log, in one line, which go test shows with -v alone: a green run without
// it says nothing of the table falling behind. Run the check with -v, or
// now and then without this option, to pin the codes it took.
func AcceptNewByCategory() Option {
	return func(c *checked) { c.byCategory = true }
}

// settings are what opts ask of a check.
func settings(opts []Option) checked {
	var c checked
	for _, opt := range opts {
		opt(&c)
	}
	return c
}

// codesIn are the codes of limes.Codes that c leaves in.
func (c checked) codesIn() []limes.Code {
	return slices.DeleteFunc(limes.Codes(), func(code limes.Code) bool { return slices.Contains(c.without, code) })
}

// asItsCategory reports whether every code of codes pinned in code's
// category, and there is one, is pinned as got.
func asItsCategory[T comparable](code limes.Code, got T, codes []limes.Code, pinned map[limes.Code]T) bool {
	category, _ := limes.CategoryOfCode(code)
	peers := 0
	for _, other := range codes {
		want, ok := pinned[other]
		if c, _ := limes.CategoryOfCode(other); !ok || c != category {
			continue
		}
		if want != got {
			return false
		}
		peers++
	}
	return peers > 0
}

// CheckMapping fails t for every way the consumer's answer to limes's codes
// — every one, but those opts leave out — differs from pinned, its record of
// the answers its mapping was written to give: a code answer maps otherwise
// than pinned, a code not pinned — unless AcceptNewByCategory passes it —
// and a code pinned that is not checked: which limes no longer answers, or
// opts left out. Each failure of a code checked names the entry to pin, so
// the first failing run writes the table; failures come in the order of
// their codes' names, which no release changes: those of the codes checked
// first, then those of the codes pinned and not checked.
func CheckMapping[T comparable](t testing.TB, pinned map[limes.Code]T, answer func(limes.Code) T, opts ...Option) {
	t.Helper()
	c := settings(opts)
	codes := c.codesIn()
	var accepted []string
	for _, code := range slices.Sorted(slices.Values(codes)) {
		got := answer(code)
		want, ok := pinned[code]
		switch {
		case !ok && c.byCategory && asItsCategory(code, got, codes, pinned):
			accepted = append(accepted, fmt.Sprintf("%q: %s,", code, literal(got)))
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
	if len(accepted) > 0 {
		t.Logf("limestest: %d codes not pinned, passed as their categories are answered; pin them: %s", len(accepted), strings.Join(accepted, " "))
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
