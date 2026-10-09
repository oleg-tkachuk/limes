package limes

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
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
