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

// testClientID is the OAuth client the v7 tests' capabilities name.
const testClientID = "client-1"

// issuerOf is an issuer signing tokens of typ with priv, recording to a
// store of its own.
func issuerOf(t *testing.T, priv ed25519.PrivateKey, typ TokenType) *Issuer {
	t.Helper()
	signer, err := NewSigner(SignerConfig{KeyID: "k1", Key: SealKey(priv), TokenType: typ})
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

// A v7 token is an RFC 9068 access token: typ at+jwt, its client in
// client_id and its format in limes_v; a verifier reads it, client and all.
func TestV7IsAnAccessToken(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	_, token, err := issueClient(t, issuerOf(t, priv, TokenTypeV7), testClientID)
	if err != nil {
		t.Fatal(err)
	}
	parts, err := jose.Split(token)
	if err != nil {
		t.Fatal(err)
	}
	h, err := parseHeader(parts.Header)
	if err != nil || TokenType(h.Typ) != TokenTypeV7 {
		t.Errorf("typ = %q, %v; want %q", h.Typ, err, TokenTypeV7)
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
	only6 := verifierFor(t, pub, VerifierConfig{AcceptTokenTypes: []TokenType{TokenTypeV6}})
	if _, err := only6.Verify(t.Context(), VerifyRequest{Token: token, Audience: audienceData}); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("a verifier of v6 alone: %v, want ErrInvalidToken", err)
	}
}

// RFC 9068 requires client_id: a v7 signer invents none. A v6 token has no
// member for it, and leaves it out.
func TestClientIDByFormat(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	if _, _, err := issueClient(t, issuerOf(t, priv, TokenTypeV7), ""); err == nil {
		t.Error("a v7 capability with no client was signed")
	}
	_, token, err := issueClient(t, issuerOf(t, priv, TokenTypeV6), testClientID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := verifierFor(t, pub, VerifierConfig{}).Verify(t.Context(), VerifyRequest{Token: token, Audience: audienceData})
	if err != nil || got.Capability().ClientID != "" {
		t.Errorf("a v6 token reads client %q, %v; want none", got.Capability().ClientID, err)
	}
}

// Each format's claims are its own: an at+jwt is read only with limes_v 7
// and a client_id, so an access token of another issuer's making, or a v6
// payload under the type, is refused; and a v6 token carries neither member.
func TestEachFormatHasItsOwnClaims(t *testing.T) {
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
	v7 := map[string]any{"client_id": testClientID, "limes_v": formatV7}
	header := func(typ TokenType) map[string]any {
		return map[string]any{"alg": jose.AlgEd25519, "kid": "k1", "typ": typ}
	}
	accepted := resign(t, priv, header(TokenTypeV7), with(v7))
	if _, err := v.Verify(t.Context(), VerifyRequest{Token: accepted, Audience: audienceData}); err != nil {
		t.Fatalf("a v7 token: %v", err)
	}
	for name, token := range map[string]string{
		"a v6 payload as at+jwt":       resign(t, priv, header(TokenTypeV7), with(nil)),
		"at+jwt with no limes_v":       resign(t, priv, header(TokenTypeV7), with(map[string]any{"client_id": testClientID})),
		"at+jwt of another format":     resign(t, priv, header(TokenTypeV7), with(map[string]any{"client_id": testClientID, "limes_v": formatV7 + 1})),
		"at+jwt with no client_id":     resign(t, priv, header(TokenTypeV7), with(map[string]any{"limes_v": formatV7})),
		"at+jwt with an empty client":  resign(t, priv, header(TokenTypeV7), with(map[string]any{"client_id": "", "limes_v": formatV7})),
		"a v6 token with a client_id":  resign(t, priv, header(TokenTypeV6), with(map[string]any{"client_id": testClientID})),
		"a v6 token with a limes_v":    resign(t, priv, header(TokenTypeV6), with(map[string]any{"limes_v": formatV7})),
		"at+jwt with an unknown claim": resign(t, priv, header(TokenTypeV7), with(map[string]any{"client_id": testClientID, "limes_v": formatV7, "scope": "get"})),
	} {
		if _, err := v.Verify(t.Context(), VerifyRequest{Token: token, Audience: audienceData}); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("%s: %v, want ErrInvalidToken", name, err)
		}
	}
}

// A child names its parent's client unless asked for another, and a v7
// Biscuit seals a v7 token that verifies.
func TestV7DelegatesAndSeals(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	issuer := issuerOf(t, priv, TokenTypeV7)
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
		t.Errorf("the Biscuit of a v7 capability: client %q, %v", got.Capability().ClientID, err)
	}
}
