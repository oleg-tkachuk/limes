package limes

import (
	"context"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes/internal/jose"
)

// RFC 8725 §3.1: the verifier fixes the algorithm; the token's header does
// not choose it. A token naming any other — none, an HMAC keyed by the public
// key, another asymmetric one, or no alg at all — is refused, whatever its
// signature segment holds.
func TestVerifyFixesTheAlgorithm(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	v := verifierFor(t, pub, VerifierConfig{})
	claims := validClaims(uuid.New())
	signed := func(alg string, sign func(input string) []byte) string {
		header := map[string]any{"kid": "k1", "typ": TokenTypeV5}
		if alg != "" {
			header["alg"] = alg
		}
		h, _ := json.Marshal(header)
		c, _ := json.Marshal(claims)
		input := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(c)
		return input + "." + base64.RawURLEncoding.EncodeToString(sign(input))
	}
	ed := func(input string) []byte { return ed25519.Sign(priv, []byte(input)) }
	keyedByThePublicKey := func(input string) []byte {
		m := hmac.New(sha256.New, pub)
		m.Write([]byte(input))
		return m.Sum(nil)
	}
	unsigned := func(string) []byte { return nil }
	for name, token := range map[string]string{
		"none":                    signed("none", unsigned),
		"HS256 by the public key": signed("HS256", keyedByThePublicKey),
		"ES256":                   signed("ES256", ed),
		"RS256":                   signed("RS256", ed),
		"no alg":                  signed("", ed),
		"Ed25519 in lower case":   signed("ed25519", ed),
	} {
		if _, err := v.Verify(context.Background(), VerifyRequest{Token: token, Audience: audienceData}); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("%s: Verify = %v, want ErrInvalidToken", name, err)
		}
	}
	if _, err := v.Verify(context.Background(), VerifyRequest{Token: signed(jose.AlgEd25519, ed), Audience: audienceData}); err != nil {
		t.Fatalf("the same token naming %s: %v", jose.AlgEd25519, err)
	}
}
