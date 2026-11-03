// Package limestest holds a consumer's mapping of limes's errors to the
// error contract it was written against (docs/compatibility.md). A code
// answered with another status or category is a break, yet it compiles: a
// consumer mapping categories onto its transport goes on building, and
// answers otherwise. Pin what the consumer's own transport answers each
// code with, and a limes that makes it answer otherwise fails the
// consumer's test instead of its traffic:
//
//	func TestErrorContract(t *testing.T) {
//		codes := slices.Concat(limes.CodesOf(limes.OriginLimes), limes.CodesOf(limes.OriginMeter))
//		limestest.CheckMapping(t, codes, map[limes.Code]connect.Code{
//			"revoked": connect.CodeUnauthenticated,
//			// …
//		}, func(c limes.Code) connect.Code {
//			p, _ := limes.ProblemOfCode(c)
//			return connectCode(p)
//		})
//	}
//
// CheckCodes is CheckMapping of every code to its HTTP status and
// category, for a consumer that answers in HTTP's terms.
package limestest

import (
	"slices"
	"sort"
	"testing"

	"github.com/oleg-tkachuk/limes"
)

// CheckMapping fails t for every way the consumer's answer to codes differs
// from pinned, its record of the answers its mapping was written to give: a
// code of codes answer maps otherwise than pinned, a code of codes not
// pinned, and a code pinned that is none of codes — which limes no longer
// answers, or the consumer left out of codes. Each failure names the entry
// to pin, so the first failing run writes the table.
func CheckMapping[T comparable](t testing.TB, codes []limes.Code, pinned map[limes.Code]T, answer func(limes.Code) T) {
	t.Helper()
	for _, code := range codes {
		got := answer(code)
		want, ok := pinned[code]
		switch {
		case !ok:
			t.Errorf("limestest: %q is not pinned; pin it, mapped: %q: %#v,", code, code, got)
		case want != got:
			t.Errorf("limestest: %q is answered %#v; pinned %#v: map it anew, then pin %q: %#v,", code, got, want, code, got)
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
			t.Errorf("limestest: %q is pinned, and not among the codes checked", code)
		} else {
			t.Errorf("limestest: %q is pinned, and limes no longer answers it", code)
		}
	}
}

// Answer is how limes answers a code in HTTP's terms.
type Answer struct {
	// Status is the HTTP status, Problem.Status.
	Status int
	// Category is the code's category, Problem.Category.
	Category limes.Category
}

// CheckCodes is CheckMapping of every code limes answers, the meter's and
// the exchange's among them, to its Answer.
func CheckCodes(t testing.TB, pinned map[limes.Code]Answer) {
	t.Helper()
	CheckMapping(t, limes.Codes(), pinned, answerOf)
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
