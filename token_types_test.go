package limes

import (
	"context"
	"crypto/ed25519"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes/internal/jose"
)

// The typs of the retired formats.
const (
	retiredV4Type TokenType = "limes-cap-v4+jwt" //nolint:gosec // G101: a JWT type label, not a credential
	retiredV5Type TokenType = "limes-cap-v5+jwt" //nolint:gosec // G101: a JWT type label, not a credential
)

// What v4 carried and v5 does not: a claim, an unnamespaced operation and the
// old name of the parent's run.
const (
	retiredGenerationKey = "limes_gen"
	retiredV4Op          = "search"
	retiredParentRunKey  = "parent_agent_id"
)

// headerOf is the protected header of a token of type typ, signed by key k1.
func headerOf(typ TokenType) map[string]any {
	return map[string]any{"alg": jose.AlgEd25519, "kid": "k1", "typ": typ}
}

// withParentRun names a parent run in the token's agent details, under member.
func withParentRun(member string) func(map[string]any) {
	return func(c map[string]any) {
		c["limes_principal"].(map[string]any)["agent"] = map[string]any{member: uuid.NewString()}
	}
}

// A v6 token is read under v6's schema alone: nothing v4 carried passes, a
// copy quota does, and a token of the v4 or v5 type is refused however well
// formed.
func TestTokenIsReadUnderTheV6Schema(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	v := verifierFor(t, pub, VerifierConfig{})
	for name, tc := range map[string]struct {
		typ    TokenType
		mutate func(map[string]any)
		ok     bool
	}{
		"v6":                             {TokenTypeV6, func(map[string]any) {}, true},
		"v6 naming the parent run":       {TokenTypeV6, withParentRun("parent_run_id"), true},
		"v6 with a copy quota":           {TokenTypeV6, func(c map[string]any) { c["limes_caveats"].(map[string]any)["max_copies"] = 3 }, true},
		"v6 with limes_gen":              {TokenTypeV6, func(c map[string]any) { c[retiredGenerationKey] = 1 }, false},
		"v6 with an unnamespaced v4 op":  {TokenTypeV6, func(c map[string]any) { c["limes_caveats"].(map[string]any)["ops"] = []string{retiredV4Op} }, false},
		"v6 naming the parent run as v4": {TokenTypeV6, withParentRun(retiredParentRunKey), false},
		"a v4 token":                     {retiredV4Type, func(map[string]any) {}, false},
		"a v5 token":                     {retiredV5Type, func(map[string]any) {}, false},
	} {
		claims := validClaims(uuid.New())
		tc.mutate(claims)
		_, err := v.Verify(context.Background(), VerifyRequest{Token: resign(t, priv, headerOf(tc.typ), claims), Audience: audienceData})
		if tc.ok && err != nil {
			t.Errorf("%s: %v, want it verified", name, err)
		}
		if !tc.ok && !errors.Is(err, ErrInvalidToken) {
			t.Errorf("%s: err = %v, want ErrInvalidToken", name, err)
		}
	}
	if err := (Caveats{Ops: []Op{retiredV4Op}}).Validate(); !errors.Is(err, ErrInvalidCaveats) {
		t.Errorf("Validate of caveats naming %q: err = %v, want ErrInvalidCaveats", retiredV4Op, err)
	}
}

// A Signer mints v6 by default, and refuses a type nothing reads — v4 and
// v5 among them.
func TestSignerMintsTheTypeItIsConfiguredWith(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	for _, configured := range []TokenType{"", TokenTypeV6} {
		s, err := NewSigner(SignerConfig{KeyID: "k1", Key: priv, TokenType: configured})
		if err != nil {
			t.Fatalf("NewSigner(%q): %v", configured, err)
		}
		token, err := s.sign(context.Background(), signableCapability())
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		parts, err := jose.Split(token)
		if err != nil {
			t.Fatal(err)
		}
		h, err := parseHeader(parts.Header)
		if err != nil || TokenType(h.Typ) != TokenTypeV6 {
			t.Errorf("configured %q: typ = %q, %v; want %q", configured, h.Typ, err, TokenTypeV6)
		}
		if _, err := parseClaims(parts.Payload, TokenTypeV6); err != nil {
			t.Errorf("configured %q: the claims do not read as v6: %v", configured, err)
		}
	}
	for _, typ := range []TokenType{retiredV4Type, retiredV5Type, "limes-cap-v7+jwt"} {
		if _, err := NewSigner(SignerConfig{KeyID: "k1", Key: priv, TokenType: typ}); err == nil {
			t.Errorf("NewSigner built a signer for %q, which nothing reads", typ)
		}
	}
}

// signableCapability is a capability sign accepts.
func signableCapability() Capability {
	now := goldenClock()
	return Capability{
		ID: uuid.New(), Issuer: "iss",
		Subject:  Principal{Type: PrincipalTypeAgent, TenantID: uuid.New(), Subject: "s"},
		Audience: []string{audienceData}, Caveats: Caveats{Ops: []Op{OpGet}},
		IssuedAt: now, ExpiresAt: now.Add(tokenTimeResolution),
	}
}
