package jwks

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
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

// A document with no "keys" array is refused (RFC 7517 §5): only an empty
// array withdraws every key.
func TestParseRequiresAKeysArray(t *testing.T) {
	for _, raw := range []string{`null`, `{}`, `{"keys":null}`} {
		if keys, err := Parse([]byte(raw)); err == nil || keys != nil {
			t.Errorf("Parse(%s) = %v, %v; want an error", raw, keys, err)
		}
	}
	if keys, err := Parse([]byte(`{"keys":[]}`)); err != nil || keys == nil || len(keys) != 0 {
		t.Errorf(`Parse({"keys":[]}) = %v, %v; want an empty set`, keys, err)
	}
}

// A key set is read with JSON's own rules — names in their exact case — and
// a kid named twice is used for neither key: which one signs is not for the
// reader to guess.
func TestParseJWKSIsStrict(t *testing.T) {
	_, a, _, _ := generateKeypair()
	_, b, _, _ := generateKeypair()
	upper := fmt.Sprintf(`{"KEYS":[{"KTY":"OKP","CRV":"Ed25519","KID":"k","X":%q}]}`, jose.B64(a))
	// "KEYS" is not "keys", so the document has no key set at all.
	if keys, err := Parse([]byte(upper)); err == nil || len(keys) != 0 {
		t.Errorf("members in another case = %v, %v; want no key and an error", keys, err)
	}
	dup := fmt.Sprintf(`{"keys":[{"kty":"OKP","crv":"Ed25519","kid":"k","x":%q},{"kty":"OKP","crv":"Ed25519","kid":"k","x":%q},{"kty":"OKP","crv":"Ed25519","kid":"other","x":%q}]}`, jose.B64(a), jose.B64(b), jose.B64(b))
	// Both are counted as skipped: a set holding only such keys is broken,
	// not a withdrawal of every key.
	keys, skipped, err := parse([]byte(dup))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := keys["k"]; ok || !keys["other"].Equal(b) || skipped != 2 {
		t.Errorf("a kid named twice = %v, %d skipped; want it dropped, both skipped, and the rest kept", keys, skipped)
	}
	// A member named twice is refused in an entry as in the document.
	twice := fmt.Sprintf(`{"keys":[{"kty":"OKP","kty":"OKP","crv":"Ed25519","kid":"k","x":%q}]}`, jose.B64(a))
	if keys, err := Parse([]byte(twice)); err == nil || keys != nil {
		t.Errorf("an entry naming a member twice = %v, %v; want an error", keys, err)
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
	if err := json.Unmarshal(doc, &set); err != nil || set.Keys == nil || (*set.Keys)[0].Alg != jose.AlgEd25519 {
		t.Errorf("JWKS alg = %+v, %v; want %q", set, err, jose.AlgEd25519)
	}

}

// A key set refuses, at once, a key the verifier could never use: an empty
// kid or a key of the wrong length would otherwise surface token by token as
// the tokens' fault.
func TestStaticRefusesAKeyItCannotServe(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, keys := range map[string]map[string]ed25519.PublicKey{
		"an empty kid":  {"": pub},
		"a short key":   {"k": pub[:ed25519.PublicKeySize-1]},
		"no key at all": {"k": nil},
	} {
		if _, err := NewStatic(StaticConfig{Keys: keys}); err == nil {
			t.Errorf("NewStatic with %s: built", name)
		}
	}
	s, err := NewStatic(StaticConfig{Keys: map[string]ed25519.PublicKey{"k": pub}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetKey("short", pub[:1]); err == nil {
		t.Error("SetKey accepted a short key")
	}
	if _, err := s.PublicKey(context.Background(), "short"); !errors.Is(err, ErrUnknownKID) {
		t.Errorf("a refused key is served: %v", err)
	}
	// The resolver keeps its own copy of each key.
	pub[0] ^= 1
	got, err := s.PublicKey(context.Background(), "k")
	if err != nil || got[0] == pub[0] {
		t.Error("changing the caller's key changed the one the resolver serves")
	}
}

// An entry of a shape the reader does not support — a member of another JSON
// type, or no object at all — is skipped, not taken as the whole set's fault
// (RFC 7517 §5), and a kid shared with an entry that is no usable Ed25519 key
// still names its key: §4.5 allows one kid across key types.
func TestParseSkipsAnEntryOfTheWrongShape(t *testing.T) {
	kid, pub, _, err := generateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	valid := fmt.Sprintf(`{"kty":"OKP","crv":"Ed25519","kid":%q,"x":%q}`, kid, jose.B64(pub))
	for name, other := range map[string]string{
		"a numeric kid":         `{"kty":"RSA","kid":5}`,
		"a numeric x":           `{"kty":"OKP","crv":"Ed25519","kid":"n","x":1}`,
		"a bare number":         `1`,
		"a string":              `"key"`,
		"an array":              `[]`,
		"null":                  `null`,
		"the kid of an RSA key": fmt.Sprintf(`{"kty":"RSA","kid":%q,"n":"abc","e":"AQAB"}`, kid),
		"the kid of a bad key":  fmt.Sprintf(`{"kty":"OKP","crv":"Ed25519","kid":%q,"x":"AAAA"}`, kid),
		// A key of the right length on another curve, or under another kty.
		"the kid of an X25519 key":   fmt.Sprintf(`{"kty":"OKP","crv":"X25519","kid":%q,"x":%q}`, kid, jose.B64(pub)),
		"the kid of an EC-typed key": fmt.Sprintf(`{"kty":"EC","crv":"Ed25519","kid":%q,"x":%q}`, kid, jose.B64(pub)),
	} {
		keys, skipped, err := parse([]byte(`{"keys":[` + other + `,` + valid + `]}`))
		if err != nil || len(keys) != 1 || !keys[kid].Equal(pub) || skipped != 1 {
			t.Errorf("beside %s: %v, %d skipped, %v; want the Ed25519 key and one skipped", name, keys, skipped, err)
		}
	}
}

// An entry is used to verify signatures only when its use and key_ops allow
// it (RFC 7517 §4.2, §4.3): a key published for another purpose is skipped.
func TestParseHonoursUseAndKeyOps(t *testing.T) {
	kid, pub, _, err := generateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		members string
		usable  bool
	}{
		"neither":                 {``, true},
		"use sig":                 {`,"use":"sig"`, true},
		"use enc":                 {`,"use":"enc"`, false},
		"a use of its own":        {`,"use":"other"`, false},
		"key_ops verify":          {`,"key_ops":["verify"]`, true},
		"key_ops among others":    {`,"key_ops":["sign","verify"]`, true},
		"key_ops sign":            {`,"key_ops":["sign"]`, false},
		"key_ops encrypt":         {`,"key_ops":["encrypt"]`, false},
		"key_ops empty":           {`,"key_ops":[]`, false},
		"key_ops null":            {`,"key_ops":null`, true},
		"use sig, key_ops sign":   {`,"use":"sig","key_ops":["sign"]`, false},
		"use enc, key_ops verify": {`,"use":"enc","key_ops":["verify"]`, false},
	} {
		doc := fmt.Sprintf(`{"keys":[{"kty":"OKP","crv":"Ed25519","kid":%q,"x":%q%s}]}`, kid, jose.B64(pub), tc.members)
		keys, err := Parse([]byte(doc))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := keys[kid].Equal(pub); got != tc.usable {
			t.Errorf("%s: key used = %v, want %v", name, got, tc.usable)
		}
	}
}
