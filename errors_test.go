package limes

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
)

// The families a sentinel may belong to, as errors.Is reads them.
const (
	famUnauthenticated = "Unauthenticated"
	famSignature       = "InvalidSignature"
	famProof           = "ProofOfPossession"
	famCaveat          = "CaveatViolation"
	famRequest         = "InvalidRequest"
	famNotFound        = "NotFound"
)

var families = map[string]error{
	famUnauthenticated: ErrUnauthenticated,
	famSignature:       ErrInvalidSignature,
	famProof:           ErrProofOfPossession,
	famCaveat:          ErrCaveatViolation,
	famRequest:         ErrInvalidRequest,
	famNotFound:        ErrNotFound,
}

// sentinelCase is one exported sentinel: the families it matches, the Reason
// ReasonOf gives it ("" for none) and the status ErrorResponse answers it with.
type sentinelCase struct {
	err      error
	families []string
	reason   Reason
	status   int
}

// sentinelMatrix is every exported sentinel and how it reads. A sentinel
// added to the package and left out of it fails TestSentinelMatrixIsComplete.
var sentinelMatrix = map[string]sentinelCase{
	"ErrUnauthenticated":    {ErrUnauthenticated, []string{famUnauthenticated}, "", http.StatusUnauthorized},
	"ErrInvalidSignature":   {ErrInvalidSignature, []string{famUnauthenticated, famSignature}, ReasonInvalidSignature, http.StatusUnauthorized},
	"ErrExpired":            {ErrExpired, []string{famUnauthenticated}, ReasonExpired, http.StatusUnauthorized},
	"ErrNotYetValid":        {ErrNotYetValid, []string{famUnauthenticated}, ReasonNotYetValid, http.StatusUnauthorized},
	"ErrRevoked":            {ErrRevoked, []string{famUnauthenticated}, ReasonRevoked, http.StatusUnauthorized},
	"ErrAudienceMismatch":   {ErrAudienceMismatch, []string{famUnauthenticated}, ReasonAudienceMismatch, http.StatusUnauthorized},
	"ErrBiscuitAttenuation": {ErrBiscuitAttenuation, []string{famUnauthenticated, famSignature}, ReasonInvalidBiscuit, http.StatusUnauthorized},
	"ErrCopyCountersNotMetered": {
		ErrCopyCountersNotMetered, []string{famUnauthenticated, famSignature}, ReasonInvalidBiscuit, http.StatusUnauthorized,
	},
	"ErrUnknownKID":             {ErrUnknownKID, nil, "", http.StatusInternalServerError},
	"ErrProofOfPossession":      {ErrProofOfPossession, []string{famUnauthenticated, famProof}, "", http.StatusUnauthorized},
	"ErrDPoPRequired":           {ErrDPoPRequired, []string{famUnauthenticated, famProof}, ReasonProofRequired, http.StatusUnauthorized},
	"ErrDPoPInvalid":            {ErrDPoPInvalid, []string{famUnauthenticated, famProof}, ReasonProofInvalid, http.StatusUnauthorized},
	"ErrDPoPReplayed":           {ErrDPoPReplayed, []string{famUnauthenticated, famProof}, ReasonProofReplayed, http.StatusUnauthorized},
	"ErrDPoPNonceRequired":      {ErrDPoPNonceRequired, []string{famUnauthenticated, famProof}, ReasonProofInvalid, http.StatusUnauthorized},
	"ErrAuthSchemeMismatch":     {ErrAuthSchemeMismatch, []string{famUnauthenticated, famProof}, ReasonProofRequired, http.StatusUnauthorized},
	"ErrJWKSUnavailable":        {ErrJWKSUnavailable, nil, ReasonKeysUnavailable, http.StatusServiceUnavailable},
	"ErrMalformedAuthorization": {ErrMalformedAuthorization, nil, ReasonMalformedAuthorization, http.StatusBadRequest},

	"ErrCaveatViolation":        {ErrCaveatViolation, []string{famCaveat}, "", http.StatusForbidden},
	"ErrOpNotAllowed":           {ErrOpNotAllowed, []string{famCaveat}, ReasonOpNotAllowed, http.StatusForbidden},
	"ErrResourceNotAllowed":     {ErrResourceNotAllowed, []string{famCaveat}, ReasonResourceNotAllowed, http.StatusForbidden},
	"ErrSourceIPNotAllowed":     {ErrSourceIPNotAllowed, []string{famCaveat}, ReasonSourceNotAllowed, http.StatusForbidden},
	"ErrIdempotencyKeyRequired": {ErrIdempotencyKeyRequired, []string{famCaveat}, ReasonIdempotencyKeyRequired, http.StatusForbidden},
	"ErrTaintedReadNotAllowed":  {ErrTaintedReadNotAllowed, []string{famCaveat}, ReasonTaintedReadNotAllowed, http.StatusForbidden},
	"ErrBudgetExceeded":         {ErrBudgetExceeded, nil, ReasonBudgetExceeded, http.StatusForbidden},
	"ErrTenantBudgetExceeded":   {ErrTenantBudgetExceeded, nil, ReasonTenantBudgetExceeded, http.StatusForbidden},
	"ErrRequestLimitExceeded":   {ErrRequestLimitExceeded, nil, ReasonRequestLimitExceeded, http.StatusForbidden},

	"ErrInvalidRequest":      {ErrInvalidRequest, []string{famRequest}, "", http.StatusInternalServerError},
	"ErrInvalidCaveats":      {ErrInvalidCaveats, []string{famRequest}, "", http.StatusInternalServerError},
	"ErrEffectConflict":      {ErrEffectConflict, []string{famRequest}, "", http.StatusInternalServerError},
	"ErrExternalRefConflict": {ErrExternalRefConflict, []string{famRequest}, "", http.StatusInternalServerError},

	"ErrNotFound":             {ErrNotFound, []string{famNotFound}, "", http.StatusInternalServerError},
	"ErrChargeNotFound":       {ErrChargeNotFound, []string{famNotFound}, "", http.StatusInternalServerError},
	"ErrReservationNotFound":  {ErrReservationNotFound, []string{famNotFound}, "", http.StatusInternalServerError},
	"ErrUsageNotFound":        {ErrUsageNotFound, []string{famNotFound}, "", http.StatusInternalServerError},
	"ErrTenantBudgetNotFound": {ErrTenantBudgetNotFound, []string{famNotFound}, "", http.StatusInternalServerError},

	"ErrAlreadyExists":               {ErrAlreadyExists, nil, "", http.StatusInternalServerError},
	"ErrInvalidAmount":               {ErrInvalidAmount, nil, "", http.StatusInternalServerError},
	"ErrDelegationTooWide":           {ErrDelegationTooWide, nil, "", http.StatusInternalServerError},
	"ErrUnitCodeMismatch":            {ErrUnitCodeMismatch, nil, "", http.StatusInternalServerError},
	"ErrUnknownTenant":               {ErrUnknownTenant, nil, "", http.StatusInternalServerError},
	"ErrTenantDeleted":               {ErrTenantDeleted, nil, "", http.StatusInternalServerError},
	"ErrCursorRepeated":              {ErrCursorRepeated, nil, "", http.StatusInternalServerError},
	"ErrRefundExceedsCharge":         {ErrRefundExceedsCharge, nil, "", http.StatusInternalServerError},
	"ErrTenantBudgetVersionMismatch": {ErrTenantBudgetVersionMismatch, nil, "", http.StatusInternalServerError},
}

// Each sentinel matches exactly the families the matrix names, has the
// Reason it names and is answered with the status it names.
func TestSentinelMatrix(t *testing.T) {
	v := &StandardVerifier{}
	for name, c := range sentinelMatrix {
		for family, target := range families {
			want := slices.Contains(c.families, family)
			if got := errors.Is(c.err, target); got != want {
				t.Errorf("errors.Is(%s, Err%s) = %v, want %v", name, family, got, want)
			}
		}
		got, ok := ReasonOf(c.err)
		if c.reason == "" && ok {
			t.Errorf("ReasonOf(%s) = %q, want none", name, got)
		}
		if c.reason != "" && got != c.reason {
			t.Errorf("ReasonOf(%s) = %q, want %q", name, got, c.reason)
		}
		if status := v.ErrorResponse(context.Background(), c.err, AuthSchemeBearer).Status; status != c.status {
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
