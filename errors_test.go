package limes

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/oleg-tkachuk/limes/dpop"
	"github.com/oleg-tkachuk/limes/jwks"
)

// The families a sentinel may belong to, as errors.Is reads them.
const (
	famUnauthenticated = "Unauthenticated"
	famToken           = "InvalidToken"
	famProof           = "ProofOfPossession"
	famCaveat          = "CaveatViolation"
	famRequest         = "InvalidRequest"
	famNotFound        = "NotFound"
)

var families = map[string]error{
	famUnauthenticated: ErrUnauthenticated,
	famToken:           ErrInvalidToken,
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

// sentinelMatrix is every exported sentinel and how it reads — a dpop
// refusal as the verifier returns it, wrapped in its family. A sentinel
// added to the package and left out of it fails TestSentinelMatrixIsComplete.
var sentinelMatrix = map[string]sentinelCase{
	"ErrUnauthenticated":    {ErrUnauthenticated, []string{famUnauthenticated}, "", http.StatusUnauthorized},
	"ErrInvalidToken":       {ErrInvalidToken, []string{famUnauthenticated, famToken}, ReasonInvalidToken, http.StatusUnauthorized},
	"ErrExpired":            {ErrExpired, []string{famUnauthenticated}, ReasonExpired, http.StatusUnauthorized},
	"ErrNotYetValid":        {ErrNotYetValid, []string{famUnauthenticated}, ReasonNotYetValid, http.StatusUnauthorized},
	"ErrRevoked":            {ErrRevoked, []string{famUnauthenticated}, ReasonRevoked, http.StatusUnauthorized},
	"ErrAudienceMismatch":   {ErrAudienceMismatch, []string{famUnauthenticated}, ReasonAudienceMismatch, http.StatusUnauthorized},
	"ErrBiscuitAttenuation": {ErrBiscuitAttenuation, []string{famUnauthenticated, famToken}, ReasonInvalidBiscuit, http.StatusUnauthorized},
	"ErrCopyCountersNotMetered": {
		ErrCopyCountersNotMetered, []string{famUnauthenticated, famToken}, ReasonInvalidBiscuit, http.StatusUnauthorized,
	},
	"jwks.ErrUnknownKID":        {jwks.ErrUnknownKID, nil, "", http.StatusInternalServerError},
	"ErrProofOfPossession":      {ErrProofOfPossession, []string{famUnauthenticated, famProof}, "", http.StatusUnauthorized},
	"dpop.ErrRequired":          {proofRefusal(dpop.ErrRequired), []string{famUnauthenticated, famProof}, ReasonProofRequired, http.StatusUnauthorized},
	"dpop.ErrInvalid":           {proofRefusal(dpop.ErrInvalid), []string{famUnauthenticated, famProof}, ReasonProofInvalid, http.StatusUnauthorized},
	"dpop.ErrReplayed":          {proofRefusal(dpop.ErrReplayed), []string{famUnauthenticated, famProof}, ReasonProofReplayed, http.StatusUnauthorized},
	"dpop.ErrNonceRequired":     {proofRefusal(dpop.ErrNonceRequired), []string{famUnauthenticated, famProof}, ReasonProofInvalid, http.StatusUnauthorized},
	"ErrAuthSchemeMismatch":     {ErrAuthSchemeMismatch, []string{famUnauthenticated, famProof}, ReasonProofRequired, http.StatusUnauthorized},
	"dpop.ErrNoReplayCache":     {dpop.ErrNoReplayCache, nil, "", http.StatusInternalServerError},
	"jwks.ErrUnavailable":       {jwks.ErrUnavailable, nil, ReasonKeysUnavailable, http.StatusServiceUnavailable},
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

// sentinelPackages are the directories whose exported sentinels the matrix
// must hold, and the qualifier their names take in it.
var sentinelPackages = map[string]string{".": "", "jwks": "jwks.", "dpop": "dpop."}

// Every exported sentinel of the module's packages is in the matrix, and so
// is every sentinel the refusal table names.
func TestSentinelMatrixIsComplete(t *testing.T) {
	for _, rule := range refusalRules {
		found := false
		for _, c := range sentinelMatrix {
			found = found || errors.Is(c.err, rule.sentinel)
		}
		if !found {
			t.Errorf("%v is in refusalRules but not in sentinelMatrix", rule.sentinel)
		}
	}
	for dir, qualifier := range sentinelPackages {
		checkPackageSentinels(t, dir, qualifier)
	}
}

func checkPackageSentinels(t *testing.T, dir, qualifier string) {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
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
						if _, ok := sentinelMatrix[qualifier+ident.Name]; !ok {
							t.Errorf("%s%s (%s) is not in sentinelMatrix", qualifier, ident.Name, file)
						}
					}
				}
			}
		}
	}
}

