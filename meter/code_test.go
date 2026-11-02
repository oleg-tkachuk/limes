package meter

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/oleg-tkachuk/limes"
)

// meterSentinels are the meter's exported sentinels, by name.
var meterSentinels = map[string]error{
	"ErrCounterOverflow": ErrCounterOverflow, "ErrChargeNotFound": ErrChargeNotFound,
	"ErrRefundExceedsCharge": ErrRefundExceedsCharge, "ErrReservationNotFound": ErrReservationNotFound,
	"ErrCopyLimitExceeded": ErrCopyLimitExceeded, "ErrUsageNotFound": ErrUsageNotFound,
	"ErrTenantBudgetNotFound": ErrTenantBudgetNotFound, "ErrTenantBudgetVersionMismatch": ErrTenantBudgetVersionMismatch,
	"ErrExternalRefConflict": ErrExternalRefConflict, "ErrSettleConflict": ErrSettleConflict,
	"ErrLeaseExhausted": ErrLeaseExhausted, "ErrLeaseRequestsExhausted": ErrLeaseRequestsExhausted, "ErrLeaseNotFound": ErrLeaseNotFound, "ErrLeaseNested": ErrLeaseNested, "ErrLeasingDisabled": ErrLeasingDisabled,
}

// declaredSentinels are the exported Err variables this package's
// non-test files declare.
func declaredSentinels(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, f, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			if gen, ok := decl.(*ast.GenDecl); ok && gen.Tok == token.VAR {
				for _, spec := range gen.Specs {
					for _, n := range spec.(*ast.ValueSpec).Names {
						if strings.HasPrefix(n.Name, "Err") && n.IsExported() {
							names = append(names, n.Name)
						}
					}
				}
			}
		}
	}
	sort.Strings(names)
	return names
}

// Every sentinel of the meter has a code of its own, unlike any of limes's.
func TestEveryMeterSentinelHasACode(t *testing.T) {
	var listed []string
	for name := range meterSentinels {
		listed = append(listed, name)
	}
	sort.Strings(listed)
	if declared := declaredSentinels(t); !slices.Equal(declared, listed) {
		t.Fatalf("the sources declare %q; the test lists %q", declared, listed)
	}
	for name, err := range meterSentinels {
		r, ok := codeRuleFor(err)
		if !ok || !errors.Is(r.sentinel, err) {
			t.Errorf("%s: no rule of its own", name)
		}
		if slices.Contains(limes.Codes(), r.code) {
			t.Errorf("%s: code %q is limes's", name, r.code)
		}
		_, refused := limes.ReasonOf(err)
		if refused == (r.status != 0) {
			t.Errorf("%s: status %d, a refusal %v; want a status exactly when limes answers no refusal", name, r.status, refused)
		}
	}
	if len(Codes()) != len(meterSentinels) {
		t.Errorf("Codes() lists %d, want %d", len(Codes()), len(meterSentinels))
	}
}

func TestMeterProblemOf(t *testing.T) {
	for name, tc := range map[string]struct {
		err    error
		status int
		code   limes.Code
		reason limes.Reason
	}{
		"a charge not found":     {fmt.Errorf("refund: %w", ErrChargeNotFound), http.StatusNotFound, CodeChargeNotFound, ""},
		"a settle conflicting":   {ErrSettleConflict, http.StatusConflict, CodeSettleConflict, ""},
		"a copy past its limit":  {ErrCopyLimitExceeded, http.StatusForbidden, CodeCopyLimitExceeded, limes.ReasonRequestLimitExceeded},
		"limes's own":            {limes.ErrBudgetExceeded, http.StatusForbidden, limes.CodeBudgetExceeded, limes.ReasonBudgetExceeded},
		"a not found of limes's": {limes.ErrNotFound, http.StatusNotFound, limes.CodeNotFound, ""},
	} {
		p, ok := ProblemOf(tc.err)
		if !ok || p.Status != tc.status || p.Code != tc.code || p.Reason != tc.reason || p.Detail != "" {
			t.Errorf("%s: ProblemOf = %+v, %v; want status %d, code %q, reason %q", name, p, ok, tc.status, tc.code, tc.reason)
		}
		if code, _ := CodeOf(tc.err); code != tc.code {
			t.Errorf("%s: CodeOf = %q, want %q", name, code, tc.code)
		}
	}
	if p, ok := ProblemOf(ErrRefundExceedsCharge); !ok || p.Type != limes.ProblemTypeURI(string(CodeRefundExceedsCharge)) {
		t.Errorf("ProblemOf = %+v: want a type ending in its code", p)
	}
	if _, ok := ProblemOf(errors.New("a store's own")); ok {
		t.Error("ProblemOf of an error not limes's reports one")
	}
}

