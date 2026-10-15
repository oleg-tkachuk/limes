package limes

import (
	"fmt"
	"strings"
	"testing"
)

// verbsThatShowBytes are the verbs under which fmt would print a private
// key's bytes, in one notation or another, from a value that holds it.
var verbsThatShowBytes = []string{"%v", "%+v", "%#v", "%s", "%x", "%X", "%q", "%d"}

// seedPrefixBytes is how many leading bytes of the seed identify it in
// formatted text: enough that no kid or public key repeats them by chance.
const seedPrefixBytes = 8

// spelling is how fmt writes b under verb, without the brackets, braces or
// quotes around a slice, so it is found inside a longer slice's spelling.
func spelling(verb string, b []byte) string {
	s := fmt.Sprintf(verb, b)
	s = strings.TrimPrefix(s, "[]byte{")
	return strings.Trim(s, `[]{}"`)
}

// A value that holds a private key formats without it, under every verb,
// itself or through a pointer, and still shows its kid.
func TestFormattingRedactsPrivateKeys(t *testing.T) {
	key, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	signer, err := NewSigner(SignerConfig{KeyID: key.KeyID, Key: key.Private})
	if err != nil {
		t.Fatal(err)
	}
	seed := key.Private.Seed()[:seedPrefixBytes]
	for name, v := range map[string]any{
		"SigningKey":    key,
		"*SigningKey":   &key,
		"SignerConfig":  SignerConfig{KeyID: key.KeyID, Key: key.Private},
		"*SignerConfig": &SignerConfig{KeyID: key.KeyID, Key: key.Private},
		"*Signer":       signer,
	} {
		for _, verb := range verbsThatShowBytes {
			got := fmt.Sprintf(verb, v)
			if strings.Contains(got, spelling(verb, seed)) {
				t.Errorf("%s under %s = %q: carries the private key", name, verb, got)
			}
		}
		got := fmt.Sprintf("%v", v)
		if !strings.Contains(got, key.KeyID) || !strings.Contains(got, redactedKeyText) {
			t.Errorf("%s under %%v = %q: want its kid and %s", name, got, redactedKeyText)
		}
	}
}

// The check above would see a key that is printed: without the redaction,
// the same values carry it.
func TestSpellingFindsAnUnredactedKey(t *testing.T) {
	key, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	seed := key.Private.Seed()[:seedPrefixBytes]
	for _, verb := range verbsThatShowBytes {
		if got := fmt.Sprintf(verb, []byte(key.Private)); !strings.Contains(got, spelling(verb, seed)) {
			t.Errorf("the bare key under %s = %q: its seed's spelling %q is not found", verb, got, spelling(verb, seed))
		}
	}
}

// The public half still prints: it is what an operator matches against a
// key set.
func TestFormattingKeepsThePublicKey(t *testing.T) {
	key, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := fmt.Sprintf("%x", key), fmt.Sprintf("%x", []byte(key.Public)); !strings.Contains(got, want) {
		t.Errorf("SigningKey under %%x = %q, want the public key %s in it", got, want)
	}
}
