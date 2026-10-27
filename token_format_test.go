package limes

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"maps"
	"testing"
	"time"
	"uuid"

	"github.com/oleg-tkachuk/limes/internal/jose"
)

// testClientID is the OAuth client the tests' capabilities name.
const testClientID = "client-1"

// issuerOf is an issuer signing with priv, recording to a store of its own.
func issuerOf(t *testing.T, priv ed25519.PrivateKey) *Issuer {
	t.Helper()
	signer, err := NewSigner(SignerConfig{KeyID: testKID, Key: SealKey(priv)})
	if err != nil {
		t.Fatal(err)
	}
	issuer, err := NewIssuer(IssuerConfig{Signer: signer, Store: newMemStore(), IssuerName: "iss"})
	if err != nil {
		t.Fatal(err)
	}
	return issuer
}

// issueClient issues a capability naming client.
func issueClient(t *testing.T, issuer *Issuer, client string) (Capability, string, error) {
	t.Helper()
	return issuer.issue(context.Background(), IssueRequest{
		IssuedBy: Principal{Subject: "op"}, Subject: Principal{Type: PrincipalTypeAgent, TenantID: uuid.New(), Subject: "a"},
		Audience: []string{audienceData}, Caveats: Caveats{Ops: []Op{OpGet, OpShare}}, TTL: time.Hour, ClientID: client,
	})
}

// A capability token is an RFC 9068 access token: typ at+jwt, its client in
// client_id and its format in limes_v; a verifier reads it, client and all.
func TestATokenIsAnAccessToken(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	_, token, err := issueClient(t, issuerOf(t, priv), testClientID)
	if err != nil {
		t.Fatal(err)
	}
	parts, err := jose.Split(token)
	if err != nil {
		t.Fatal(err)
	}
	h, err := parseHeader(parts.Header)
	if err != nil || h.Typ != TokenType {
		t.Errorf("typ = %q, %v; want %q", h.Typ, err, TokenType)
	}
	var claims map[string]any
	if raw, err := jose.UnB64(parts.Payload); err != nil || json.Unmarshal(raw, &claims) != nil {
		t.Fatal("payload does not decode")
	}
	if claims["client_id"] != testClientID || claims["limes_v"] != float64(formatV7) {
		t.Errorf("client_id %v, limes_v %v", claims["client_id"], claims["limes_v"])
	}
	v := verifierFor(t, pub, VerifierConfig{})
	got, err := v.Verify(t.Context(), VerifyRequest{Token: token, Audience: audienceData})
	if err != nil || got.Capability().ClientID != testClientID {
		t.Errorf("Verify = %+v, %v; want client %q", got.Capability(), err, testClientID)
	}
}

// RFC 9068 requires client_id, and none is invented: Issue refuses a
// request without one, and a signer a capability without one.
func TestClientIDIsRequired(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	if _, _, err := issueClient(t, issuerOf(t, priv), ""); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("Issue with no client: %v, want ErrInvalidRequest", err)
	}
	c := signableCapability()
	c.ClientID = ""
	if _, err := issuerOf(t, priv).signer.sign(context.Background(), c); err == nil {
		t.Error("a capability with no client was signed")
	}
}

