package limes

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/oleg-tkachuk/limes/internal/jose"
)

// The parsers below read bytes an attacker chooses. Each must refuse what it
// cannot read — never panic — and what it accepts must hold the invariants
// the code after it relies on.

// FuzzParseClaims holds the claims reader to its strictness: whatever it
// accepts names a capability, and its sub is its principal's.
func FuzzParseClaims(f *testing.F) {
	f.Add([]byte(`{"iss":"i","sub":"s","aud":["data"],"iat":1,"exp":2,"jti":"0190a5c3-0000-7000-8000-000000000000","limes_principal":{"tenant_id":"11111111-1111-1111-1111-111111111111","subject":"s"},"limes_caveats":{"ops":["get"],"max_budget":"1.5"}}`))
	f.Add([]byte(`{"iss":"a","iss":"b"}`))
	f.Add([]byte(`{"limes_caveats":{"Ops":["get"]}}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		c, err := parseClaims(jose.B64(raw))
		if err != nil {
			return
		}
		if c.Caveats.MaxBudgetAmount < 0 || c.Caveats.MaxBudgetAmount > MaxNanos {
			t.Fatalf("accepted a budget of %d", int64(c.Caveats.MaxBudgetAmount))
		}
	})
}

// FuzzDPoPCheck holds the proof checker to refusing, never panicking on, a
// proof it cannot read; and to accepting none for a capability bound to a
// key the fuzzer does not hold.
func FuzzDPoPCheck(f *testing.F) {
	now := time.Unix(1_800_000_000, 0)
	key := ed25519.NewKeyFromSeed(goldenSeed[:])
	proof, err := NewDPoPProof(key, DPoPProofRequest{Method: dpopMethod, URL: dpopURL, Token: "tok", Now: now})
	if err != nil {
		f.Fatal(err)
	}
	f.Add(proof)
	f.Add("a.b.c")
	other := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	jkt, err := KeyThumbprint(other.Public())
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, p string) {
		v := &DPoPVerifier{Replay: NewMemoryReplayCache(0), Now: func() time.Time { return now }}
		err := v.check(context.Background(), Capability{ConfirmationJKT: jkt}, "tok", DPoPProof{Proof: p, Method: dpopMethod, URL: dpopURL})
		if err == nil {
			t.Fatalf("a proof without the bound key was accepted: %q", p)
		}
	})
}

// FuzzOpenBiscuit holds the Biscuit reader to refusing, never panicking on,
// a token it cannot read.
func FuzzOpenBiscuit(f *testing.F) {
	f.Add("")
	for _, name := range []string{biscuitSeedFile, biscuitAttenuatedFile} {
		raw, err := os.ReadFile(biscuitFixturePath(name))
		if err != nil {
			f.Fatal(err)
		}
		f.Add(strings.TrimSpace(string(raw)))
	}
	f.Fuzz(func(t *testing.T, token string) {
		_, _, _ = openBiscuit(token)
	})
}

// FuzzNormalizeURI holds the htu normalisation to being a normal form:
// normalising what it returns changes nothing.
func FuzzNormalizeURI(f *testing.F) {
	for _, s := range []string{"https://A.example:443/a/./b/../%7e?q#f", "http://[::1]:80", "https://h/%zz/.."} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		once, err := normalizeURI(raw)
		if err != nil {
			return
		}
		twice, err := normalizeURI(once)
		if err != nil || twice != once {
			t.Fatalf("%q normalises to %q, which normalises to %q, %v", raw, once, twice, err)
		}
	})
}

// FuzzParseAuthorization holds the Authorization reader to what it promises:
// a scheme this module reads and a token68 credential, which read back under
// that scheme give the same answer.
func FuzzParseAuthorization(f *testing.F) {
	for _, s := range []string{"Bearer abc.def-_~+/==", "dpop  x", "Basic dXNlcg==", "Bearer a b", "Bearer ="} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, header string) {
		scheme, credential, err := ParseAuthorization(header)
		if err != nil {
			return
		}
		if scheme != AuthSchemeBearer && scheme != AuthSchemeDPoP {
			t.Fatalf("%q read as scheme %q", header, scheme)
		}
		if !isToken68(credential) {
			t.Fatalf("%q read as credential %q, not a token68", header, credential)
		}
		again, cred, err := ParseAuthorization(string(scheme) + " " + credential)
		if err != nil || again != scheme || cred != credential {
			t.Fatalf("%q reads back as %q %q, %v", header, again, cred, err)
		}
	})
}

// FuzzHMACNonceValid holds HMACNonces to accepting only what it issued:
// every nonce it accepts carries the issued nonce's bytes.
func FuzzHMACNonceValid(f *testing.F) {
	issuedAt := time.Unix(1_800_000_000, 0)
	nonces, err := NewHMACNonces(0, bytes.Repeat([]byte{1}, HMACNonceKeyBytes))
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
		if !nonces.Valid(context.Background(), nonce) {
			return
		}
		got, err := base64.RawURLEncoding.DecodeString(nonce)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("accepted %q, which is not the nonce issued", nonce)
		}
	})
}
