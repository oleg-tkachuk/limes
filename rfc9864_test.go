package limes

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"uuid"

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
	c, token, err := issuer.issue(context.Background(), IssueRequest{
		IssuedBy: Principal{Subject: "op"}, Subject: Principal{Type: PrincipalTypeAgent, TenantID: uuid.New(), Subject: "a"},
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
	tok := resign(t, priv, map[string]any{"alg": jose.AlgEdDSA, "kid": "k1", "typ": TokenTypeV6}, validClaims(uuid.New()))
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

// A token has one spelling: changing the spare bits of its signature's last
// character makes a different string that must not verify as the same token.
func TestATokenHasOneSpelling(t *testing.T) {
	issuer, v, _, _ := buildIssuerVerifier(t)
	_, token := issueFor(t, issuer)
	if _, err := v.Verify(t.Context(), VerifyRequest{Token: token, Audience: audienceData}); err != nil {
		t.Fatal(err)
	}
	// An Ed25519 signature is 64 bytes, 86 characters with 4 spare bits:
	// flipping the lowest bit of the last character keeps the bytes the
	// same for a lenient decoder.
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	last := strings.IndexByte(alphabet, token[len(token)-1])
	altered := token[:len(token)-1] + string(alphabet[last^1])
	if _, err := v.Verify(t.Context(), VerifyRequest{Token: altered, Audience: audienceData}); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("a token with a spare bit of its signature set: err = %v, want ErrInvalidToken", err)
	}
}
