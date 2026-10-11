package limes

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
)

// RFC 9864 deprecates the polymorphic "EdDSA": everything this module writes
// names Ed25519, and a capability token naming anything else is refused.
func TestWritesTheFullySpecifiedAlgorithm(t *testing.T) {
	issuer, _, _, pub := buildIssuerVerifier(t)
	_, token := issueFor(t, issuer)
	parts, err := splitToken(token)
	if err != nil {
		t.Fatal(err)
	}
	h, err := parseHeader(parts.header)
	if err != nil || h.Alg != algEd25519 {
		t.Errorf("token alg = %q, %v; want %q", h.Alg, err, algEd25519)
	}

	doc, err := MarshalJWKS(map[string]ed25519.PublicKey{"k1": pub})
	if err != nil {
		t.Fatal(err)
	}
	var set JWKSDocument
	if err := json.Unmarshal(doc, &set); err != nil || set.Keys[0].Alg != algEd25519 {
		t.Errorf("JWKS alg = %+v, %v; want %q", set, err, algEd25519)
	}

	key := dpopKeys(t)["Ed25519"]
	proof, err := NewDPoPProof(key, DPoPProofRequest{Method: dpopMethod, URL: dpopURL, Token: token, Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	pp, _ := splitToken(proof)
	var ph dpopHeader
	if err := decodeSegment(pp.header, &ph); err != nil || ph.Alg != algEd25519 {
		t.Errorf("proof alg = %q, %v; want %q", ph.Alg, err, algEd25519)
	}
}

func issueFor(t *testing.T, issuer *Issuer) (Capability, string) {
	t.Helper()
	c, token, err := issuer.Issue(context.Background(), IssueRequest{
		IssuedBy: Principal{Subject: "op"}, Subject: Principal{Type: PrincipalAgent, TenantID: uuid.New(), Subject: "a"},
		Audience: []string{audienceData}, Caveats: Caveats{Ops: []Op{OpGet}}, TTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	return c, token
}

func TestCapabilityTokenRefusesThePolymorphicAlgorithm(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	v := verifierFor(t, pub, VerifierConfig{})
	tok := resign(t, priv, map[string]any{"alg": algEdDSA, "kid": "k1", "typ": TokenType}, validClaims(uuid.New()))
	if _, err := v.Verify(context.Background(), VerifyRequest{Token: tok, Audience: audienceData}); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("a token naming EdDSA: err = %v, want ErrInvalidSignature", err)
	}
}

// A JWKS entry or a DPoP proof from a tool that predates RFC 9864 still
// names EdDSA; both are read.
func TestReadsTheDeprecatedAlgorithmFromOthers(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(nil)
	doc := fmt.Sprintf(`{"keys":[{"kty":"OKP","crv":"Ed25519","kid":"old","alg":"EdDSA","x":%q}]}`, b64(pub))
	keys, err := ParseJWKS([]byte(doc))
	if err != nil || !keys["old"].Equal(pub) {
		t.Errorf("a JWKS entry naming EdDSA = %v, %v; want it read", keys, err)
	}
	_, priv, _ := ed25519.GenerateKey(nil)
	if !verifyDPoPSignature(priv.Public(), algEdDSA, []byte("in"), ed25519.Sign(priv, []byte("in"))) {
		t.Error("a proof signed under EdDSA was refused")
	}
}
