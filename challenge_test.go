package limes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/oleg-tkachuk/limes/dpop"
	"github.com/oleg-tkachuk/limes/internal/httpx"
	"github.com/oleg-tkachuk/limes/jwks"
)

func TestErrorResponse(t *testing.T) {
	_, v, _, _ := buildIssuerVerifier(t) // DPoP configured
	nonces, err := dpop.NewHMACNonces(dpop.HMACNoncesConfig{Lifetime: time.Minute, Keys: [][]byte{nonceKey(1)}})
	if err != nil {
		t.Fatal(err)
	}
	cfg := v.cfg
	cfg.DPoP = newVerifier(t, dpop.VerifierConfig{Replay: newReplay(t, dpop.MemoryReplayCacheConfig{}), Nonces: nonces})
	withNonces, err := NewVerifier(cfg)
	if err != nil {
		t.Fatal(err)
	}
	algs := `algs="Ed25519 ES256"`
	cases := []struct {
		name       string
		err        error
		scheme     AuthScheme
		status     int
		challenges []string // a substring of each challenge, in order
		nonce      bool
	}{
		{"malformed header", fmt.Errorf("x: %w", ErrMalformedAuthorization), AuthSchemeUnspecified, http.StatusBadRequest,
			[]string{`Bearer error="invalid_request"`, "DPoP " + algs}, false},
		// RFC 6750 §3.1: no credentials, a challenge with no error code.
		{"no credentials", ErrNoCredentials, AuthSchemeUnspecified, http.StatusUnauthorized,
			[]string{"Bearer", "DPoP " + algs}, false},
		{"expired bearer", ErrExpired, AuthSchemeBearer, http.StatusUnauthorized,
			[]string{`Bearer error="invalid_token"`, "DPoP " + algs}, false},
		{"revoked, under DPoP", ErrRevoked, AuthSchemeDPoP, http.StatusUnauthorized,
			[]string{`DPoP error="invalid_token"`}, false},
		{"no proof", dpop.ErrRequired, AuthSchemeBearer, http.StatusUnauthorized,
			[]string{`DPoP error="invalid_dpop_proof"`}, false},
		{"replayed proof", dpop.ErrReplayed, AuthSchemeDPoP, http.StatusUnauthorized,
			[]string{`DPoP error="invalid_dpop_proof"`}, false},
		{"downgraded scheme", ErrAuthSchemeMismatch, AuthSchemeBearer, http.StatusUnauthorized,
			[]string{`Bearer error="invalid_token"`}, false},
		{"nonce needed", dpop.ErrNonceRequired, AuthSchemeDPoP, http.StatusUnauthorized,
			[]string{`DPoP error="use_dpop_nonce"`}, true},
		{"caveat", fmt.Errorf("%w: x", ErrResourceNotAllowed), AuthSchemeBearer, http.StatusForbidden,
			[]string{`Bearer error="insufficient_scope"`}, false},
		{"budget", ErrBudgetExceeded, AuthSchemeBearer, http.StatusForbidden, nil, false},
		{"store outage", errors.New("dial tcp: refused"), AuthSchemeBearer, http.StatusInternalServerError, nil, false},
		// An issuer's JWKS out of reach is the server's trouble, not a bad
		// token: the client may retry, and must not discard its token.
		{"keys unavailable", fmt.Errorf("limes: resolve kid %q: %w", "k", jwks.ErrUnavailable), AuthSchemeBearer,
			http.StatusServiceUnavailable, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := withNonces.ErrorResponse(context.Background(), tc.err, tc.scheme)
			if r.Status != tc.status || r.Problem.Status != tc.status {
				t.Errorf("status = %d / %d, want %d", r.Status, r.Problem.Status, tc.status)
			}
			if len(r.Challenges) < len(tc.challenges) || len(tc.challenges) == 0 && len(r.Challenges) != 0 {
				t.Fatalf("challenges = %q, want %q", r.Challenges, tc.challenges)
			}
			for i, want := range tc.challenges {
				if !strings.Contains(r.Challenges[i], want) {
					t.Errorf("challenge %d = %q, want it to contain %q", i, r.Challenges[i], want)
				}
			}
			if (r.DPoPNonce != "") != tc.nonce || tc.nonce && !validNonce(t, nonces, r.DPoPNonce) {
				t.Errorf("nonce = %q, want one: %v", r.DPoPNonce, tc.nonce)
			}
			if !strings.HasPrefix(r.Problem.Type, problemTypeBase) || r.Problem.Title == "" {
				t.Errorf("problem = %+v", r.Problem)
			}
		})
	}
}

// A refusal says nothing the server's error text says: no kid, store or
// issuer reaches the client.
func TestErrorResponseLeaksNothing(t *testing.T) {
	_, v, _, _ := buildIssuerVerifier(t)
	secret := "kid \"internal-kid-7\" from vault\nat db-3"
	r := v.ErrorResponse(context.Background(), fmt.Errorf("%w: %s", ErrInvalidToken, secret), AuthSchemeBearer)
	all := strings.Join(r.Challenges, " ") + r.Problem.Title + r.Problem.Detail
	if strings.Contains(all, "internal-kid-7") || strings.Contains(all, "db-3") {
		t.Errorf("the response repeats the error's text: %q", all)
	}
	r = v.ErrorResponse(context.Background(), errors.New(secret), AuthSchemeBearer)
	if strings.Contains(r.Problem.Title+r.Problem.Detail, "vault") {
		t.Errorf("an internal error leaked: %+v", r.Problem)
	}
}