// docRow reads a row of docs/errors.md's tables: its code and HTTP status.
var docRow = regexp.MustCompile("(?m)^\\| `([a-z_]+)` \\| ([0-9]{3}) \\|")

// docs/errors.md lists every code of the meter's with the status ProblemOf
// answers it with.
func TestErrorsDocListsEveryMeterCode(t *testing.T) {
	raw, err := os.ReadFile("../docs/errors.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := map[string]string{}
	for _, m := range docRow.FindAllStringSubmatch(string(raw), -1) {
		doc[m[1]] = m[2]
	}
	for _, r := range codeRules {
		p, _ := ProblemOf(r.sentinel)
		if got := doc[string(r.code)]; got != strconv.Itoa(p.Status) {
			t.Errorf("docs/errors.md lists %q with status %q; ProblemOf answers %d", r.code, got, p.Status)
		}
	}
}

// Every code of the meter's has one of limes's categories, and CategoryOf
// answers it, through a wrap too; limes's codes it answers as limes does.
func TestEveryMeterCodeHasACategory(t *testing.T) {
	for _, r := range codeRules {
		if !slices.Contains(limes.Categories(), r.category) {
			t.Errorf("%s has category %q, none of limes.Categories()", r.code, r.category)
		}
		if got, ok := CategoryOf(fmt.Errorf("metering: %w", r.sentinel)); !ok || got != r.category {
			t.Errorf("CategoryOf(%v) = %q, %v; want %q", r.sentinel, got, ok, r.category)
		}
	}
	if got, ok := CategoryOf(limes.ErrRevoked); !ok || got != limes.CategoryUnauthenticated {
		t.Errorf("CategoryOf(limes.ErrRevoked) = %q, %v; want %q", got, ok, limes.CategoryUnauthenticated)
	}
	if got, ok := CategoryOf(errors.New("connection reset")); ok {
		t.Errorf("CategoryOf of an error not limes's = %q, want none", got)
	}
}

// docCategory reads a row of docs/errors.md's code tables: its code and, in
// the last column, its category.
var docCategory = regexp.MustCompile("(?m)^\\| `([a-z_]+)` \\| [0-9]{3} \\| .* \\| `([a-z_]+)` \\|$")

// docs/errors.md lists every code of the meter's with its category.
func TestErrorsDocListsEveryMeterCategory(t *testing.T) {
	raw, err := os.ReadFile("../docs/errors.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := map[string]string{}
	for _, m := range docCategory.FindAllStringSubmatch(string(raw), -1) {
		doc[m[1]] = m[2]
	}
	for _, r := range codeRules {
		if got := doc[string(r.code)]; got != string(r.category) {
			t.Errorf("docs/errors.md lists %q in category %q; the code table says %q", r.code, got, r.category)
		}
	}
}

// CategoryOfCode answers the meter's codes as CategoryOf answers their
// sentinels, limes's as limes does, and false for any other.
func TestMeterCategoryOfCode(t *testing.T) {
	for _, r := range codeRules {
		if got, ok := CategoryOfCode(r.code); !ok || got != r.category {
			t.Errorf("CategoryOfCode(%q) = %q, %v; want %q", r.code, got, ok, r.category)
		}
	}
	for _, code := range limes.Codes() {
		want, _ := limes.CategoryOfCode(code)
		if got, ok := CategoryOfCode(code); !ok || got != want {
			t.Errorf("CategoryOfCode(%q) = %q, %v; want limes's %q", code, got, ok, want)
		}
	}
	if got, ok := CategoryOfCode("no_such_code"); ok {
		t.Errorf("CategoryOfCode of an unknown code = %q, want none", got)
	}
}
