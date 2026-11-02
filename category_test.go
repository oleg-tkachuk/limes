package limes

import (
	"errors"
	"fmt"
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
	for _, r := range codeRules {
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
		{jwks.ErrUnavailable, CategoryUnavailable},
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
