package limes

import (
	"context"
	"encoding/json/v2"
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

	"github.com/oleg-tkachuk/limes/dpop"
	"github.com/oleg-tkachuk/limes/jwks"
)

// sentinelPrefix opens the name of every exported sentinel.
const sentinelPrefix = "Err"

// codePattern is what a code looks like: lower snake case.
var codePattern = regexp.MustCompile(`^[a-z]+(_[a-z]+)*$`)

// exportedSentinels are the names of the exported Err variables a
// package's non-test files declare, sorted.
func exportedSentinels(t *testing.T, dir string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
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
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.VAR {
				continue
			}
			for _, spec := range gen.Specs {
				for _, n := range spec.(*ast.ValueSpec).Names {
					if strings.HasPrefix(n.Name, sentinelPrefix) && n.IsExported() {
						names = append(names, filepath.Base(dir)+"."+n.Name)
					}
				}
			}
		}
	}
	sort.Strings(names)
	return names
}

// sentinels are the exported sentinels of limes, dpop and jwks, by the name
// their source declares them under.
var sentinels = map[string]error{
	"..ErrNoCredentials": ErrNoCredentials, "..ErrMalformedAuthorization": ErrMalformedAuthorization,
	"..ErrAuthSchemeMismatch": ErrAuthSchemeMismatch, "..ErrNoRevocationLineage": ErrNoRevocationLineage,
	"..ErrOpNotAllowed": ErrOpNotAllowed, "..ErrResourceNotAllowed": ErrResourceNotAllowed,
	"..ErrSourceIPNotAllowed": ErrSourceIPNotAllowed, "..ErrIdempotencyKeyRequired": ErrIdempotencyKeyRequired,
	"..ErrTaintedReadNotAllowed": ErrTaintedReadNotAllowed, "..ErrEffectConflict": ErrEffectConflict,
	"..ErrUnauthenticated": ErrUnauthenticated, "..ErrInvalidToken": ErrInvalidToken,
	"..ErrProofOfPossession": ErrProofOfPossession, "..ErrExpired": ErrExpired, "..ErrNotYetValid": ErrNotYetValid,
	"..ErrRevoked": ErrRevoked, "..ErrTenantErased": ErrTenantErased, "..ErrDelegationTooDeep": ErrDelegationTooDeep,
	"..ErrAudienceMismatch": ErrAudienceMismatch, "..ErrCaveatViolation": ErrCaveatViolation,
	"..ErrBudgetExceeded": ErrBudgetExceeded, "..ErrInvalidAmount": ErrInvalidAmount,
	"..ErrCursorMismatch": ErrCursorMismatch, "..ErrRequestLimitExceeded": ErrRequestLimitExceeded,
	"..ErrTenantBudgetExceeded": ErrTenantBudgetExceeded, "..ErrDelegationTooWide": ErrDelegationTooWide,
	"..ErrUnitCodeMismatch": ErrUnitCodeMismatch, "..ErrInvalidRequest": ErrInvalidRequest,
	"..ErrUnknownTenant": ErrUnknownTenant, "..ErrTenantDeleted": ErrTenantDeleted,
	"..ErrScopeMismatch": ErrScopeMismatch, "..ErrProofUnsupported": ErrProofUnsupported,
	"..ErrCursorRepeated": ErrCursorRepeated, "..ErrCopyCountersNotMetered": ErrCopyCountersNotMetered,
	"..ErrBiscuitAttenuation": ErrBiscuitAttenuation, "..ErrNotFound": ErrNotFound, "..ErrConflict": ErrConflict,
	"..ErrAlreadyExists": ErrAlreadyExists, "..ErrInvalidCaveats": ErrInvalidCaveats,
	"..ErrInvalidConfig": ErrInvalidConfig, "..ErrInternal": ErrInternal,
	"dpop.ErrRequired": dpop.ErrRequired, "dpop.ErrInvalid": dpop.ErrInvalid, "dpop.ErrReplayed": dpop.ErrReplayed,
	"dpop.ErrNonceRequired": dpop.ErrNonceRequired,
	"jwks.ErrUnavailable":   jwks.ErrUnavailable, "jwks.ErrUnknownKID": jwks.ErrUnknownKID,
}

