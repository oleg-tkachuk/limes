package jwks

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/oleg-tkachuk/limes/internal/jose"
)

// TestJWKS_RoundTrip confirms Marshal produces something Parse
// reads back into the same kid → key mapping.
func TestJWKS_RoundTrip(t *testing.T) {
	t.Parallel()
	pub1, _, _ := ed25519.GenerateKey(rand.Reader)
	pub2, _, _ := ed25519.GenerateKey(rand.Reader)

	in := map[string]ed25519.PublicKey{
		"k1": pub1,
		"k2": pub2,
	}
	raw, err := Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out, err := Parse(raw)
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
	out, err := Parse(raw)
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
	_, a, _, _ := generateKeypair()
	_, b, _, _ := generateKeypair()
	upper := fmt.Sprintf(`{"KEYS":[{"KTY":"OKP","CRV":"Ed25519","KID":"k","X":%q}]}`, jose.B64(a))
	if keys, err := Parse([]byte(upper)); err != nil || len(keys) != 0 {
		t.Errorf("members in another case = %v, %v; want no key", keys, err)
	}
	dup := fmt.Sprintf(`{"keys":[{"kty":"OKP","crv":"Ed25519","kid":"k","x":%q},{"kty":"OKP","crv":"Ed25519","kid":"k","x":%q},{"kty":"OKP","crv":"Ed25519","kid":"other","x":%q}]}`, jose.B64(a), jose.B64(b), jose.B64(b))
	keys, err := Parse([]byte(dup))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := keys["k"]; ok || !keys["other"].Equal(b) {
		t.Errorf("a kid named twice = %v; want it dropped and the rest kept", keys)
	}
}

func TestMarshalJWKSIsDeterministic(t *testing.T) {
	keys := map[string]ed25519.PublicKey{}
	for range 8 {
		kid, pub, _, _ := generateKeypair()
		keys[kid] = pub
	}
	first, err := Marshal(keys)
	if err != nil {
		t.Fatal(err)
	}
	for range 10 {
		again, _ := Marshal(keys)
		if string(again) != string(first) {
			t.Fatal("Marshal output differs between calls")
		}
	}
}

func TestParseJWKSSkipsMalformedEntries(t *testing.T) {
	kid, pub, _, _ := generateKeypair()
	doc := `{"keys":[
		{"kty":"OKP","crv":"Ed25519","kid":"broken","x":"!!!"},
		{"kty":"OKP","crv":"Ed25519","kid":"short","x":"AAAA"},
		{"kty":"OKP","crv":"Ed25519","kid":"` + kid + `","x":"` + base64.RawURLEncoding.EncodeToString(pub) + `"}
	]}`
	keys, err := Parse([]byte(doc))
	if err != nil {
		t.Fatalf("jwks.Parse: %v", err)
	}
	if len(keys) != 1 || !keys[kid].Equal(pub) {
		t.Fatalf("keys = %v, want only the valid entry", keys)
	}
}

// generateKeypair is a fresh Ed25519 key under its derived kid.
func generateKeypair() (string, ed25519.PublicKey, ed25519.PrivateKey, error) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		return "", nil, nil, err
	}
	return KeyID(pub), pub, priv, nil
}

// A key set names its keys' algorithm as RFC 9864 fully specifies it.
func TestMarshalWritesTheFullySpecifiedAlgorithm(t *testing.T) {
	_, pub, _, err := generateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	doc, err := Marshal(map[string]ed25519.PublicKey{"k1": pub})
	if err != nil {
		t.Fatal(err)
	}
	var set document
	if err := json.Unmarshal(doc, &set); err != nil || set.Keys[0].Alg != jose.AlgEd25519 {
		t.Errorf("JWKS alg = %+v, %v; want %q", set, err, jose.AlgEd25519)
	}

}
