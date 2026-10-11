package limes

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"testing"

	"github.com/oleg-tkachuk/limes/internal/jose"
)

// TestJWKS_RoundTrip confirms MarshalJWKS produces something ParseJWKS
// reads back into the same kid → key mapping.
func TestJWKS_RoundTrip(t *testing.T) {
	t.Parallel()
	pub1, _, _ := ed25519.GenerateKey(rand.Reader)
	pub2, _, _ := ed25519.GenerateKey(rand.Reader)

	in := map[string]ed25519.PublicKey{
		"k1": pub1,
		"k2": pub2,
	}
	raw, err := MarshalJWKS(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out, err := ParseJWKS(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(out))
	}
	for kid, pub := range in {
		got, ok := out[kid]
		if !ok {
			t.Errorf("kid %q missing in parsed JWKS", kid)
			continue
		}
		if !pub.Equal(got) {
			t.Errorf("kid %q key mismatch", kid)
		}
	}
}

// TestJWKS_SkipsNonEd25519 confirms unknown kty/crv entries are dropped
// during parse — keeps backward compatibility when future cipher
// suites land in the same document.
func TestJWKS_SkipsNonEd25519(t *testing.T) {
	t.Parallel()
	raw := []byte(`{"keys":[
        {"kty":"RSA","kid":"old","n":"abc","e":"AQAB"},
        {"kty":"OKP","crv":"X25519","kid":"unrelated","x":"abc"}
    ]}`)
	out, err := ParseJWKS(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("expected 0 entries (all non-Ed25519), got %d", len(out))
	}
}

// A key set is read with JSON's own rules — names in their exact case — and
// a kid named twice is used for neither key: which one signs is not for the
// reader to guess.
func TestParseJWKSIsStrict(t *testing.T) {
	_, a, _, _ := GenerateEd25519Keypair()
	_, b, _, _ := GenerateEd25519Keypair()
	upper := fmt.Sprintf(`{"KEYS":[{"KTY":"OKP","CRV":"Ed25519","KID":"k","X":%q}]}`, jose.B64(a))
	if keys, err := ParseJWKS([]byte(upper)); err != nil || len(keys) != 0 {
		t.Errorf("members in another case = %v, %v; want no key", keys, err)
	}
	dup := fmt.Sprintf(`{"keys":[{"kty":"OKP","crv":"Ed25519","kid":"k","x":%q},{"kty":"OKP","crv":"Ed25519","kid":"k","x":%q},{"kty":"OKP","crv":"Ed25519","kid":"other","x":%q}]}`, jose.B64(a), jose.B64(b), jose.B64(b))
	keys, err := ParseJWKS([]byte(dup))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := keys["k"]; ok || !keys["other"].Equal(b) {
		t.Errorf("a kid named twice = %v; want it dropped and the rest kept", keys)
	}
}
