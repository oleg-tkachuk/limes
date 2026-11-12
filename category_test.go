package limes

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"slices"
	"testing"

	"github.com/oleg-tkachuk/limes/jwks"
)

// docCategory reads a row of errorsDoc's code tables: its code and, in the
// last column, its category.
var docCategory = regexp.MustCompile("(?m)^\\| `([a-z_]+)` \\| [0-9]{3} \\| .* \\| `([a-z_]+)` \\|$")

// Every code has one of the categories, so a consumer mapping the categories
// maps every error; a new code without one fails here.
func TestEveryCodeHasACategory(t *testing.T) {
	for _, r := range allCodeRules() {
		if !slices.Contains(Categories(), r.category) {
			t.Errorf("%s has category %q, none of Categories()", r.code, r.category)
		}
	}
}

// CategoryOf answers as the code table does, through a wrap too, and false
// for an error that is not limes's.
func TestCategoryOf(t *testing.T) {
	cases := []struct {
		err  error
		want Category
	}{
		{ErrRevoked, CategoryUnauthenticated},
		{ErrOpNotAllowed, CategoryDenied},
		{ErrBudgetExceeded, CategoryExhausted},
		{ErrInvalidRequest, CategoryInvalid},
		{ErrNotFound, CategoryNotFound},
		{ErrConflict, CategoryConflict},
		{ErrAlreadyExists, CategoryAlreadyExists},
		{ErrMalformedAuthorization, CategoryInvalid},
		{ErrUnitCodeMismatch, CategoryPrecondition},
		{ErrProofUnsupported, CategoryInternal},
		{ErrRevocationUnavailable, CategoryUnavailable},
		{jwks.ErrUnavailable, CategoryUnavailable},
		{ErrCursorRepeated, CategoryUnavailable},
		{ErrNoRevocationLineage, CategoryInternal},
		{ErrInvalidConfig, CategoryInternal},
		{fmt.Errorf("charging: %w", ErrTenantBudgetExceeded), CategoryExhausted},
	}
	for _, tc := range cases {
		if got, ok := CategoryOf(tc.err); !ok || got != tc.want {
			t.Errorf("CategoryOf(%v) = %q, %v; want %q", tc.err, got, ok, tc.want)
		}
	}
	for _, err := range []error{nil, errors.New("connection reset")} {
		if got, ok := CategoryOf(err); ok {
			t.Errorf("CategoryOf(%v) = %q, want none", err, got)
		}
	}
}

// docs/errors.md lists every code with its category.
func TestErrorsDocListsEveryCategory(t *testing.T) {
	raw, err := os.ReadFile(errorsDoc)
	if err != nil {
		t.Fatal(err)
	}
	doc := map[string]string{}
	for _, m := range docCategory.FindAllStringSubmatch(string(raw), -1) {
		doc[m[1]] = m[2]
	}
	for _, r := range codeRules {
		if got := doc[string(r.code)]; got != string(r.category) {
			t.Errorf("%s lists %q in category %q; the code table says %q", errorsDoc, r.code, got, r.category)
		}
	}
}

// CategoryOfCode answers every code as CategoryOf answers its sentinel, so
// a consumer can map codes without an error to hand, and false for a code
// limes does not define.
func TestCategoryOfCode(t *testing.T) {
	for _, r := range codeRules {
		want, _ := CategoryOf(r.sentinel)
		if got, ok := CategoryOfCode(r.code); !ok || got != want {
			t.Errorf("CategoryOfCode(%q) = %q, %v; want %q", r.code, got, ok, want)
		}
	}
	if got, ok := CategoryOfCode("no_such_code"); ok {
		t.Errorf("CategoryOfCode of an unknown code = %q, want none", got)
	}
}

// A category agrees with the status: every error answered 502, 503 or 504
// is a dependency that failed, unavailable.
func TestEveryGatewayStatusIsUnavailable(t *testing.T) {
	for _, r := range codeRules {
		p, _ := ProblemOf(r.sentinel)
		gateway := p.Status == http.StatusBadGateway || p.Status == http.StatusServiceUnavailable || p.Status == http.StatusGatewayTimeout
		if gateway != (r.category == CategoryUnavailable) {
			t.Errorf("%s answers %d in category %q", r.code, p.Status, r.category)
		}
	}
}

// statusClass is the class of an HTTP status: its hundreds.
func statusClass(status int) int { return status / 100 }

// A category spans one class of status: the client's 4xx or the server's
// 5xx, never both, so whose fault an error is follows from its category.
func TestEveryCategoryIsOneStatusClass(t *testing.T) {
	standInExchange(t)
	classes := map[Category]map[int][]Code{}
	for _, code := range Codes() {
		p, ok := ProblemOfCode(code)
		if !ok {
			t.Fatalf("ProblemOfCode(%q) answers nothing", code)
		}
		if classes[p.Category] == nil {
			classes[p.Category] = map[int][]Code{}
		}
		classes[p.Category][statusClass(p.Status)] = append(classes[p.Category][statusClass(p.Status)], code)
	}
	for category, byClass := range classes {
		if len(byClass) != 1 {
			t.Errorf("category %q spans status classes %v", category, byClass)
		}
	}
}

// ReasonOfCode answers every code as ReasonOf answers its sentinel: a
// refusal its reason, a wider one where the reason folds it, and no reason
// for an error that is no refusal or a code limes does not define.
func TestReasonOfCode(t *testing.T) {
	for _, r := range codeRules {
		want, wantOK := ReasonOf(r.sentinel)
		if got, ok := ReasonOfCode(r.code); got != want || ok != wantOK {
			t.Errorf("ReasonOfCode(%q) = %q, %v; want %q, %v", r.code, got, ok, want, wantOK)
		}
	}
	for code, want := range map[Code]Reason{CodeRevoked: ReasonRevoked, CodeTenantErased: ReasonRevoked, CodeExpired: ReasonExpired} {
		if got, ok := ReasonOfCode(code); !ok || got != want {
			t.Errorf("ReasonOfCode(%q) = %q, %v; want %q", code, got, ok, want)
		}
	}
	for _, code := range []Code{CodeNotFound, "no_such_code"} {
		if got, ok := ReasonOfCode(code); ok {
			t.Errorf("ReasonOfCode(%q) = %q, want none", code, got)
		}
	}
}