// Every exported sentinel has a code of its own: one its source declares
// and no rule names fails here, as does a rule of a sentinel gone.
func TestEverySentinelHasACode(t *testing.T) {
	var declared []string
	for _, dir := range []string{".", "dpop", "jwks"} {
		declared = append(declared, exportedSentinels(t, dir)...)
	}
	var listed []string
	for name := range sentinels {
		listed = append(listed, name)
	}
	sort.Strings(listed)
	if !slices.Equal(declared, listed) {
		t.Fatalf("the sources declare %q; the test lists %q", declared, listed)
	}
	for name, err := range sentinels {
		r, ok := codeRuleFor(err)
		if !ok || !errors.Is(r.sentinel, err) {
			t.Errorf("%s: matched by %v's rule, want a rule of its own", name, r.sentinel)
		}
	}
	if len(codeRules) != len(sentinels) {
		t.Errorf("%d code rules for %d sentinels", len(codeRules), len(sentinels))
	}
}

// Each code is one sentinel's, in snake case; every Reason is a code; and a
// rule names a status exactly when no refusal rule answers its sentinel.
func TestCodesAreWhole(t *testing.T) {
	seen := map[Code]bool{}
	for _, r := range codeRules {
		if seen[r.code] {
			t.Errorf("code %q names two sentinels", r.code)
		}
		seen[r.code] = true
		if !codePattern.MatchString(string(r.code)) {
			t.Errorf("code %q is not snake case", r.code)
		}
		_, refused := ruleFor(r.sentinel)
		if refused == (r.status != 0) || (r.status != 0) == (r.title == "") {
			t.Errorf("%q: status %d, title %q, refusal %v; want a status and title exactly when no refusal answers", r.code, r.status, r.title, refused)
		}
	}
	for _, reason := range Reasons() {
		if !slices.Contains(Codes(), Code(reason)) {
			t.Errorf("Reason %q is no Code", reason)
		}
	}
	if len(Codes()) != len(codeRules) {
		t.Errorf("Codes() lists %d, want %d", len(Codes()), len(codeRules))
	}
}

