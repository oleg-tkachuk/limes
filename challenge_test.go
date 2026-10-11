package limes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestErrorResponse(t *testing.T) {
	_, v, _, _ := buildIssuerVerifier(t) // DPoP configured
	nonces, err := NewHMACNonces(time.Minute, nonceKey(1))
	if err != nil {
		t.Fatal(err)
	}
	cfg := v.cfg
	cfg.DPoP = &DPoPVerifier{Replay: NewMemoryReplayCache(0), Nonces: nonces}
	withNonces, err := NewStandardVerifier(cfg)
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
		{"expired bearer", ErrExpired, AuthSchemeBearer, http.StatusUnauthorized,
			[]string{`Bearer error="invalid_token"`, "DPoP " + algs}, false},
		{"revoked, under DPoP", ErrRevoked, AuthSchemeDPoP, http.StatusUnauthorized,
			[]string{`DPoP error="invalid_token"`}, false},
		{"no proof", ErrDPoPRequired, AuthSchemeBearer, http.StatusUnauthorized,
			[]string{`DPoP error="invalid_dpop_proof"`}, false},
		{"replayed proof", ErrDPoPReplayed, AuthSchemeDPoP, http.StatusUnauthorized,
			[]string{`DPoP error="invalid_dpop_proof"`}, false},
		{"downgraded scheme", ErrAuthSchemeMismatch, AuthSchemeBearer, http.StatusUnauthorized,
			[]string{`Bearer error="invalid_token"`}, false},
		{"nonce needed", ErrDPoPNonceRequired, AuthSchemeDPoP, http.StatusUnauthorized,
			[]string{`DPoP error="use_dpop_nonce"`}, true},
		{"caveat", fmt.Errorf("%w: x", ErrResourceNotAllowed), AuthSchemeBearer, http.StatusForbidden,
			[]string{`Bearer error="insufficient_scope"`}, false},
		{"budget", ErrBudgetExceeded, AuthSchemeBearer, http.StatusForbidden, nil, false},
		{"store outage", errors.New("dial tcp: refused"), AuthSchemeBearer, http.StatusInternalServerError, nil, false},
		// An issuer's JWKS out of reach is the server's trouble, not a bad
		// token: the client may retry, and must not discard its token.
		{"keys unavailable", fmt.Errorf("%w: kid %q: %w", ErrInvalidSignature, "k", ErrJWKSUnavailable), AuthSchemeBearer,
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
			if (r.DPoPNonce != "") != tc.nonce || tc.nonce && !nonces.Valid(context.Background(), r.DPoPNonce) {
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
	r := v.ErrorResponse(context.Background(), fmt.Errorf("%w: %s", ErrInvalidSignature, secret), AuthSchemeBearer)
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
	if rec.Code != http.StatusUnauthorized || rec.Header().Get("Content-Type") != MediaTypeProblem ||
		len(rec.Header().Values(WWWAuthenticateHeader)) != 2 || rec.Header().Get(headerCacheControl) != directiveNoStore {
		t.Fatalf("response = %d %v", rec.Code, rec.Header())
	}
	var p Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil || p.Status != http.StatusUnauthorized || p.Type == "" {
		t.Errorf("body = %s, %v", rec.Body, err)
	}
}
