package limestest_test

import (
	"fmt"
	"maps"
	"net/http"
	"strings"
	"testing"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/limestest"
)

// recorder is a testing.TB that keeps what a check reports.
type recorder struct {
	testing.TB
	errors []string
}

func (r *recorder) Helper() {}

func (r *recorder) Errorf(format string, args ...any) {
	r.errors = append(r.errors, fmt.Sprintf(format, args...))
}

// current is the table that pins limes as it answers now.
func current() map[limes.Code]limestest.Answer {
	out := map[limes.Code]limestest.Answer{}
	for _, code := range limes.Codes() {
		p, _ := limes.ProblemOfCode(code)
		out[code] = limestest.Answer{Status: p.Status, Category: p.Category}
	}
	return out
}

// checkCodes is what CheckCodes reports for pinned.
func checkCodes(t *testing.T, pinned map[limes.Code]limestest.Answer) []string {
	r := &recorder{TB: t}
	limestest.CheckCodes(r, pinned)
	return r.errors
}

func TestCheckCodes(t *testing.T) {
	if got := checkCodes(t, current()); len(got) != 0 {
		t.Fatalf("limes as it answers now: %q", got)
	}
	if got := current()[limes.CodeRevoked]; got != (limestest.Answer{Status: http.StatusUnauthorized, Category: limes.CategoryUnauthenticated}) {
		t.Errorf("revoked is answered %+v", got)
	}
	for name, tc := range map[string]struct {
		edit func(map[limes.Code]limestest.Answer)
		want []string
	}{
		"a category changed": {
			func(m map[limes.Code]limestest.Answer) {
				m[limes.CodeRevoked] = limestest.Answer{Status: http.StatusUnauthorized, Category: limes.CategoryUnavailable}
			},
			[]string{`"revoked" with status 401, category "unauthenticated"; pinned 401, "unavailable"`},
		},
		"a status changed": {
			func(m map[limes.Code]limestest.Answer) {
				m[limes.CodeNotFound] = limestest.Answer{Status: http.StatusGone, Category: limes.CategoryNotFound}
			},
			[]string{`"not_found" with status 404, category "not_found"; pinned 410, "not_found"`},
		},
		"a code not pinned": {
			func(m map[limes.Code]limestest.Answer) { delete(m, limes.CodeChargeNotFound) },
			[]string{`"charge_not_found", which is not pinned; pin it, mapped: "charge_not_found": {Status: 404, Category: "not_found"},`},
		},
		"a code limes no longer answers": {
			func(m map[limes.Code]limestest.Answer) { m["gone_code"] = limestest.Answer{} },
			[]string{`"gone_code" is pinned, and limes no longer answers it`},
		},
	} {
		pinned := current()
		tc.edit(pinned)
		got := checkCodes(t, pinned)
		if len(got) != len(tc.want) {
			t.Errorf("%s: reported %q, want %d", name, got, len(tc.want))
			continue
		}
		for i, want := range tc.want {
			if !strings.Contains(got[i], want) {
				t.Errorf("%s: reported %q, want it to say %q", name, got[i], want)
			}
		}
	}
}

func TestCheckCategories(t *testing.T) {
	mapped := map[limes.Category]bool{}
	for _, c := range limes.Categories() {
		mapped[c] = true
	}
	check := func(m map[limes.Category]bool) []string {
		r := &recorder{TB: t}
		limestest.CheckCategories(r, func(c limes.Category) bool { return m[c] })
		return r.errors
	}
	if got := check(mapped); len(got) != 0 {
		t.Fatalf("a mapping of every category: %q", got)
	}
	without := maps.Clone(mapped)
	delete(without, limes.CategoryPrecondition)
	got := check(without)
	if len(got) != 1 || !strings.Contains(got[0], `category "precondition"`) {
		t.Errorf("a mapping without precondition: reported %q", got)
	}
}
