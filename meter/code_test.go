package meter

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/oleg-tkachuk/limes"
)

// meterSentinels are the meter's exported sentinels, by name, each with its
// code.
var meterSentinels = map[string]struct {
	err  error
	code limes.Code
}{
	"ErrCounterOverflow":             {ErrCounterOverflow, CodeCounterOverflow},
	"ErrChargeNotFound":              {ErrChargeNotFound, CodeChargeNotFound},
	"ErrRefundExceedsCharge":         {ErrRefundExceedsCharge, CodeRefundExceedsCharge},
	"ErrReservationNotFound":         {ErrReservationNotFound, CodeReservationNotFound},
	"ErrCopyLimitExceeded":           {ErrCopyLimitExceeded, CodeCopyLimitExceeded},
	"ErrUsageNotFound":               {ErrUsageNotFound, CodeUsageNotFound},
	"ErrTenantBudgetNotFound":        {ErrTenantBudgetNotFound, CodeTenantBudgetNotFound},
	"ErrTenantBudgetVersionMismatch": {ErrTenantBudgetVersionMismatch, CodeTenantBudgetVersionMismatch},
	"ErrExternalRefConflict":         {ErrExternalRefConflict, CodeExternalRefConflict},
	"ErrSettleConflict":              {ErrSettleConflict, CodeSettleConflict},
	"ErrLeaseExhausted":              {ErrLeaseExhausted, CodeLeaseExhausted},
	"ErrLeaseRequestsExhausted":      {ErrLeaseRequestsExhausted, CodeLeaseRequestsExhausted},
	"ErrLeaseNotFound":               {ErrLeaseNotFound, CodeLeaseNotFound},
	"ErrLeaseNested":                 {ErrLeaseNested, CodeLeaseNested},
	"ErrLeasingDisabled":             {ErrLeasingDisabled, CodeLeasingDisabled},
	"ErrFeatureDisabled":             {ErrFeatureDisabled, CodeFeatureDisabled},
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

// Every sentinel of the meter has a code of its own, which limes's own
// functions answer — through a wrap too — so a consumer needs no second
// registry: limes.Codes lists it, after every code of limes's.
func TestLimesKnowsEveryMeterCode(t *testing.T) {
	var listed []string
	for name := range meterSentinels {
		listed = append(listed, name)
	}
	sort.Strings(listed)
	if declared := declaredSentinels(t); !slices.Equal(declared, listed) {
		t.Fatalf("the sources declare %q; the test lists %q", declared, listed)
	}
	codes := limes.Codes()
	for name, s := range meterSentinels {
		wrapped := fmt.Errorf("metering: %w", s.err)
		if got, ok := limes.CodeOf(wrapped); !ok || got != s.code {
			t.Errorf("limes.CodeOf(%s) = %q, %v; want %q", name, got, ok, s.code)
		}
		category, ok := limes.CategoryOf(wrapped)
		if !ok || !slices.Contains(limes.Categories(), category) {
			t.Errorf("limes.CategoryOf(%s) = %q, %v; want one of limes.Categories()", name, category, ok)
		}
		if got, ok := limes.CategoryOfCode(s.code); !ok || got != category {
			t.Errorf("limes.CategoryOfCode(%q) = %q, %v; want %q, as CategoryOf answers", s.code, got, ok, category)
		}
		p, ok := limes.ProblemOf(wrapped)
		if !ok || p.Code != s.code || p.Category != category || p.Status == 0 {
			t.Errorf("limes.ProblemOf(%s) = %+v, %v; want code %q, category %q", name, p, ok, s.code, category)
		}
		if got, ok := limes.ProblemOfCode(s.code); !ok || !reflect.DeepEqual(got, p) {
			t.Errorf("limes.ProblemOfCode(%q) = %+v, %v; want %+v, as ProblemOf answers", s.code, got, ok, p)
		}
		if !slices.Contains(codes, s.code) {
			t.Errorf("limes.Codes() lacks %q", s.code)
		}
	}
	if tail := codes[len(codes)-len(meterSentinels):]; slices.ContainsFunc(tail, func(c limes.Code) bool {
		_, own := meterSentinels[sentinelOf(c)]
		return !own
	}) {
		t.Errorf("limes.Codes() does not end with the meter's codes: %q", tail)
	}
}

// sentinelOf is the name of the meter sentinel with code, or "".
func sentinelOf(code limes.Code) string {
	for name, s := range meterSentinels {
		if s.code == code {
			return name
		}
	}
	return ""
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
		"a lease nested":         {ErrLeaseNested, http.StatusConflict, CodeLeaseNested, ""},
		"a copy past its limit":  {ErrCopyLimitExceeded, http.StatusForbidden, CodeCopyLimitExceeded, limes.ReasonRequestLimitExceeded},
		"limes's own":            {limes.ErrBudgetExceeded, http.StatusForbidden, limes.CodeBudgetExceeded, limes.ReasonBudgetExceeded},
		"a not found of limes's": {limes.ErrNotFound, http.StatusNotFound, limes.CodeNotFound, ""},
	} {
		p, ok := limes.ProblemOf(tc.err)
		if !ok || p.Status != tc.status || p.Code != tc.code || p.Reason != tc.reason || p.Detail != "" {
			t.Errorf("%s: ProblemOf = %+v, %v; want status %d, code %q, reason %q", name, p, ok, tc.status, tc.code, tc.reason)
		}
	}
	if p, ok := limes.ProblemOf(ErrRefundExceedsCharge); !ok || p.Type != limes.ProblemTypeURI(string(CodeRefundExceedsCharge)) {
		t.Errorf("ProblemOf = %+v: want a type ending in its code", p)
	}
}

