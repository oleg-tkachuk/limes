package limes_test

import (
	"context"
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"strings"
	"testing"

	"github.com/oleg-tkachuk/limes"
)

// idpKID is a kid an identity provider signs its access tokens under.
const idpKID = "idp-key-1"

// headerToken is a compact JWT of header and claims, signed by nobody.
func headerToken(t *testing.T, header, claims map[string]any) string {
	t.Helper()
	seg := func(v any) string {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	return seg(header) + "." + seg(claims) + "." + base64.RawURLEncoding.EncodeToString([]byte("signature"))
}

// HasLimesKeyID tells a capability from an identity provider's access
// token, both typed at+jwt, by the kid alone.
func TestHasLimesKeyID(t *testing.T) {
	s := newExampleStack()
	_, capability := s.issue()
	idpToken := headerToken(t, map[string]any{"alg": "RS256", "typ": limes.TokenType, "kid": idpKID, "x5t": "thumb"}, map[string]any{"iss": "https://idp.example.com"})
	for name, tc := range map[string]struct {
		token string
		want  bool
	}{
		"a capability":                        {capability, true},
		"an identity provider's access token": {idpToken, false},
		"no kid":                              {headerToken(t, map[string]any{"alg": "RS256", "typ": limes.TokenType}, map[string]any{}), false},
		"not a JWT":                           {"opaque-access-token", false},
		"a header that is no JSON":            {"bm90LWpzb24.e30.c2ln", false},
		"a header that is no base64url":       {"!!!.e30.c2ln", false},
	} {
		if got := limes.HasLimesKeyID(tc.token); got != tc.want {
			t.Errorf("%s: HasLimesKeyID = %v, want %v", name, got, tc.want)
		}
	}
}

// HasLimesKeyID decides nothing: a forged token naming a limes kid answers
// true, and the verifier it is routed to refuses it; an identity provider's
// token routed to the verifier anyway is refused before any key is looked
// up.
func TestHasLimesKeyIDIsNoAuthorization(t *testing.T) {
	s := newExampleStack()
	_, capability := s.issue()
	parts := strings.SplitN(capability, ".", 3)
	forged := parts[0] + "." + parts[1] + "." + base64.RawURLEncoding.EncodeToString([]byte("not the signature"))
	if !limes.HasLimesKeyID(forged) {
		t.Fatal("a forged capability's header names a limes kid, so HasLimesKeyID answers true")
	}
	idpToken := headerToken(t, map[string]any{"alg": "EdDSA", "typ": limes.TokenType, "kid": idpKID}, map[string]any{"iss": exampleIssuerName})
	for name, token := range map[string]string{"forged": forged, "an identity provider's": idpToken} {
		_, err := s.verifier.Verify(context.Background(), limes.VerifyRequest{Token: token, Audience: exampleAudience})
		if !errors.Is(err, limes.ErrInvalidToken) {
			t.Errorf("%s token: Verify = %v, want ErrInvalidToken", name, err)
		}
	}
	if _, err := s.verifier.Verify(context.Background(), limes.VerifyRequest{Token: idpToken, Audience: exampleAudience}); err == nil || !strings.Contains(err.Error(), "is not a limes kid") {
		t.Errorf("an identity provider's token: Verify = %v, want it refused by its kid, before any key is looked up", err)
	}
}
