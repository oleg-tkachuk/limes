package dpop

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"testing"
	"time"
)

// The readers below read bytes a client chooses. Each must refuse what it
// cannot read — never panic — and what it accepts must hold the invariants
// the code after it relies on.

// FuzzDPoPCheck holds the proof checker to refusing, never panicking on, a
// proof it cannot read; and to accepting none for a capability bound to a
// key the fuzzer does not hold.
func FuzzDPoPCheck(f *testing.F) {
	now := time.Unix(1_800_000_000, 0)
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{1}, ed25519.SeedSize))
	proof, err := Sign(key, ProofRequest{Method: dpopMethod, URL: dpopURL, Token: "tok", Now: now})
	if err != nil {
		f.Fatal(err)
	}
	f.Add(proof)
	f.Add("a.b.c")
	other := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	jkt, err := Thumbprint(other.Public())
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, p string) {
		v := newVerifier(t, VerifierConfig{Replay: newReplay(t, MemoryReplayCacheConfig{}), Now: func() time.Time { return now }})
		err := v.Verify(context.Background(), Request{Proof: Proof{JWT: p, Method: dpopMethod, URL: dpopURL}, Token: "tok", JKT: jkt})
		if err == nil {
			t.Fatalf("a proof without the bound key was accepted: %q", p)
		}
	})
}

// FuzzHMACNonceValid holds HMACNonces to accepting only what it issued:
// every nonce it accepts carries the issued nonce's bytes.
func FuzzHMACNonceValid(f *testing.F) {
	issuedAt := time.Unix(1_800_000_000, 0)
	nonces, err := NewHMACNonces(HMACNoncesConfig{Keys: [][]byte{bytes.Repeat([]byte{1}, HMACNonceKeyBytes)}})
	if err != nil {
		f.Fatal(err)
	}
	nonces.now = func() time.Time { return issuedAt }
	issued, err := nonces.Nonce(context.Background())
	if err != nil {
		f.Fatal(err)
	}
	want, err := base64.RawURLEncoding.DecodeString(issued)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(issued)
	f.Add(issued[:len(issued)-1] + "A")
	f.Add("")
	f.Fuzz(func(t *testing.T, nonce string) {
		if !valid(t, nonces, nonce) {
			return
		}
		got, err := base64.RawURLEncoding.DecodeString(nonce)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("accepted %q, which is not the nonce issued", nonce)
		}
	})
}
