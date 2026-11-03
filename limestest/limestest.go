// Package limestest holds a consumer's mapping of limes's errors to the
// error contract it was written against (docs/compatibility.md). A code
// answered with another status or category is a break, yet it compiles: a
// consumer mapping categories onto its transport goes on building, and
// answers otherwise. Pin the answers the mapping was written for, and a
// limes that answers otherwise fails the consumer's test instead of its
// traffic:
//
//	func TestErrorContract(t *testing.T) {
//		limestest.CheckCodes(t, pinned)
//		limestest.CheckCategories(t, func(c limes.Category) bool {
//			_, ok := transportCodes[c]
//			return ok
//		})
//	}
//
// CheckCodes names each entry to pin in Go's own syntax, so the first
// failing run writes the table.
package limestest

import (
	"fmt"
	"slices"
	"sort"
	"testing"

	"github.com/oleg-tkachuk/limes"
)

// Answer is how limes answers a code: what a consumer's mapping reads.
type Answer struct {
	// Status is the HTTP status, Problem.Status.
	Status int
	// Category is the code's category, Problem.Category.
	Category limes.Category
}

// CheckCodes fails t for every way limes's codes, the meter's among them,
// differ from pinned, the consumer's record of the ones its mapping was
// written against: a code limes answers with another status or category
// than pinned, a code limes answers that is not pinned, and a code pinned
// that limes no longer answers.
func CheckCodes(t testing.TB, pinned map[limes.Code]Answer) {
	t.Helper()
	codes := limes.Codes()
	for _, code := range codes {
		p, _ := limes.ProblemOfCode(code)
		got := Answer{Status: p.Status, Category: p.Category}
		want, ok := pinned[code]
		switch {
		case !ok:
			t.Errorf("limestest: limes answers %q, which is not pinned; pin it, mapped: %s", code, entry(code, got))
		case want != got:
			t.Errorf("limestest: limes answers %q with status %d, category %q; pinned %d, %q: map it anew, then pin %s",
				code, got.Status, got.Category, want.Status, want.Category, entry(code, got))
		}
	}
	var gone []string
	for code := range pinned {
		if !slices.Contains(codes, code) {
			gone = append(gone, string(code))
		}
	}
	sort.Strings(gone)
	for _, code := range gone {
		t.Errorf("limestest: %q is pinned, and limes no longer answers it", code)
	}
}

// entry is the pinned table's entry for code answered as a.
func entry(code limes.Code, a Answer) string {
	return fmt.Sprintf("%q: {Status: %d, Category: %q},", code, a.Status, a.Category)
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