// failingResolver answers every kid with err.
type failingResolver struct{ err error }

func (r failingResolver) PublicKey(context.Context, string) (ed25519.PublicKey, error) {
	return nil, r.err
}

// Only an unknown kid is the token's fault. A resolver that cannot answer —
// the issuer's key set out of reach, a store down — is the server's, and the
// verifier says so rather than calling the token invalid.
func TestVerifyTellsAnUnknownKidFromAResolverOutage(t *testing.T) {
	issuer, configured, _, _ := buildIssuerVerifier(t)
	_, token := issueFor(t, issuer)
	outage := errors.New("key store down")
	for name, tc := range map[string]struct {
		resolverErr  error
		unauthorised bool
		status       int
	}{
		"unknown kid":         {jwks.ErrUnknownKID, true, http.StatusUnauthorized},
		"keys unavailable":    {fmt.Errorf("fetch: %w", jwks.ErrUnavailable), false, http.StatusServiceUnavailable},
		"the resolver failed": {outage, false, http.StatusInternalServerError},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := configured.cfg
			cfg.Keys = failingResolver{tc.resolverErr}
			v, err := NewStandardVerifier(cfg)
			if err != nil {
				t.Fatal(err)
			}
			_, err = v.Verify(context.Background(), VerifyRequest{Token: token, Audience: audienceData})
			if !errors.Is(err, tc.resolverErr) {
				t.Errorf("err = %v, want it to keep %v", err, tc.resolverErr)
			}
			if got := errors.Is(err, ErrUnauthenticated); got != tc.unauthorised {
				t.Errorf("errors.Is(err, ErrUnauthenticated) = %v, want %v", got, tc.unauthorised)
			}
			if status := v.ErrorResponse(context.Background(), err, AuthSchemeBearer).Status; status != tc.status {
				t.Errorf("ErrorResponse status = %d, want %d", status, tc.status)
			}
		})
	}
}

// failingReplay is a replay cache whose store is down.
type failingReplay struct{ err error }

func (r failingReplay) Seen(context.Context, dpop.ReplayEntry) (bool, error) { return false, r.err }

// failingNonces are nonces whose store is down.
type failingNonces struct{ err error }

func (n failingNonces) Nonce(context.Context) (string, error)       { return "", n.err }
func (n failingNonces) Valid(context.Context, string) (bool, error) { return false, n.err }

// A replay cache or nonce store that cannot answer is the server's fault:
// the proof is refused with its error, as an outage rather than a refusal of
// the credentials.
func TestVerifyTellsAProofRefusalFromAProofStoreOutage(t *testing.T) {
	issuer, configured, _, _ := buildIssuerVerifier(t)
	key := dpopKeys(t)["Ed25519"]
	token := issueBound(t, issuer, key)
	outage := errors.New("store down")
	for name, d := range map[string]*dpop.Verifier{
		"replay cache": {Replay: failingReplay{outage}},
		"nonces":       {Replay: dpop.NewMemoryReplayCache(0), Nonces: failingNonces{outage}},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := configured.cfg
			cfg.DPoP = d
			v, err := NewStandardVerifier(cfg)
			if err != nil {
				t.Fatal(err)
			}
			_, err = v.Verify(context.Background(), withProof(t, key, token, audienceData, time.Now()))
			if !errors.Is(err, outage) || errors.Is(err, ErrUnauthenticated) {
				t.Errorf("err = %v, want the outage and not ErrUnauthenticated", err)
			}
			if status := v.ErrorResponse(context.Background(), err, AuthSchemeDPoP).Status; status != http.StatusInternalServerError {
				t.Errorf("ErrorResponse status = %d, want 500", status)
			}
		})
	}
}
