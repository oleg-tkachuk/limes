package limes

import (
	"bytes"
	"crypto"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"
)

// rawKey is the Ed25519 private key k seals, for a test that looks for its
// bytes.
func rawKey(k SealedKey) ed25519.PrivateKey {
	p, _ := k.signer().(ed25519.PrivateKey)
	return p
}

// verbsThatShowBytes are the verbs under which fmt would print a private
// key's bytes, in one notation or another, from a value that holds it.
var verbsThatShowBytes = []string{"%v", "%+v", "%#v", "%s", "%x", "%X", "%q", "%d", "%t"}

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
			if strings.Contains(got, keySpelling(verb, rawKey(key.Private))) {
				t.Errorf("%s under %s = %q: carries the private key", name, verb, got)
			}
		}
		got := fmt.Sprintf("%v", v)
		if !strings.Contains(got, key.KeyID) || !strings.Contains(got, redactedKeyText) {
			t.Errorf("%s under %%v = %q: want its kid and %s", name, got, redactedKeyText)
		}
	}
}

// A value that holds a Signer where fmt cannot call its Format — an Issuer,
// or any struct keeping one in an unexported field — formats without the
// key under every verb: under one a pointer does not take, fmt prints the
// pointer's target field by field, which reaches the key unless it is out of
// that walk.
func TestCarriersOfASignerLeaveItsKeyOut(t *testing.T) {
	key, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	signer, err := NewSigner(SignerConfig{KeyID: key.KeyID, Key: key.Private})
	if err != nil {
		t.Fatal(err)
	}
	issuer, err := NewIssuer(IssuerConfig{Signer: signer, Store: genStore{}, IssuerName: goldenIssuer})
	if err != nil {
		t.Fatal(err)
	}
	for name, v := range map[string]any{
		"*Issuer":                     issuer,
		"Issuer":                      *issuer,
		"a struct with an Issuer":     struct{ issuer *Issuer }{issuer},
		"a struct with a Signer":      struct{ signer *Signer }{signer},
		"a struct with a Signer copy": struct{ signer Signer }{*signer},
	} {
		for _, verb := range verbsThatShowBytes {
			got := fmt.Sprintf(verb, v)
			// fmt prints a field it reaches this way under %v, whatever
			// the verb, so the key is looked for in every spelling.
			for _, spelling := range verbsThatShowBytes {
				if strings.Contains(got, keySpelling(spelling, rawKey(key.Private))) {
					t.Errorf("%s under %s = %q: carries the private key", name, verb, got)
				}
			}
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
		"a key field":      struct{ Key ed25519.PrivateKey }{rawKey(key.Private)},
		"a crypto.Signer":  struct{ Key crypto.Signer }{rawKey(key.Private)},
		"behind a pointer": &struct{ Key ed25519.PrivateKey }{rawKey(key.Private)},
	} {
		for _, verb := range verbsThatShowBytes {
			if got := fmt.Sprintf(verb, v); !strings.Contains(got, keySpelling(verb, rawKey(key.Private))) {
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

// A value that holds a private key also leaves it out of encoding/json and
// of either slog handler, the two other ways a key reaches a log.
func TestEncodingRedactsPrivateKeys(t *testing.T) {
	key, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	spellings := []string{base64.StdEncoding.EncodeToString(rawKey(key.Private)), fmt.Sprintf("%x", []byte(rawKey(key.Private)))}
	for name, v := range map[string]any{
		"SigningKey":    key,
		"*SigningKey":   &key,
		"SignerConfig":  SignerConfig{KeyID: key.KeyID, Key: key.Private},
		"*SignerConfig": &SignerConfig{KeyID: key.KeyID, Key: key.Private},
	} {
		encoded, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("%s: json.Marshal: %v", name, err)
		}
		var text, structured bytes.Buffer
		slog.New(slog.NewTextHandler(&text, nil)).Info("key", "value", v)
		slog.New(slog.NewJSONHandler(&structured, nil)).Info("key", "value", v)
		for how, got := range map[string]string{"json.Marshal": string(encoded), "slog text": text.String(), "slog JSON": structured.String()} {
			for _, spelling := range spellings {
				if strings.Contains(got, spelling) {
					t.Errorf("%s under %s = %s: carries the private key", name, how, got)
				}
			}
			if !strings.Contains(got, key.KeyID) || !strings.Contains(got, redactedKeyText) {
				t.Errorf("%s under %s = %s: want its kid and %s", name, how, got, redactedKeyText)
			}
		}
	}
}

// A value that holds a key in a field of its own type — SigningKey,
// SignerConfig — formats without it however it is reached: under %p, which
// fmt prints field by field, and from an unexported field of a struct, where
// fmt cannot call its Format, in fmt and in slog's text handler alike.
func TestKeyHoldersLeaveTheKeyOutEverywhere(t *testing.T) {
	key, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	raw := rawKey(key.Private)
	cfg := SignerConfig{KeyID: key.KeyID, Key: key.Private}
	verbs := append(slices.Clone(verbsThatShowBytes), "%p")
	for name, v := range map[string]any{
		"SigningKey":                 key,
		"SignerConfig":               cfg,
		"SealedKey":                  key.Private,
		"a struct with a SigningKey": struct{ key SigningKey }{key},
		"a struct with a config":     struct{ cfg SignerConfig }{cfg},
		"a struct with a SealedKey":  struct{ k SealedKey }{key.Private},
	} {
		for _, verb := range verbs {
			got := fmt.Sprintf(verb, v)
			for _, spelling := range verbsThatShowBytes {
				if strings.Contains(got, keySpelling(spelling, raw)) {
					t.Errorf("%s under %s = %q: carries the private key", name, verb, got)
				}
			}
		}
		var text bytes.Buffer
		slog.New(slog.NewTextHandler(&text, nil)).Info("key", "value", v)
		for _, spelling := range verbsThatShowBytes {
			if strings.Contains(text.String(), keySpelling(spelling, raw)) {
				t.Errorf("%s through slog's text handler = %s: carries the private key", name, text.String())
			}
		}
	}
}

// A SealedKey signs and names its public key as the key it seals does; one
// holding no key, or an Ed25519 key of the wrong length, has neither, and
// NewSigner refuses it.
func TestSealedKey(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	k := SealKey(priv)
	if got, ok := k.Public().(ed25519.PublicKey); !ok || !got.Equal(pub) {
		t.Errorf("Public = %v, want the sealed key's", k.Public())
	}
	msg := []byte("message")
	sig, err := k.Sign(nil, msg, crypto.Hash(0))
	if err != nil || !ed25519.Verify(pub, msg, sig) {
		t.Errorf("Sign = %v; the signature does not verify", err)
	}
	for name, bad := range map[string]SealedKey{"the zero SealedKey": {}, "a nil key": SealKey(nil), "a short key": SealKey(priv[:ed25519.SeedSize])} {
		if bad.Public() != nil {
			t.Errorf("%s: Public = %v, want nil", name, bad.Public())
		}
		if _, err := bad.Sign(nil, msg, crypto.Hash(0)); err == nil {
			t.Errorf("%s: Sign succeeded", name)
		}
		if _, err := NewSigner(SignerConfig{KeyID: "k1", Key: bad}); err == nil {
			t.Errorf("%s: NewSigner accepted it", name)
		}
	}
	if !(SealedKey{}).IsZero() || k.IsZero() {
		t.Error("IsZero does not tell a SealedKey with no key from one with a key")
	}
}