// A refusal's problem is the body ErrorResponse writes, code and all; any
// other error of limes's has its own status and a type ending in its code.
func TestProblemOf(t *testing.T) {
	v := &Verifier{}
	for name, err := range sentinels {
		if errors.Is(err, dpop.ErrNonceRequired) {
			continue // ErrorResponse adds a nonce, which this verifier has none of
		}
		p, ok := ProblemOf(fmt.Errorf("context: %w", err))
		if !ok {
			t.Errorf("%s: ProblemOf reports no problem", name)
			continue
		}
		code, _ := CodeOf(err)
		if p.Code != code || p.Detail != "" || p.Status == 0 || p.Title == "" {
			t.Errorf("%s: problem %+v; want code %q, a status and a title, no detail", name, p, code)
		}
		if _, refused := ruleFor(err); refused {
			want := v.ErrorResponse(context.Background(), err, AuthSchemeBearer).Problem
			if p.Type != want.Type || p.Title != want.Title || p.Status != want.Status || p.Reason != want.Reason || p.Code != want.Code {
				t.Errorf("%s: ProblemOf %+v, ErrorResponse %+v", name, p, want)
			}
		} else if p.Type != ProblemTypeURI(string(code)) {
			t.Errorf("%s: type %q, want it to end in its code", name, p.Type)
		}
	}
	for name, tc := range map[string]struct {
		err    error
		status int
		code   Code
	}{
		"not found":        {fmt.Errorf("get: %w", ErrNotFound), http.StatusNotFound, CodeNotFound},
		"an erased tenant": {ErrTenantErased, http.StatusUnauthorized, CodeTenantErased},
		"invalid caveats":  {invalidRequest("x"), http.StatusBadRequest, CodeInvalidRequest},
		"a deleted tenant": {ErrTenantDeleted, http.StatusGone, CodeTenantDeleted},
		"a scope mismatch": {ErrScopeMismatch, http.StatusInternalServerError, CodeScopeMismatch},
		"already exists":   {ErrAlreadyExists, http.StatusConflict, CodeAlreadyExists},
		"a revoked parent": {fmt.Errorf("delegate: %w", ErrRevoked), http.StatusUnauthorized, CodeRevoked},
	} {
		p, ok := ProblemOf(tc.err)
		if !ok || p.Status != tc.status || p.Code != tc.code {
			t.Errorf("%s: ProblemOf = %+v, %v; want status %d, code %q", name, p, ok, tc.status, tc.code)
		}
	}
	if p, ok := ProblemOf(ErrTenantErased); !ok || p.Reason != ReasonRevoked {
		t.Errorf("an erased tenant's reason = %q, want %q: Reason stays the wider one", p.Reason, ReasonRevoked)
	}
	for name, err := range map[string]error{"nil": nil, "a store's own": errors.New("connection reset")} {
		if p, ok := ProblemOf(err); ok {
			t.Errorf("%s: ProblemOf = %+v, want none", name, p)
		}
		if c, ok := CodeOf(err); ok {
			t.Errorf("%s: CodeOf = %q, want none", name, c)
		}
	}
}

// The body names its code beside its reason.
func TestProblemWritesItsCode(t *testing.T) {
	p, _ := ProblemOf(ErrNotFound)
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil || body["code"] != string(CodeNotFound) || body["status"] != float64(http.StatusNotFound) {
		t.Errorf("body = %s, %v", raw, err)
	}
	if _, ok := body["detail"]; ok {
		t.Errorf("body = %s: a detail", raw)
	}
}

// errorsDoc is the reference of every code.
const errorsDoc = "docs/errors.md"

// docRow reads a row of errorsDoc's tables: its code and HTTP status.
var docRow = regexp.MustCompile("(?m)^\\| `([a-z_]+)` \\| ([0-9]{3}) \\|")

// DocumentedCodes are the codes errorsDoc's tables list, with their status.
func documentedCodes(t *testing.T, path string) map[string]int {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]int{}
	for _, m := range docRow.FindAllStringSubmatch(string(raw), -1) {
		status, err := strconv.Atoi(m[2])
		if err != nil {
			t.Fatal(err)
		}
		out[m[1]] = status
	}
	return out
}

// docs/errors.md lists every code with the status ProblemOf answers it with.
func TestErrorsDocListsEveryCode(t *testing.T) {
	doc := documentedCodes(t, errorsDoc)
	for _, r := range codeRules {
		p, _ := ProblemOf(r.sentinel)
		if got, ok := doc[string(r.code)]; !ok || got != p.Status {
			t.Errorf("%s lists %q with status %d (listed %v); ProblemOf answers %d", errorsDoc, r.code, got, ok, p.Status)
		}
	}
}

// ProblemFor answers every error: limes's as ProblemOf does, any other as
// internal.
func TestProblemFor(t *testing.T) {
	if p := ProblemFor(ErrNotFound); p.Code != CodeNotFound || p.Status != http.StatusNotFound {
		t.Errorf("ProblemFor(ErrNotFound) = %+v", p)
	}
	for _, err := range []error{errors.New("connection reset"), nil} {
		if p := ProblemFor(err); p.Code != CodeInternal || p.Status != http.StatusInternalServerError || p.Title == "" {
			t.Errorf("ProblemFor(%v) = %+v, want internal", err, p)
		}
	}
}
