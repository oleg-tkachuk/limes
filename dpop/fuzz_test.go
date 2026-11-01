package dpop

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/oleg-tkachuk/limes/internal/jose"
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
	proof, err := Sign(context.Background(), key, SignRequest{Method: dpopMethod, URL: dpopURL, Token: "tok", IssuedAt: now})
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
		err := v.Verify(context.Background(), Request{Proof: Proof{JWT: p, Method: dpopMethod, URL: dpopURL}, Token: "tok", JKT: jkt, Holder: testHolder})
		if err == nil {
			t.Fatalf("a proof without the bound key was accepted: %q", p)
		}
	})
}

// FuzzHMACNonceValid holds HMACNonces to accepting only what it issued:
// every nonce it accepts carries the issued nonce's bytes.
func FuzzHMACNonceValid(f *testing.F) {
	issuedAt := time.Unix(1_800_000_000, 0)
	nonces, err := NewHMACNonces(HMACNoncesConfig{Keys: SealNonceKeys(bytes.Repeat([]byte{1}, HMACNonceKeyBytes))})
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

// FuzzDPoPClaims signs whatever header and claims the fuzzer writes with the
// key the capability is bound to, so the reader past the thumbprint — claims,
// method, URL, iat, ath — is what is fuzzed. Whatever it accepts must name
// the request's method, the token's hash and a time inside the window, and
// is accepted once: presented again, it is refused as a replay.
func FuzzDPoPClaims(f *testing.F) {
	now := time.Unix(1_800_000_000, 0)
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{2}, ed25519.SeedSize))
	jkt, err := Thumbprint(key.Public())
	if err != nil {
		f.Fatal(err)
	}
	proof, err := Sign(context.Background(), key, SignRequest{Method: dpopMethod, URL: dpopURL, Token: "tok", IssuedAt: now})
	if err != nil {
		f.Fatal(err)
	}
	parts := strings.Split(proof, ".")
	header, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		f.Fatal(err)
	}
	claims, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		f.Fatal(err)
	}
	f.Add(string(header), string(claims))
	tokenRequest, err := Sign(context.Background(), key, SignRequest{Method: dpopMethod, URL: dpopURL, IssuedAt: now})
	if err != nil {
		f.Fatal(err)
	}
	if claims, err = base64.RawURLEncoding.DecodeString(strings.Split(tokenRequest, ".")[1]); err != nil {
		f.Fatal(err)
	}
	f.Add(string(header), string(claims))
	f.Fuzz(func(t *testing.T, header, claims string) {
		input := jose.B64([]byte(header)) + "." + jose.B64([]byte(claims))
		p := input + "." + jose.B64(ed25519.Sign(key, []byte(input)))
		checkTokenRequest(t, p, claims, jkt, now)
		v := newVerifier(t, VerifierConfig{Replay: newReplay(t, MemoryReplayCacheConfig{}), Now: func() time.Time { return now }})
		req := Request{Proof: Proof{JWT: p, Method: dpopMethod, URL: dpopURL}, Token: "tok", JKT: jkt, Holder: testHolder}
		if err := v.Verify(context.Background(), req); err != nil {
			return
		}
		// A proof is good once: presented again, it is a replay.
		if err := v.Verify(context.Background(), req); !errors.Is(err, ErrReplayed) {
			t.Fatalf("an accepted proof presented again: %v, want ErrReplayed", err)
		}
		var c proofClaims
		if err := decodeSegment(jose.B64([]byte(claims)), &c); err != nil {
			t.Fatalf("accepted claims that do not decode: %q", claims)
		}
		iat := time.Unix(int64(c.IAT), 0)
		if c.HTM != dpopMethod || c.ATH != accessTokenHash("tok") || c.JTI == "" ||
			now.Sub(iat) > DefaultWindow+time.Second || iat.Sub(now) > DefaultWindow+time.Second {
			t.Fatalf("accepted %+v for %s %s", c, dpopMethod, dpopURL)
		}
	})
}

// checkTokenRequest is FuzzDPoPClaims's check of p as a token request's
// proof: what CheckTokenRequest accepts names no token, the request's
// method and a time inside the window, and binds to the key that signed it.
func checkTokenRequest(t *testing.T, p, claims, jkt string, now time.Time) {
	t.Helper()
	v := newVerifier(t, VerifierConfig{Replay: newReplay(t, MemoryReplayCacheConfig{}), Now: func() time.Time { return now }})
	checked, err := v.CheckTokenRequest(context.Background(), TokenRequest{Proof: Proof{JWT: p, Method: dpopMethod, URL: dpopURL}, Holder: testHolder})
	if err != nil {
		return
	}
	var c proofClaims
	if err := decodeSegment(jose.B64([]byte(claims)), &c); err != nil {
		t.Fatalf("accepted token-request claims that do not decode: %q", claims)
	}
	iat := time.Unix(int64(c.IAT), 0)
	if c.HTM != dpopMethod || c.ATH != "" || c.JTI == "" || checked.JKT() != jkt ||
		now.Sub(iat) > DefaultWindow+time.Second || iat.Sub(now) > DefaultWindow+time.Second {
		t.Fatalf("accepted %+v as a token request's proof, bound to %s", c, checked.JKT())
	}
}
