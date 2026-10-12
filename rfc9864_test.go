package limes

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes/internal/jose"
	"github.com/oleg-tkachuk/limes/jwks"
)

// RFC 9864 deprecates the polymorphic "EdDSA": everything this module writes
// names Ed25519, and a capability token naming anything else is refused.
func TestWritesTheFullySpecifiedAlgorithm(t *testing.T) {
	issuer, _, _, _ := buildIssuerVerifier(t)
	_, token := issueFor(t, issuer)
	parts, err := jose.Split(token)
	if err != nil {
		t.Fatal(err)
	}
	h, err := parseHeader(parts.Header)
	if err != nil || h.Alg != jose.AlgEd25519 {
		t.Errorf("token alg = %q, %v; want %q", h.Alg, err, jose.AlgEd25519)
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
	tok := resign(t, priv, map[string]any{"alg": jose.AlgEdDSA, "kid": "k1", "typ": TokenTypeV3}, validClaims(uuid.New()))
	if _, err := v.Verify(context.Background(), VerifyRequest{Token: tok, Audience: audienceData}); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("a token naming EdDSA: err = %v, want ErrInvalidToken", err)
	}
}

// A JWKS entry or a DPoP proof from a tool that predates RFC 9864 still
// names EdDSA; both are read.
func TestReadsTheDeprecatedAlgorithmFromOthers(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(nil)
	doc := fmt.Sprintf(`{"keys":[{"kty":"OKP","crv":"Ed25519","kid":"old","alg":"EdDSA","x":%q}]}`, jose.B64(pub))
	keys, err := jwks.Parse([]byte(doc))
	if err != nil || !keys["old"].Equal(pub) {
		t.Errorf("a JWKS entry naming EdDSA = %v, %v; want it read", keys, err)
	}
}
