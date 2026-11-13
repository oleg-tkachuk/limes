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
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/oleg-tkachuk/limes/dpop"
	"github.com/oleg-tkachuk/limes/internal/errcode"
	"github.com/oleg-tkachuk/limes/internal/link"
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
	"..ErrRevoked": ErrRevoked, "..ErrUnrecorded": ErrUnrecorded, "..ErrTenantErased": ErrTenantErased, "..ErrDelegationTooDeep": ErrDelegationTooDeep,
	"..ErrAudienceMismatch": ErrAudienceMismatch, "..ErrCaveatViolation": ErrCaveatViolation,
	"..ErrBudgetExceeded": ErrBudgetExceeded, "..ErrInvalidAmount": ErrInvalidAmount,
	"..ErrCursorMismatch": ErrCursorMismatch, "..ErrRequestLimitExceeded": ErrRequestLimitExceeded,
	"..ErrTenantBudgetExceeded": ErrTenantBudgetExceeded, "..ErrDelegationTooWide": ErrDelegationTooWide, "..ErrDelegationDepthExceeded": ErrDelegationDepthExceeded,
	"..ErrUnitCodeMismatch": ErrUnitCodeMismatch, "..ErrInvalidRequest": ErrInvalidRequest,
	"..ErrUnknownTenant": ErrUnknownTenant, "..ErrTenantDeleted": ErrTenantDeleted, "..ErrTenantGone": ErrTenantGone,
	"..ErrScopeMismatch": ErrScopeMismatch, "..ErrProofUnsupported": ErrProofUnsupported,
	"..ErrCursorRepeated": ErrCursorRepeated, "..ErrCopyCountersNotMetered": ErrCopyCountersNotMetered,
	"..ErrStoreUnavailable": ErrStoreUnavailable, "..ErrStoreTimeout": ErrStoreTimeout,
	"..ErrBiscuitAttenuation": ErrBiscuitAttenuation, "..ErrNotFound": ErrNotFound, "..ErrConflict": ErrConflict,
	"..ErrAlreadyExists": ErrAlreadyExists, "..ErrInvalidCaveats": ErrInvalidCaveats,
	"..ErrInvalidConfig": ErrInvalidConfig, "..ErrInternal": ErrInternal, "..ErrRevocationUnavailable": ErrRevocationUnavailable,
	"dpop.ErrRequired": dpop.ErrRequired, "dpop.ErrInvalid": dpop.ErrInvalid, "dpop.ErrReplayed": dpop.ErrReplayed,
	"dpop.ErrNonceRequired": dpop.ErrNonceRequired, "dpop.ErrReplayQuota": dpop.ErrReplayQuota,
	"jwks.ErrUnavailable": jwks.ErrUnavailable, "jwks.ErrUnknownKID": jwks.ErrUnknownKID,
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
	for _, r := range allCodeRules() {
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
	if len(Codes()) != len(allCodeRules()) {
		t.Errorf("Codes() lists %d, want %d", len(Codes()), len(allCodeRules()))
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
		category, _ := CategoryOf(err)
		if p.Code != code || p.Category != category || p.Detail != "" || p.Status == 0 || p.Title == "" {
			t.Errorf("%s: problem %+v; want code %q, category %q, a status and a title, no detail", name, p, code, category)
		}
		if _, refused := ruleFor(err); refused {
			want := v.ErrorResponse(context.Background(), err, AuthSchemeBearer).Problem
			if p.Type != want.Type || p.Title != want.Title || p.Status != want.Status || p.Reason != want.Reason || p.Code != want.Code || p.Category != want.Category {
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
	if body["category"] != string(CategoryNotFound) {
		t.Errorf("body = %s: want category %q", raw, CategoryNotFound)
	}
	if _, ok := body["detail"]; ok {
		t.Errorf("body = %s: a detail", raw)
	}
}

// ProblemOfCode answers a code as ProblemOf answers an error with it, and
// false for a code that is none of limes's.
func TestProblemOfCode(t *testing.T) {
	for _, r := range codeRules {
		want, _ := ProblemOf(r.sentinel)
		if got, ok := ProblemOfCode(r.code); !ok || !reflect.DeepEqual(got, want) {
			t.Errorf("ProblemOfCode(%q) = %+v, %v; ProblemOf answers %+v", r.code, got, ok, want)
		}
	}
	for _, code := range Codes() {
		p, ok := ProblemOfCode(code)
		category, _ := CategoryOfCode(code)
		if !ok || p.Code != code || p.Category != category || p.Status == 0 || p.Title == "" {
			t.Errorf("ProblemOfCode(%q) = %+v, %v; want its code, category %q, a status and a title", code, p, ok, category)
		}
	}
	if p, ok := ProblemOfCode("no_such_code"); ok {
		t.Errorf("ProblemOfCode(no_such_code) = %+v, want none", p)
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
		if p := ProblemFor(err); p.Code != CodeInternal || p.Category != CategoryInternal || p.Status != http.StatusInternalServerError || p.Title == "" {
			t.Errorf("ProblemFor(%v) = %+v, want internal", err, p)
		}
	}
}

// coded is an error of another package naming its own code, as the meter's
// sentinels do, in the family family.
type coded struct {
	code   Code
	family error
}

func (c coded) Error() string          { return string(c.code) }
func (c coded) LimesErrorCode() string { return string(c.code) }
func (c coded) Is(target error) bool   { return errors.Is(c.family, target) }

// An error naming its own code is answered by that code's rule, ahead of
// the family it belongs to; one naming a code limes does not know is
// answered by its family, as any other error is.
func TestAnErrorNamingItsCode(t *testing.T) {
	for name, tc := range map[string]struct {
		err      error
		code     Code
		category Category
		status   int
	}{
		"a code of the meter's":      {fmt.Errorf("wrapped: %w", coded{CodeChargeNotFound, ErrNotFound}), CodeChargeNotFound, CategoryNotFound, http.StatusNotFound},
		"a status of its own":        {coded{CodeLeaseNested, ErrConflict}, CodeLeaseNested, CategoryPrecondition, http.StatusConflict},
		"a refusal of the meter's":   {coded{CodeLeaseExhausted, ErrBudgetExceeded}, CodeLeaseExhausted, CategoryExhausted, http.StatusForbidden},
		"a code limes does not know": {coded{"no_such_code", ErrNotFound}, CodeNotFound, CategoryNotFound, http.StatusNotFound},
	} {
		if got, ok := CodeOf(tc.err); !ok || got != tc.code {
			t.Errorf("%s: CodeOf = %q, %v; want %q", name, got, ok, tc.code)
		}
		if got, ok := CategoryOf(tc.err); !ok || got != tc.category {
			t.Errorf("%s: CategoryOf = %q, %v; want %q", name, got, ok, tc.category)
		}
		if p, ok := ProblemOf(tc.err); !ok || p.Code != tc.code || p.Status != tc.status {
			t.Errorf("%s: ProblemOf = %+v, %v; want code %q, status %d", name, p, ok, tc.code, tc.status)
		}
	}
}

// The codes of each optional component are some of Codes, in its order,
// apart from each other and from limes's own.
func TestComponentCodes(t *testing.T) {
	standInExchange(t)
	for name, tc := range map[string]struct {
		codes func() []string
		has   Code
	}{
		"exchange": {link.ExchangeCodes, CodeInvalidGrant},
		"meter":    {link.MeterCodes, CodeLeaseNested},
	} {
		var codes []Code
		for _, c := range tc.codes() {
			codes = append(codes, Code(c))
		}
		if !slices.Contains(codes, tc.has) {
			t.Errorf("%s's codes %q lack %q", name, codes, tc.has)
		}
		var inOrder []Code
		for _, c := range Codes() {
			if slices.Contains(codes, c) {
				inOrder = append(inOrder, c)
			}
		}
		if !slices.Equal(inOrder, codes) {
			t.Errorf("%s's codes %q; Codes() lists them as %q", name, codes, inOrder)
		}
		for _, c := range []Code{CodeRevoked, CodeInvalidRequest} {
			if slices.Contains(codes, c) {
				t.Errorf("%s's codes hold %q, one of limes's own", name, c)
			}
		}
	}
	for _, c := range link.ExchangeCodes() {
		if slices.Contains(link.MeterCodes(), c) {
			t.Errorf("%q is both the exchange's and the meter's", c)
		}
	}
}

// answered is an error of a component that carries its whole answer, as
// the exchange's sentinels do.
type answered struct {
	code     Code
	status   int
	title    string
	category Category
}

func (a answered) Error() string              { return string(a.code) }
func (a answered) LimesErrorCode() string     { return string(a.code) }
func (a answered) LimesErrorStatus() int      { return a.status }
func (a answered) LimesErrorTitle() string    { return a.title }
func (a answered) LimesErrorCategory() string { return string(a.category) }

// The exchange's codes as the exchange registers them, which a test of this
// module stands in: limes holds no table of them.
var (
	exchangeGrant  = answered{CodeInvalidGrant, http.StatusBadRequest, "The subject token was refused", CategoryUnauthenticated}
	exchangeServer = answered{CodeServerError, http.StatusInternalServerError, "The server could not decide on the request", CategoryInternal}
	// exchangeRequest names a code of limes's own, whose rule answers it.
	exchangeRequest = answered{CodeInvalidRequest, http.StatusBadRequest, "The request is malformed", CategoryInvalid}
)

// standInExchange registers the exchange's stand-ins for the test, and
// nothing after it.
func standInExchange(t *testing.T) {
	t.Helper()
	saved := registrations
	registrations = func() []errcode.Registration {
		return []errcode.Registration{
			{Component: errcode.ComponentExchange, Err: exchangeGrant},
			{Component: errcode.ComponentExchange, Err: exchangeRequest},
			{Component: errcode.ComponentExchange, Err: exchangeServer},
			{Component: errcode.ComponentExchange, Err: exchangeGrant},
		}
	}
	t.Cleanup(func() { registrations = saved })
}

// An error carrying the answer its component registered is answered by it —
// through a wrap, and ahead of a limes error it wraps — though limes holds
// no rule of its code; a code limes holds a rule of is answered by that
// rule.
func TestAnErrorCarryingItsAnswer(t *testing.T) {
	standInExchange(t)
	for name, tc := range map[string]struct {
		err  error
		want Problem
	}{
		"its own answer": {fmt.Errorf("exchanging: %w: %w", exchangeServer, ErrRevoked),
			Problem{Type: ProblemTypeURI(string(CodeServerError)), Title: exchangeServer.title, Status: http.StatusInternalServerError, Code: CodeServerError, Category: CategoryInternal}},
		"a code of limes's": {exchangeRequest,
			Problem{Type: ProblemTypeURI(string(CodeInvalidRequest)), Title: "The request is invalid", Status: http.StatusBadRequest, Code: CodeInvalidRequest, Category: CategoryInvalid}},
	} {
		if got, ok := ProblemOf(tc.err); !ok || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: ProblemOf = %+v, %v; want %+v", name, got, ok, tc.want)
		}
		if got, ok := CodeOf(tc.err); !ok || got != tc.want.Code {
			t.Errorf("%s: CodeOf = %q, %v; want %q", name, got, ok, tc.want.Code)
		}
		if got, ok := CategoryOf(tc.err); !ok || got != tc.want.Category {
			t.Errorf("%s: CategoryOf = %q, %v; want %q", name, got, ok, tc.want.Category)
		}
	}
}

// A registered code is answered by code alone, listed once by Codes and as
// its component's, while one limes holds a rule of stays limes's own.
func TestARegisteredCodeIsAnsweredByCode(t *testing.T) {
	if _, ok := ProblemOfCode(CodeServerError); ok {
		t.Fatal("ProblemOfCode(server_error) answers with nothing registered: limes holds a rule of the exchange's code")
	}
	standInExchange(t)
	want, _ := ProblemOf(exchangeServer)
	if got, ok := ProblemOfCode(CodeServerError); !ok || !reflect.DeepEqual(got, want) {
		t.Errorf("ProblemOfCode(server_error) = %+v, %v; want %+v, as ProblemOf answers", got, ok, want)
	}
	if got, ok := CategoryOfCode(CodeInvalidGrant); !ok || got != CategoryUnauthenticated {
		t.Errorf("CategoryOfCode(invalid_grant) = %q, %v", got, ok)
	}
	if _, ok := ReasonOfCode(CodeInvalidGrant); ok {
		t.Error("ReasonOfCode(invalid_grant) has a reason: a registered code is no refusal of a presented capability")
	}
	counted := map[Code]int{}
	for _, c := range Codes() {
		counted[c]++
	}
	for _, c := range []Code{CodeInvalidGrant, CodeServerError, CodeInvalidRequest} {
		if counted[c] != 1 {
			t.Errorf("Codes() lists %q %d times, want once", c, counted[c])
		}
	}
	if got := link.ExchangeCodes(); !slices.Equal(got, []string{string(CodeInvalidGrant), string(CodeServerError)}) {
		t.Errorf("the exchange's codes = %q; want its own two, not limes's invalid_request", got)
	}
}

// foreignAnswer is an error of a consumer's, or of a dependency's, that
// happens to carry a limes answer it never registered.
type foreignAnswer struct{}

// The answer foreignAnswer carries.
const (
	foreignCode   = "foreign_code"
	foreignStatus = http.StatusTeapot
	foreignTitle  = "A foreign title"
)

func (foreignAnswer) Error() string              { return "a foreign error" }
func (foreignAnswer) LimesErrorCode() string     { return foreignCode }
func (foreignAnswer) LimesErrorStatus() int      { return foreignStatus }
func (foreignAnswer) LimesErrorTitle() string    { return foreignTitle }
func (foreignAnswer) LimesErrorCategory() string { return string(CategoryInvalid) }

// An error that names a code no table holds — a consumer's, a
// dependency's — steers no answer: alone it is no error of limes's, as
// CodeOf says of a consumer's own, and beside a limes sentinel it is that
// sentinel's code, status and category that answer.
func TestAnUnregisteredAnswerSteersNothing(t *testing.T) {
	if code, ok := CodeOf(foreignAnswer{}); ok {
		t.Errorf("CodeOf(an unregistered answer) = %q, true; want none", code)
	}
	if p, ok := ProblemOf(foreignAnswer{}); ok {
		t.Errorf("ProblemOf(an unregistered answer) = %+v, true; want none", p)
	}
	joined := errors.Join(ErrRevoked, foreignAnswer{})
	if code, ok := CodeOf(joined); !ok || code != CodeRevoked {
		t.Errorf("CodeOf(ErrRevoked joined with an unregistered answer) = %q, %v; want %q", code, ok, CodeRevoked)
	}
	want, _ := ProblemOf(ErrRevoked)
	if p, ok := ProblemOf(joined); !ok || p.Status != want.Status || p.Title != want.Title || p.Code != want.Code || p.Category != want.Category {
		t.Errorf("ProblemOf(ErrRevoked joined with an unregistered answer) = %+v, %v; want %+v", p, ok, want)
	}
}

// A code a table holds is found past an unregistered one before it in the
// error's tree.
func TestARegisteredCodePastAnUnregisteredOne(t *testing.T) {
	registered := coded{code: CodeLeaseNotFound}
	joined := errors.Join(foreignAnswer{}, fmt.Errorf("wrapped: %w", registered))
	want, _ := CodeOf(registered)
	if code, ok := CodeOf(joined); !ok || code != want {
		t.Errorf("CodeOf(an unregistered answer, then %v) = %q, %v; want %q", registered, code, ok, want)
	}
}

// An error naming a refusal's code outside that refusal's family is
// answered as the refusal its code names, never with no status: the problem
// is the one ProblemOfCode gives the code.
func TestACodeNamedOutsideItsFamily(t *testing.T) {
	outside := errors.New("no family of limes's")
	for _, code := range Codes() {
		want, _ := ProblemOfCode(code)
		got, ok := ProblemOf(coded{code, outside})
		same := got.Type == want.Type && got.Title == want.Title && got.Status == want.Status &&
			got.Reason == want.Reason && got.Code == want.Code && got.Category == want.Category
		if !ok || !same || got.Status == 0 {
			t.Errorf("an error naming %q outside its family: ProblemOf = %+v, %v; want %+v", code, got, ok, want)
		}
	}
}
