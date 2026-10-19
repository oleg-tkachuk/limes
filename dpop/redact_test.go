package dpop

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

// verbsThatShowBytes are the verbs under which fmt would print a key's
// bytes, in one notation or another, from a value that holds it.
var verbsThatShowBytes = []string{"%v", "%+v", "%#v", "%s", "%x", "%X", "%q", "%d", "%t"}

// A value holding HMAC nonce keys formats, encodes and logs without them,
// itself or through a pointer, and still says how many there are.
func TestHMACNonceKeysAreRedacted(t *testing.T) {
	key := make([]byte, HMACNonceKeyBytes)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	cfg := HMACNoncesConfig{Keys: [][]byte{key}}
	nonces, err := NewHMACNonces(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// keyCount is how many keys the values hold, which they still show.
	const keyCount = 1
	shown := fmt.Sprintf(redactedKeysFormat, keyCount)
	values := map[string]any{"HMACNoncesConfig": cfg, "*HMACNoncesConfig": &cfg, "*HMACNonces": nonces, "HMACNonces": *nonces}
	for name, v := range values {
		for _, verb := range verbsThatShowBytes {
			got := fmt.Sprintf(verb, v)
			if strings.Contains(got, fmt.Sprintf(verb, key)) {
				t.Errorf("%s under %s = %q: carries the key", name, verb, got)
			}
		}
		if got := fmt.Sprintf("%v", v); !strings.Contains(got, shown) {
			t.Errorf("%s under %%v = %q: want %s", name, got, shown)
		}
		encoded, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("%s: json.Marshal: %v", name, err)
		}
		var text, structured bytes.Buffer
		slog.New(slog.NewTextHandler(&text, nil)).Info("nonces", "value", v)
		slog.New(slog.NewJSONHandler(&structured, nil)).Info("nonces", "value", v)
		for how, got := range map[string]string{"json.Marshal": string(encoded), "slog text": text.String(), "slog JSON": structured.String()} {
			for _, spelling := range []string{base64.StdEncoding.EncodeToString(key), fmt.Sprintf("%x", key)} {
				if strings.Contains(got, spelling) {
					t.Errorf("%s under %s = %s: carries the key", name, how, got)
				}
			}
			if !strings.Contains(got, shown) {
				t.Errorf("%s under %s = %s: want %s", name, how, got, shown)
			}
		}
	}
}

// A value that holds HMACNonces where fmt cannot call their Format — a
// Verifier, or any struct keeping one in an unexported field — formats
// without the keys under every verb, and a zero HMACNonces formats at all.
func TestCarriersOfHMACNoncesLeaveTheKeysOut(t *testing.T) {
	key := make([]byte, HMACNonceKeyBytes)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	nonces, err := NewHMACNonces(HMACNoncesConfig{Keys: [][]byte{key}})
	if err != nil {
		t.Fatal(err)
	}
	replay, err := NewMemoryReplayCache(MemoryReplayCacheConfig{})
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := NewVerifier(VerifierConfig{Replay: replay, Nonces: nonces})
	if err != nil {
		t.Fatal(err)
	}
	for name, v := range map[string]any{
		"*Verifier":                    verifier,
		"Verifier":                     *verifier,
		"a struct with a Verifier":     struct{ verifier *Verifier }{verifier},
		"a struct with HMACNonces":     struct{ nonces *HMACNonces }{nonces},
		"a struct with VerifierConfig": struct{ cfg VerifierConfig }{VerifierConfig{Replay: replay, Nonces: nonces}},
	} {
		for _, verb := range verbsThatShowBytes {
			got := fmt.Sprintf(verb, v)
			// fmt prints a field it reaches this way under %v, whatever
			// the verb, so the key is looked for in every spelling.
			for _, spelling := range verbsThatShowBytes {
				if strings.Contains(got, fmt.Sprintf(spelling, key)) {
					t.Errorf("%s under %s = %q: carries the key", name, verb, got)
				}
			}
		}
	}
	if got := fmt.Sprintf("%v", HMACNonces{}); !strings.Contains(got, fmt.Sprintf(redactedKeysFormat, 0)) {
		t.Errorf("a zero HMACNonces under %%v = %q, want no keys", got)
	}
}