// RFC 6750 §3 limits the characters of an error description; a quote or a
// backslash would end the quoted-string early.
func TestAuthParamKeepsTheQuotedStringWhole(t *testing.T) {
	got := authParam("error_description", "a \"quoted\" \\ é\nb")
	value := strings.TrimSuffix(strings.TrimPrefix(got, `error_description="`), `"`)
	for _, r := range value {
		if r == '"' || r == '\\' || r < 0x20 || r > 0x7E {
			t.Fatalf("authParam = %s: %q is outside RFC 6750's characters", got, r)
		}
	}
	if !strings.HasPrefix(value, "a quoted") || !strings.HasSuffix(value, "b") {
		t.Errorf("authParam = %s; want the allowed characters kept", got)
	}
}

func TestWriteErrorResponse(t *testing.T) {
	_, v, _, _ := buildIssuerVerifier(t)
	rec := httptest.NewRecorder()
	WriteErrorResponse(rec, v.ErrorResponse(context.Background(), ErrExpired, AuthSchemeBearer))
	if rec.Code != http.StatusUnauthorized || rec.Header().Get("Content-Type") != mediaTypeProblem ||
		len(rec.Header().Values(headerWWWAuthenticate)) != 2 || rec.Header().Get(httpx.HeaderCacheControl) != httpx.DirectiveNoStore {
		t.Fatalf("response = %d %v", rec.Code, rec.Header())
	}
	var p Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil || p.Status != http.StatusUnauthorized || p.Type == "" {
		t.Errorf("body = %s, %v", rec.Body, err)
	}
}

// The one table reads each sentinel by its own rule: no earlier, wider rule
// swallows it; rules sharing a Reason agree on whether it stands; and a
// family root, whose members carry the reasons, names none of its own.
func TestRefusalRulesAreOrdered(t *testing.T) {
	permanence := map[Reason]bool{}
	for _, rule := range refusalRules {
		if first, _ := ruleFor(rule.sentinel); first.sentinel != rule.sentinel { //nolint:errorlint // which row matched, not whether the error matches
			t.Errorf("%v is matched by the rule for %v before its own", rule.sentinel, first.sentinel)
		}
		if rule.reason == "" {
			continue
		}
		if p, seen := permanence[rule.reason]; seen && p != rule.permanent {
			t.Errorf("Reason %q is permanent in one rule and not in another", rule.reason)
		}
		permanence[rule.reason] = rule.permanent
	}
	for _, root := range []error{ErrUnauthenticated, ErrProofOfPossession, ErrCaveatViolation} {
		if r, ok := ReasonOf(root); ok {
			t.Errorf("family root %v has Reason %q; its members carry the reasons", root, r)
		}
	}
	if reasons := Reasons(); len(reasons) != len(slices.Compact(slices.Sorted(slices.Values(reasons)))) {
		t.Errorf("Reasons() repeats one: %v", reasons)
	}
}

// validNonce is n.Valid with a failure of the nonces failing the test.
func validNonce(t *testing.T, n dpop.Nonces, nonce string) bool {
	t.Helper()
	ok, err := n.Valid(context.Background(), nonce)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

// A request with no credentials is challenged bare: RFC 6750 §3.1 sends no
// error code to a client that has not tried.
func TestNoCredentialsIsChallengedWithoutAnErrorCode(t *testing.T) {
	_, v, _, _ := buildIssuerVerifier(t)
	_, _, err := ParseAuthorization("")
	resp := v.ErrorResponse(context.Background(), err, AuthSchemeUnspecified)
	if resp.Status != http.StatusUnauthorized || len(resp.Challenges) == 0 {
		t.Fatalf("ErrorResponse = %+v; want 401 with a challenge", resp)
	}
	for _, c := range resp.Challenges {
		if strings.Contains(c, "error=") {
			t.Errorf("challenge %q names an error", c)
		}
	}
	// RFC 6750 §3: the scheme is followed by at least one auth-param.
	if want := string(AuthSchemeBearer) + ` realm="` + DefaultRealm + `"`; resp.Challenges[0] != want {
		t.Errorf("first challenge = %q, want %q", resp.Challenges[0], want)
	}
}

// A configured realm names the protection space in every challenge, Bearer
// and DPoP alike.
func TestEveryChallengeNamesTheRealm(t *testing.T) {
	const realm = "api.example"
	_, configured, _, _ := buildIssuerVerifier(t)
	cfg := configured.cfg
	cfg.Realm = realm
	v, err := NewVerifier(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, err := range []error{ErrNoCredentials, ErrExpired, ErrResourceNotAllowed, dpop.ErrRequired} {
		resp := v.ErrorResponse(context.Background(), err, AuthSchemeBearer)
		if len(resp.Challenges) == 0 {
			t.Fatalf("%v: no challenge", err)
		}
		for _, c := range resp.Challenges {
			if !strings.HasSuffix(c, `realm="`+realm+`"`) {
				t.Errorf("%v: challenge %q does not name realm %q", err, c, realm)
			}
		}
	}
}

// A verifier with no DPoP configured advertises no algorithms: a client
// that presented under DPoP is challenged under it, but told nothing it
// could sign a proof with.
func TestDPoPChallengeAdvertisesAlgsOnlyWhenConfigured(t *testing.T) {
	_, configured, _, _ := buildIssuerVerifier(t)
	cfg := configured.cfg
	cfg.DPoP = nil
	bare, err := NewVerifier(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		v        *Verifier
		wantAlgs bool
	}{
		"configured": {configured, true},
		"bare":       {bare, false},
	} {
		resp := tc.v.ErrorResponse(t.Context(), ErrExpired, AuthSchemeDPoP)
		if len(resp.Challenges) == 0 {
			t.Fatalf("%s: no challenge", name)
		}
		if got := strings.Contains(resp.Challenges[0], authParamAlgs+"="); got != tc.wantAlgs {
			t.Errorf("%s: challenge %q advertises algs = %v, want %v", name, resp.Challenges[0], got, tc.wantAlgs)
		}
	}
}