// docRow reads a row of docs/errors.md's tables: its code and HTTP status.
var docRow = regexp.MustCompile("(?m)^\\| `([a-z_]+)` \\| ([0-9]{3}) \\|")

// docs/errors.md lists every code of the meter's with the status
// limes.ProblemOf answers it with.
func TestErrorsDocListsEveryMeterCode(t *testing.T) {
	raw, err := os.ReadFile("../docs/errors.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := map[string]string{}
	for _, m := range docRow.FindAllStringSubmatch(string(raw), -1) {
		doc[m[1]] = m[2]
	}
	for _, s := range meterSentinels {
		p, _ := limes.ProblemOf(s.err)
		if got := doc[string(s.code)]; got != strconv.Itoa(p.Status) {
			t.Errorf("docs/errors.md lists %q with status %q; ProblemOf answers %d", s.code, got, p.Status)
		}
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
	for _, s := range meterSentinels {
		want, _ := limes.CategoryOf(s.err)
		if got := doc[string(s.code)]; got != string(want) {
			t.Errorf("docs/errors.md lists %q in category %q; limes.CategoryOf says %q", s.code, got, want)
		}
	}
}

// limes.ReasonOfCode answers the meter's refusals with the reason they fold
// into, and no reason for its other codes.
func TestMeterReasonOfCode(t *testing.T) {
	for code, want := range map[limes.Code]limes.Reason{
		CodeCopyLimitExceeded:      limes.ReasonRequestLimitExceeded,
		CodeLeaseExhausted:         limes.ReasonBudgetExceeded,
		CodeLeaseRequestsExhausted: limes.ReasonRequestLimitExceeded,
	} {
		if got, ok := limes.ReasonOfCode(code); !ok || got != want {
			t.Errorf("ReasonOfCode(%q) = %q, %v; want %q", code, got, ok, want)
		}
	}
	for _, code := range []limes.Code{CodeChargeNotFound, CodeFeatureDisabled} {
		if got, ok := limes.ReasonOfCode(code); ok {
			t.Errorf("ReasonOfCode(%q) = %q, want none", code, got)
		}
	}
}

// A request well formed whose state is not there — a counter with no room,
// a lease above, leasing turned off — is a precondition, apart from a limit
// refusing it and from a bad request.
func TestMeterPreconditions(t *testing.T) {
	for _, err := range []error{ErrCounterOverflow, ErrRefundExceedsCharge, ErrLeaseNested, ErrLeasingDisabled, ErrFeatureDisabled} {
		if got, ok := limes.CategoryOf(err); !ok || got != limes.CategoryPrecondition {
			t.Errorf("CategoryOf(%v) = %q, %v; want precondition", err, got, ok)
		}
	}
}
