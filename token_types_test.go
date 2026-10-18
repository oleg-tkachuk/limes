package limes

import (
	"context"
	"crypto/ed25519"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes/internal/jose"
)

// v4OnlyOp is one of the operations v4 defined unnamespaced and v5 does not.
const v4OnlyOp = "search"

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

// Each token type is read under its own schema: limes_gen and the operations
// v4 defined unnamespaced are a v4 token's, refused in a v5 one.
func TestEachTokenTypeIsReadUnderItsSchema(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	v := verifierFor(t, pub, VerifierConfig{})
	withGen := func(c map[string]any) { c[claimGeneration] = 1 }
	withV4Op := func(c map[string]any) { c["limes_caveats"].(map[string]any)["ops"] = []string{v4OnlyOp} }
	for name, tc := range map[string]struct {
		typ    TokenType
		mutate func(map[string]any)
		ok     bool
	}{
		"v4 with limes_gen":                   {TokenTypeV4, withGen, true},
		"v4 with a v4 operation":              {TokenTypeV4, withV4Op, true},
		"v5 with neither":                     {TokenTypeV5, func(map[string]any) {}, true},
		"v5 with limes_gen":                   {TokenTypeV5, withGen, false},
		"v5 with a v4 operation":              {TokenTypeV5, withV4Op, false},
		"v4 with an unnamespaced op too":      {TokenTypeV4, func(c map[string]any) { c["limes_caveats"].(map[string]any)["ops"] = []string{"browse"} }, false},
		"v4 naming the parent run as v4 does": {TokenTypeV4, withParentRun(claimParentAgentID), true},
		"v4 naming it as v5 does":             {TokenTypeV4, withParentRun("parent_run_id"), false},
		"v5 naming it as v5 does":             {TokenTypeV5, withParentRun("parent_run_id"), true},
		"v5 naming it as v4 does":             {TokenTypeV5, withParentRun(claimParentAgentID), false},
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
}

// A v4 operation keeps its name, and the effect of a consumer's operation:
// this package no longer says it reads.
func TestAV4OperationIsAConsumersOperation(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	v := verifierFor(t, pub, VerifierConfig{})
	claims := validClaims(uuid.New())
	claims["limes_caveats"].(map[string]any)["ops"] = []string{v4OnlyOp}
	vc, err := v.Verify(context.Background(), VerifyRequest{Token: resign(t, priv, headerOf(TokenTypeV4), claims), Audience: audienceData})
	if err != nil {
		t.Fatal(err)
	}
	if ops := vc.Capability().Caveats.Ops; len(ops) != 1 || ops[0] != v4OnlyOp {
		t.Errorf("Ops = %q, want [%q]", ops, v4OnlyOp)
	}
	if e, err := Op(v4OnlyOp).ResolveEffect(EffectUnspecified); err != nil || e == EffectRead {
		t.Errorf("ResolveEffect(%q) = %v, %v; want a mutating default", v4OnlyOp, e, err)
	}
	if err := (Caveats{Ops: []Op{v4OnlyOp}}).Validate(); !errors.Is(err, ErrInvalidCaveats) {
		t.Errorf("Validate of caveats naming %q: err = %v, want ErrInvalidCaveats: nothing mints it", v4OnlyOp, err)
	}
}

// A verifier accepting one type refuses the other, before its claims are read.
func TestAVerifierRefusesATypeItDoesNotAccept(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	for accepted, refused := range map[TokenType]TokenType{TokenTypeV4: TokenTypeV5, TokenTypeV5: TokenTypeV4} {
		v := verifierFor(t, pub, VerifierConfig{AcceptTokenTypes: []TokenType{accepted}})
		token := resign(t, priv, headerOf(refused), validClaims(uuid.New()))
		if _, err := v.Verify(context.Background(), VerifyRequest{Token: token, Audience: audienceData}); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("a %s token at a verifier accepting %s: err = %v, want ErrInvalidToken", refused, accepted, err)
		}
	}
}

// A Signer mints v4 unless told v5, and refuses a type nothing reads.
func TestSignerMintsTheTypeItIsConfiguredWith(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	for configured, want := range map[TokenType]TokenType{"": TokenTypeV4, TokenTypeV4: TokenTypeV4, TokenTypeV5: TokenTypeV5} {
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
		if err != nil || TokenType(h.Typ) != want {
			t.Errorf("configured %q: typ = %q, %v; want %q", configured, h.Typ, err, want)
		}
		if _, err := parseClaims(parts.Payload, want); err != nil {
			t.Errorf("configured %q: the claims do not read as %s: %v", configured, want, err)
		}
	}
	if _, err := NewSigner(SignerConfig{KeyID: "k1", Key: priv, TokenType: "limes-cap-v6+jwt"}); err == nil {
		t.Error("NewSigner built a signer for a type nothing reads")
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
