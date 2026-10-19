package limes

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes/internal/jose"
)

// RFC 4648 §5, as RFC 7515 §2 uses it: every segment of a token is base64url
// with no padding, and has one spelling. A segment padded or in the standard
// alphabet is refused even when the signature covers exactly those bytes.
func TestVerifyReadsTokenSegmentsAsUnpaddedBase64URL(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	v := verifierFor(t, pub, VerifierConfig{})
	header, _ := json.Marshal(map[string]any{"alg": jose.AlgEd25519, "kid": "k1", "typ": TokenTypeV6})
	claims, _ := json.Marshal(validClaims(uuid.New()))
	// signedWith encodes header and claims with enc and signs exactly that
	// input, so only the encoding can fail.
	signedWith := func(enc *base64.Encoding) string {
		input := enc.EncodeToString(header) + "." + enc.EncodeToString(claims)
		return input + "." + enc.EncodeToString(ed25519.Sign(priv, []byte(input)))
	}
	unpadded := signedWith(base64.RawURLEncoding)
	if _, err := v.Verify(context.Background(), VerifyRequest{Token: unpadded, Audience: audienceData}); err != nil {
		t.Fatalf("unpadded base64url: %v", err)
	}
	if !strings.ContainsAny(signedWith(base64.URLEncoding), "=") || !strings.ContainsAny(signedWith(base64.StdEncoding), "+/=") {
		t.Fatal("the fixture would not tell the encodings apart")
	}
	for name, token := range map[string]string{
		"padded":            signedWith(base64.URLEncoding),
		"standard alphabet": signedWith(base64.StdEncoding),
	} {
		if _, err := v.Verify(context.Background(), VerifyRequest{Token: token, Audience: audienceData}); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("%s: Verify = %v, want ErrInvalidToken", name, err)
		}
	}
}

// What the module writes is unpadded base64url: every segment of a token.
func TestWritesUnpaddedBase64URL(t *testing.T) {
	issuer, _, _, _ := buildIssuerVerifier(t)
	_, token := issueFor(t, issuer)
	for i, seg := range strings.Split(token, ".") {
		if strings.ContainsAny(seg, "=+/") {
			t.Errorf("segment %d = %q, not unpadded base64url", i, seg)
		}
	}
}
