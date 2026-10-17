package limes

import (
	"crypto"
	"crypto/ed25519"
	"fmt"
	"strings"
	"testing"
)

// verbsThatShowBytes are the verbs under which fmt would print a private
// key's bytes, in one notation or another, from a value that holds it.
var verbsThatShowBytes = []string{"%v", "%+v", "%#v", "%s", "%x", "%X", "%q", "%d"}

// keySpelling is how fmt writes key under verb. A value that holds the key
// in a field writes the field the same way, so finding this in the value's
// text finds the key; a prefix of the key would not do, as %q joins bytes
// into runes across the prefix's end.
func keySpelling(verb string, key ed25519.PrivateKey) string { return fmt.Sprintf(verb, key) }

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
	for name, v := range map[string]any{
		"SigningKey":    key,
		"*SigningKey":   &key,
		"SignerConfig":  SignerConfig{KeyID: key.KeyID, Key: key.Private},
		"*SignerConfig": &SignerConfig{KeyID: key.KeyID, Key: key.Private},
		"*Signer":       signer,
	} {
		for _, verb := range verbsThatShowBytes {
			got := fmt.Sprintf(verb, v)
			if strings.Contains(got, keySpelling(verb, key.Private)) {
				t.Errorf("%s under %s = %q: carries the private key", name, verb, got)
			}
		}
		got := fmt.Sprintf("%v", v)
		if !strings.Contains(got, key.KeyID) || !strings.Contains(got, redactedKeyText) {
			t.Errorf("%s under %%v = %q: want its kid and %s", name, got, redactedKeyText)
		}
	}
}

// The check above would see a key that is printed: a value holding the key
// with no redaction, in a field of its own type or of an interface, carries
// its spelling under every verb.
func TestKeySpellingFindsAnUnredactedKey(t *testing.T) {
	key, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	for name, v := range map[string]any{
		"a key field":      struct{ Key ed25519.PrivateKey }{key.Private},
		"a crypto.Signer":  struct{ Key crypto.Signer }{key.Private},
		"behind a pointer": &struct{ Key ed25519.PrivateKey }{key.Private},
	} {
		for _, verb := range verbsThatShowBytes {
			if got := fmt.Sprintf(verb, v); !strings.Contains(got, keySpelling(verb, key.Private)) {
				t.Errorf("%s under %s = %q: the key's spelling is not found", name, verb, got)
			}
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
	if got, want := fmt.Sprintf("%x", key), fmt.Sprintf("%x", []byte(key.Public())); !strings.Contains(got, want) {
		t.Errorf("SigningKey under %%x = %q, want the public key %s in it", got, want)
	}
}
