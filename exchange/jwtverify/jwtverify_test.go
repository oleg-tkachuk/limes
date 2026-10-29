package jwtverify

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"github.com/oleg-tkachuk/limes/exchange"
	"slices"
	"testing"

	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwk"
)

// typJWT is the generic type some identity providers give their access
// tokens instead of RFC 9068's.
const typJWT = "JWT"

func newVerifier(t *testing.T, cfg Config) *Verifier {
	t.Helper()
	v, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// An RFC 9068 access token verifies, and its claims are read as the profile
// defines them.
func TestVerifierReadsAnAccessToken(t *testing.T) {
	p := newIDP(t)
	v := newVerifier(t, Config{Issuer: idpIssuer, Keys: StaticKeys(p.keys), Now: clock})
	claims := accessClaims()
	for _, typ := range []string{TypeAccessToken, TypeAccessTokenMedia, "AT+JWT"} {
		s, err := v.VerifySubject(t.Context(), p.sign(t, typ, claims))
		if err != nil {
			t.Fatalf("typ %s: %v", typ, err)
		}
		if s.Issuer != idpIssuer || s.Subject != userSubject || s.ClientID != idpClient || s.ID != claims[claimJWTID] ||
			!slices.Equal(s.Audience, []string{mcpServer}) || !slices.Equal(s.Scopes, []string{"tools:read", "tools:write"}) ||
			!s.ExpiresAt.Equal(testNow.Add(subjectTTL)) || !s.IssuedAt.Equal(testNow) || s.JKT != "" || s.Claims[ClaimClientID] != idpClient {
			t.Errorf("typ %s: subject %+v", typ, s)
		}
	}
}

// Whatever RFC 9068 requires of an access token, and the verifier's own
// issuer, keys, types and algorithms, a token lacking is refused.
func TestVerifierRefusals(t *testing.T) {
	p, other := newIDP(t), newIDP(t)
	v := newVerifier(t, Config{Issuer: idpIssuer, Keys: StaticKeys(p.keys), Now: clock})
	claims := accessClaims()
	cases := map[string]string{
		"typ JWT":                p.sign(t, typJWT, claims),
		"no typ":                 p.sign(t, "", claims),
		"another issuer":         p.sign(t, TypeAccessToken, with(claims, map[string]any{claimIssuer: "https://other.example.com"})),
		"expired":                p.sign(t, TypeAccessToken, with(claims, map[string]any{claimExpiry: testNow.Add(-subjectTTL).Unix()})),
		"another key":            other.sign(t, TypeAccessToken, claims),
		"a binding not DPoP":     p.sign(t, TypeAccessToken, with(claims, map[string]any{claimCnf: map[string]any{"x5t#S256": "abc"}})),
		"a binding besides DPoP": p.sign(t, TypeAccessToken, with(claims, map[string]any{claimCnf: map[string]any{cnfJKT: "0ZcOCORZNYy-DWpqq30jZyJGHTN0d2HglBV3uiguA4I", "x5t#S256": "abc"}})),
		"an empty jkt":           p.sign(t, TypeAccessToken, with(claims, map[string]any{claimCnf: map[string]any{cnfJKT: ""}})),
		"a scope not text":       p.sign(t, TypeAccessToken, with(claims, map[string]any{claimScope: 1})),
		"not a JWS":              "a.b.c",
	}
	for _, claim := range []string{claimIssuer, claimExpiry, claimAudience, claimSubject, ClaimClientID, claimIssuedAt, claimJWTID} {
		cases["no "+claim] = p.sign(t, TypeAccessToken, with(claims, map[string]any{claim: nil}))
	}
	for name, token := range cases {
		if _, err := v.VerifySubject(t.Context(), token); !errors.Is(err, exchange.ErrInvalidGrant) {
			t.Errorf("%s: %v, want exchange.ErrInvalidGrant", name, err)
		}
	}
}

// The algorithms a verifier accepts are its config's, and only asymmetric
// ones may be named.
func TestVerifierHoldsItsAlgorithms(t *testing.T) {
	p := newIDP(t)
	v := newVerifier(t, Config{Issuer: idpIssuer, Keys: StaticKeys(p.keys), Now: clock, Algorithms: []string{jwa.ES256().String()}})
	if _, err := v.VerifySubject(t.Context(), p.sign(t, TypeAccessToken, accessClaims())); !errors.Is(err, exchange.ErrInvalidGrant) {
		t.Errorf("an Ed25519 token at a verifier of ES256 alone: %v", err)
	}
	for _, alg := range []string{jwa.HS256().String(), jwa.NoSignature().String()} {
		if _, err := New(Config{Issuer: idpIssuer, Keys: StaticKeys(p.keys), Algorithms: []string{alg}}); err == nil {
			t.Errorf("a verifier accepting %s was built", alg)
		}
	}
	for name, cfg := range map[string]Config{"no issuer": {Keys: StaticKeys(p.keys)}, "no keys": {Issuer: idpIssuer}} {
		if _, err := New(cfg); err == nil {
			t.Errorf("a verifier with %s was built", name)
		}
	}
}

// A provider that types its tokens JWT and names the client in azp is read
// once its config says so.
func TestVerifierReadsAProviderOfItsOwn(t *testing.T) {
	p := newIDP(t)
	const azp = "azp"
	v := newVerifier(t, Config{Issuer: idpIssuer, Keys: StaticKeys(p.keys), Now: clock, Types: []string{typJWT}, ClientIDClaim: azp})
	claims := with(accessClaims(), map[string]any{ClaimClientID: nil, azp: idpClient})
	s, err := v.VerifySubject(t.Context(), p.sign(t, typJWT, claims))
	if err != nil || s.ClientID != idpClient {
		t.Errorf("subject %+v, %v", s, err)
	}
}

// A token bound to a DPoP key names it; an ES256 identity provider is read
// like an Ed25519 one.
func TestVerifierReadsTheBindingAndOtherAlgorithms(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := jwk.Import(key.Public())
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]any{jwk.KeyIDKey: idpKID, jwk.AlgorithmKey: jwa.ES256()} {
		if err := pub.Set(name, value); err != nil {
			t.Fatal(err)
		}
	}
	set := jwk.NewSet()
	if err := set.AddKey(pub); err != nil {
		t.Fatal(err)
	}
	v := newVerifier(t, Config{Issuer: idpIssuer, Keys: StaticKeys(set), Now: clock})
	const jkt = "0ZcOCORZNYy-DWpqq30jZyJGHTN0d2HglBV3uiguA4I"
	token := signWith(t, key, jwa.ES256(), TypeAccessToken, with(accessClaims(), map[string]any{claimCnf: map[string]any{cnfJKT: jkt}}))
	s, err := v.VerifySubject(t.Context(), token)
	if err != nil || s.JKT != jkt {
		t.Errorf("subject %+v, %v; want bound to %s", s, err, jkt)
	}
}
