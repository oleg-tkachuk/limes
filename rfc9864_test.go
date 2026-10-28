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
	c, token, err := issuer.issue(context.Background(), IssueRequest{ClientID: testClientID,
		IssuedBy: Principal{Subject: "op"}, Subject: Principal{Type: PrincipalTypeAgent, TenantID: uuid.New(), Subject: "a"},
		Audience: []string{audienceData}, Caveats: Caveats{Requests: RequestCeiling{Unlimited: true}, Ops: []Op{OpGet}}, TTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	return c, token
}

// A verifier reads a token under either name of Ed25519: the polymorphic
// EdDSA names the same algorithm over the same key.
func TestReadsEitherNameOfEd25519(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	v := verifierFor(t, pub, VerifierConfig{})
	for _, alg := range []SigningAlgorithm{SigningAlgorithmEd25519, SigningAlgorithmEdDSA} {
		tok := resign(t, priv, map[string]any{"alg": alg, "kid": testKID, "typ": TokenType}, validClaims(uuid.New()))
		if _, err := v.Verify(context.Background(), VerifyRequest{Token: tok, Audience: audienceData}); err != nil {
			t.Errorf("a token naming %s: %v", alg, err)
		}
	}
}

// A signer names the algorithm it is configured with, Ed25519 by default,
// and its tokens verify under either name.
func TestSignerWritesTheConfiguredAlgorithm(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	v := verifierFor(t, pub, VerifierConfig{TrustedIssuers: []string{goldenIssuer}})
	for configured, want := range map[SigningAlgorithm]SigningAlgorithm{
		"":                      SigningAlgorithmEd25519,
		SigningAlgorithmEd25519: SigningAlgorithmEd25519,
		SigningAlgorithmEdDSA:   SigningAlgorithmEdDSA,
	} {
		signer, err := NewSigner(SignerConfig{KeyID: testKID, Key: SealKey(priv), Algorithm: configured})
		if err != nil {
			t.Fatal(err)
		}
		issuer, err := NewIssuer(IssuerConfig{Signer: signer, Store: newMemStore(), IssuerName: goldenIssuer})
		if err != nil {
			t.Fatal(err)
		}
		_, token := issueFor(t, issuer)
		parts, err := jose.Split(token)
		if err != nil {
			t.Fatal(err)
		}
		if h, err := parseHeader(parts.Header); err != nil || SigningAlgorithm(h.Alg) != want {
			t.Errorf("Algorithm %q: token alg = %q, %v; want %q", configured, h.Alg, err, want)
		}
		if _, err := v.Verify(t.Context(), VerifyRequest{Token: token, Audience: audienceData}); err != nil {
			t.Errorf("Algorithm %q: %v", configured, err)
		}
	}
}

// A signer names no algorithm but Ed25519's two names: another would sign
// tokens no verifier reads, or claim a signature the key does not make.
func TestNewSignerRefusesAnotherAlgorithm(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	for _, alg := range []SigningAlgorithm{"none", jose.AlgES256, "eddsa", "ed25519"} {
		if _, err := NewSigner(SignerConfig{KeyID: testKID, Key: SealKey(priv), Algorithm: alg}); err == nil {
			t.Errorf("NewSigner accepted Algorithm %q", alg)
		}
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
