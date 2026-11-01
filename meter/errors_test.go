package meter

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/oleg-tkachuk/limes"
)

// The limes families a meter sentinel may belong to, as errors.Is reads them.
const (
	famRequestLimit = "RequestLimitExceeded"
	famBudget       = "BudgetExceeded"
	famNotFound     = "NotFound"
	famConflict     = "Conflict"
	famRequest      = "InvalidRequest"
)

var families = map[string]error{
	famRequestLimit: limes.ErrRequestLimitExceeded,
	famBudget:       limes.ErrBudgetExceeded,
	famNotFound:     limes.ErrNotFound,
	famConflict:     limes.ErrConflict,
	famRequest:      limes.ErrInvalidRequest,
}

// sentinelCase is one exported sentinel: the families it matches, the
// limes.Reason limes.ReasonOf gives it ("" for none) and the status
// ErrorResponse answers it with.
type sentinelCase struct {
	err      error
	families []string
	reason   limes.Reason
	status   int
}

// sentinelMatrix is every sentinel of this package and how limes reads it. A
// sentinel added to the package and left out of it fails
// TestSentinelMatrixIsComplete.
var sentinelMatrix = map[string]sentinelCase{
	"ErrCopyLimitExceeded":           {ErrCopyLimitExceeded, []string{famRequestLimit}, limes.ReasonRequestLimitExceeded, http.StatusForbidden},
	"ErrChargeNotFound":              {ErrChargeNotFound, []string{famNotFound}, "", http.StatusInternalServerError},
	"ErrReservationNotFound":         {ErrReservationNotFound, []string{famNotFound}, "", http.StatusInternalServerError},
	"ErrUsageNotFound":               {ErrUsageNotFound, []string{famNotFound}, "", http.StatusInternalServerError},
	"ErrTenantBudgetNotFound":        {ErrTenantBudgetNotFound, []string{famNotFound}, "", http.StatusInternalServerError},
	"ErrExternalRefConflict":         {ErrExternalRefConflict, []string{famConflict}, "", http.StatusConflict},
	"ErrSettleConflict":              {ErrSettleConflict, []string{famConflict}, "", http.StatusConflict},
	"ErrLeaseExhausted":              {ErrLeaseExhausted, []string{famBudget}, limes.ReasonBudgetExceeded, http.StatusForbidden},
	"ErrLeaseNotFound":               {ErrLeaseNotFound, []string{famNotFound}, "", http.StatusInternalServerError},
	"ErrLeaseNested":                 {ErrLeaseNested, []string{famConflict}, "", http.StatusConflict},
	"ErrTenantBudgetVersionMismatch": {ErrTenantBudgetVersionMismatch, []string{famConflict}, "", http.StatusConflict},
	"ErrCounterOverflow":             {ErrCounterOverflow, nil, "", http.StatusInternalServerError},
	"ErrRefundExceedsCharge":         {ErrRefundExceedsCharge, nil, "", http.StatusInternalServerError},
}

// Each sentinel matches exactly the families the matrix names, has the
// Reason it names and is answered with the status it names.
func TestSentinelMatrix(t *testing.T) {
	v := &limes.Verifier{}
	for name, c := range sentinelMatrix {
		for family, target := range families {
			want := slices.Contains(c.families, family)
			if got := errors.Is(c.err, target); got != want {
				t.Errorf("errors.Is(%s, limes.Err%s) = %v, want %v", name, family, got, want)
			}
		}
		got, ok := limes.ReasonOf(c.err)
		if c.reason == "" && ok {
			t.Errorf("ReasonOf(%s) = %q, want none", name, got)
		}
		if c.reason != "" && got != c.reason {
			t.Errorf("ReasonOf(%s) = %q, want %q", name, got, c.reason)
		}
		if status := v.ErrorResponse(context.Background(), c.err, limes.AuthSchemeBearer).Status; status != c.status {
			t.Errorf("ErrorResponse(%s).Status = %d, want %d", name, status, c.status)
		}
	}
}

// Every exported sentinel of the package is in the matrix.
func TestSentinelMatrixIsComplete(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.VAR {
				continue
			}
			for _, spec := range gen.Specs {
				for _, ident := range spec.(*ast.ValueSpec).Names {
					if ident.IsExported() && strings.HasPrefix(ident.Name, "Err") {
						if _, ok := sentinelMatrix[ident.Name]; !ok {
							t.Errorf("%s (%s) is not in sentinelMatrix", ident.Name, file)
						}
					}
				}
			}
		}
	}
}

// No sentinel of the package matches another, and each survives wrapping.
func TestSentinelsAreIndividuallyMatchable(t *testing.T) {
	for nameA, a := range sentinelMatrix {
		for nameB, b := range sentinelMatrix {
			if nameA != nameB && errors.Is(a.err, b.err) {
				t.Errorf("%s matches %s — they must stay distinguishable", nameA, nameB)
			}
		}
		if !errors.Is(errors.Join(errors.New("ctx"), a.err), a.err) {
			t.Errorf("%s is lost when wrapped", nameA)
		}
	}
}
