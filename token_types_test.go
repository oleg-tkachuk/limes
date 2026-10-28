package limes

import (
	"context"
	"crypto/ed25519"
	"errors"
	"strings"
	"testing"
	"uuid"

	"github.com/oleg-tkachuk/limes/internal/jose"
	"github.com/oleg-tkachuk/limes/jwks"
)

// The typs of the retired formats.
const (
	retiredV4Type = "limes-cap-v4+jwt" //nolint:gosec // G101: a JWT type label, not a credential
	retiredV5Type = "limes-cap-v5+jwt" //nolint:gosec // G101: a JWT type label, not a credential
	retiredV6Type = "limes-cap-v6+jwt" //nolint:gosec // G101: a JWT type label, not a credential
)

// What v4 carried and v5 does not: a claim, an unnamespaced operation and the
// old name of the parent's run.
const (
	retiredGenerationKey = "limes_gen"
	retiredV4Op          = "search"
	retiredParentRunKey  = "parent_agent_id"
)

// headerOf is the protected header of a token of type typ, signed under
// testKID.
func headerOf(typ string) map[string]any {
	return map[string]any{"alg": jose.AlgEd25519, "kid": testKID, "typ": typ}
}

// withParentRun names a parent run in the token's agent details, under member.
func withParentRun(member string) func(map[string]any) {
	return func(c map[string]any) {
		c["limes_principal"].(map[string]any)["agent"] = map[string]any{member: uuid.New().String()}
	}
}

// A token is read under its schema alone: nothing v4 carried passes, a copy
// quota does, and a token of a retired type is refused however well formed.
func TestTokenIsReadUnderItsSchema(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	v := verifierFor(t, pub, VerifierConfig{})
	for name, tc := range map[string]struct {
		typ    string
		mutate func(map[string]any)
		ok     bool
	}{
		"current":                     {TokenType, func(map[string]any) {}, true},
		"naming the parent run":       {TokenType, withParentRun("parent_run_id"), true},
		"with a copy quota":           {TokenType, func(c map[string]any) { c["limes_caveats"].(map[string]any)["max_copies"] = 3 }, true},
		"with limes_gen":              {TokenType, func(c map[string]any) { c[retiredGenerationKey] = 1 }, false},
		"with an unnamespaced v4 op":  {TokenType, func(c map[string]any) { c["limes_caveats"].(map[string]any)["ops"] = []string{retiredV4Op} }, false},
		"naming the parent run as v4": {TokenType, withParentRun(retiredParentRunKey), false},
		"a v4 token":                  {retiredV4Type, func(map[string]any) {}, false},
		"a v5 token":                  {retiredV5Type, func(map[string]any) {}, false},
		"a v6 token":                  {retiredV6Type, func(map[string]any) {}, false},
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

// A Signer writes at+jwt under its kid, which must be in limes's namespace.
func TestSignerWritesAnAccessTokenUnderALimesKid(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	s, err := NewSigner(SignerConfig{KeyID: testKID, Key: SealKey(priv)})
	if err != nil {
		t.Fatal(err)
	}
	token, err := s.sign(context.Background(), signableCapability())
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	parts, err := jose.Split(token)
	if err != nil {
		t.Fatal(err)
	}
	if h, err := parseHeader(parts.Header); err != nil || h.Typ != TokenType || h.Kid != testKID {
		t.Errorf("header %+v, %v; want typ %q under %q", h, err, TokenType, testKID)
	}
	for _, kid := range []string{"", "k1", jwks.KeyIDPrefix, "LIMES:k1", " " + testKID} {
		if _, err := NewSigner(SignerConfig{KeyID: kid, Key: SealKey(priv)}); err == nil {
			t.Errorf("NewSigner signs under %q, outside limes's namespace", kid)
		}
	}
}

// A token under a kid outside limes's namespace is refused before any key is
// looked up, even when the key it names would verify it.
func TestVerifyRefusesAKidOutsideTheNamespace(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	const foreign = "k1"
	lookups := &countingKeys{KeyResolver: staticKeys(t, map[string]ed25519.PublicKey{foreign: pub, testKID: pub})}
	v, err := NewVerifier(VerifierConfig{Keys: lookups, Revocations: newMemStore(), TrustedIssuers: []string{"iss"}})
	if err != nil {
		t.Fatal(err)
	}
	token := resign(t, priv, map[string]any{"alg": jose.AlgEd25519, "kid": foreign, "typ": TokenType}, validClaims(uuid.New()))
	if _, err := v.Verify(t.Context(), VerifyRequest{Token: token, Audience: audienceData}); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("a token under kid %q: %v, want ErrInvalidToken", foreign, err)
	}
	if lookups.n != 0 {
		t.Errorf("%d key lookups for a kid outside the namespace", lookups.n)
	}
}

// countingKeys counts the keys looked up.
type countingKeys struct {
	KeyResolver
	n int
}

func (k *countingKeys) PublicKey(ctx context.Context, kid string) (ed25519.PublicKey, error) {
	k.n++
	return k.KeyResolver.PublicKey(ctx, kid)
}

// signableCapability is a capability sign accepts.
func signableCapability() Capability {
	now := goldenClock()
	return Capability{
		ID: uuid.New(), Issuer: "iss",
		Subject:  Principal{Type: PrincipalTypeAgent, TenantID: uuid.New(), Subject: "s"},
		Audience: []string{audienceData}, Caveats: Caveats{Ops: []Op{OpGet}},
		IssuedAt: now, ExpiresAt: now.Add(tokenTimeResolution), ClientID: testClientID,
	}
}

// An id inside a claim is read only as the signer spells it: lowercase and
// hyphenated. Any other spelling of the same id is refused, so no reader
// comparing strings sees two values for it.
func TestClaimIDsAreCanonical(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	v := verifierFor(t, pub, VerifierConfig{})
	tenant := uuid.New()
	run := uuid.New().String()
	with := func(edit func(p map[string]any)) string {
		c := validClaims(tenant)
		p := c["limes_principal"].(map[string]any)
		p["tenant_id"], p["agent"] = tenant.String(), map[string]any{"run_id": run}
		edit(p)
		return resign(t, priv, headerOf(TokenType), c)
	}
	if _, err := v.Verify(t.Context(), VerifyRequest{Token: with(func(map[string]any) {}), Audience: audienceData}); err != nil {
		t.Fatalf("canonical ids: %v", err)
	}
	for name, edit := range map[string]func(p map[string]any){
		"an uppercase tenant":      func(p map[string]any) { p["tenant_id"] = strings.ToUpper(tenant.String()) },
		"a braced tenant":          func(p map[string]any) { p["tenant_id"] = "{" + tenant.String() + "}" },
		"a urn tenant":             func(p map[string]any) { p["tenant_id"] = "urn:uuid:" + tenant.String() },
		"an unhyphenated tenant":   func(p map[string]any) { p["tenant_id"] = strings.ReplaceAll(tenant.String(), "-", "") },
		"an uppercase run":         func(p map[string]any) { p["agent"] = map[string]any{"run_id": strings.ToUpper(run)} },
		"a parent run as a number": func(p map[string]any) { p["agent"] = map[string]any{"parent_run_id": 1} },
	} {
		if _, err := v.Verify(t.Context(), VerifyRequest{Token: with(edit), Audience: audienceData}); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("%s: %v, want ErrInvalidToken", name, err)
		}
	}
}