// A token's claims are read under the schema limes_v names, with a
// client_id: an access token of another issuer's making, or a v6 payload
// under at+jwt, is refused.
func TestClaimsAreReadUnderTheirFormat(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	v := verifierFor(t, pub, VerifierConfig{})
	with := func(changes map[string]any) map[string]any {
		c := validClaims(uuid.New())
		maps.Copy(c, changes)
		for k, val := range changes {
			if val == nil {
				delete(c, k)
			}
		}
		return c
	}
	header := map[string]any{"alg": jose.AlgEd25519, "kid": testKID, "typ": TokenType}
	accepted := resign(t, priv, header, with(nil))
	if _, err := v.Verify(t.Context(), VerifyRequest{Token: accepted, Audience: audienceData}); err != nil {
		t.Fatalf("a v7 token: %v", err)
	}
	for name, token := range map[string]string{
		"a v6 payload as at+jwt":     resign(t, priv, header, with(map[string]any{"client_id": nil, "limes_v": nil})),
		"no limes_v":                 resign(t, priv, header, with(map[string]any{"limes_v": nil})),
		"another format":             resign(t, priv, header, with(map[string]any{"limes_v": formatV7 + 1})),
		"no client_id":               resign(t, priv, header, with(map[string]any{"client_id": nil})),
		"an empty client":            resign(t, priv, header, with(map[string]any{"client_id": ""})),
		"an unknown claim":           resign(t, priv, header, with(map[string]any{"scope": "get"})),
		"the retired v6 type":        resign(t, priv, map[string]any{"alg": jose.AlgEd25519, "kid": testKID, "typ": retiredV6Type}, with(nil)),
		"the media type of RFC 9068": resign(t, priv, map[string]any{"alg": jose.AlgEd25519, "kid": testKID, "typ": "application/at+jwt"}, with(nil)),
	} {
		if _, err := v.Verify(t.Context(), VerifyRequest{Token: token, Audience: audienceData}); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("%s: %v, want ErrInvalidToken", name, err)
		}
	}
}

// A child names its parent's client unless asked for another, and a
// Biscuit seals a token that verifies.
func TestClientIDDelegatesAndSeals(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	issuer := issuerOf(t, priv)
	parent, _, err := issueClient(t, issuer, testClientID)
	if err != nil {
		t.Fatal(err)
	}
	for asked, want := range map[string]string{"": testClientID, "client-2": "client-2"} {
		child, _, err := issuer.delegate(context.Background(), DelegateRequest{
			Subject: Principal{Type: PrincipalTypeAgent, Subject: "child"}, Parent: asVerified(parent), InheritCaveats: true, ClientID: asked,
		})
		if err != nil || child.ClientID != want {
			t.Errorf("asked %q: child client %q, %v; want %q", asked, child.ClientID, err, want)
		}
	}
	sealed, err := issuer.Biscuit(context.Background(), BiscuitRequest{Capability: asVerified(parent)})
	if err != nil {
		t.Fatal(err)
	}
	v := verifierFor(t, pub, VerifierConfig{Biscuit: &BiscuitConfig{Revocations: revLookup{}}})
	got, err := v.Verify(t.Context(), VerifyRequest{Token: sealed.Token, Audience: audienceData})
	if err != nil || got.Capability().ClientID != testClientID {
		t.Errorf("the Biscuit of a capability: client %q, %v", got.Capability().ClientID, err)
	}
}

// RFC 9068 requires sub: Issue and Delegate refuse a subject that names no
// one, and a verifier a token whose principal names no one.
func TestSubjectIsRequired(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	issuer := issuerOf(t, priv)
	_, _, err := issuer.issue(context.Background(), IssueRequest{
		IssuedBy: Principal{Subject: "op"}, Subject: Principal{Type: PrincipalTypeService, TenantID: uuid.New()},
		Audience: []string{audienceData}, Caveats: Caveats{Ops: []Op{OpGet}}, TTL: time.Hour, ClientID: testClientID,
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("Issue with no subject: %v, want ErrInvalidRequest", err)
	}
	parent, _, err := issueClient(t, issuer, testClientID)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = issuer.delegate(context.Background(), DelegateRequest{
		Parent: asVerified(parent), Subject: Principal{Type: PrincipalTypeAgent}, InheritCaveats: true,
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("Delegate to no subject: %v, want ErrInvalidRequest", err)
	}
	claims := validClaims(uuid.New())
	claims["sub"] = ""
	claims["limes_principal"].(map[string]any)["subject"] = ""
	token := resign(t, priv, map[string]any{"alg": jose.AlgEd25519, "kid": testKID, "typ": TokenType}, claims)
	if _, err := verifierFor(t, pub, VerifierConfig{}).Verify(t.Context(), VerifyRequest{Token: token, Audience: audienceData}); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("a token naming no subject: %v, want ErrInvalidToken", err)
	}
}
