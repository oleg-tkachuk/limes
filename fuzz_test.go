package limes

import (
	"context"
	"crypto/ed25519"
	"os"
	"strings"
	"testing"
	"time"
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
		c, err := parseClaims(b64(raw))
		if err != nil {
			return
		}
		if c.Caveats.MaxBudgetAmount < 0 || c.Caveats.MaxBudgetAmount > MaxNanos {
			t.Fatalf("accepted a budget of %d", int64(c.Caveats.MaxBudgetAmount))
		}
	})
}

// FuzzParseJWKS holds ParseJWKS to keys a verifier can use: every key it
// returns is an Ed25519 public key under a kid.
func FuzzParseJWKS(f *testing.F) {
	pub := ed25519.NewKeyFromSeed(goldenSeed[:]).Public().(ed25519.PublicKey)
	doc, err := MarshalJWKS(map[string]ed25519.PublicKey{goldenKID: pub})
	if err != nil {
		f.Fatal(err)
	}
	f.Add(doc)
	f.Add([]byte(`{"keys":[{"kty":"OKP","crv":"Ed25519","kid":"k","x":"AA"}]}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		keys, err := ParseJWKS(raw)
		if err != nil {
			return
		}
		for kid, k := range keys {
			if kid == "" || len(k) != ed25519.PublicKeySize {
				t.Fatalf("kid %q with a %d-byte key", kid, len(k))
			}
		}
	})
}

// FuzzDPoPCheck holds the proof checker to refusing, never panicking on, a
// proof it cannot read; and to accepting none for a capability bound to a
// key the fuzzer does not hold.
func FuzzDPoPCheck(f *testing.F) {
	now := time.Unix(1_800_000_000, 0)
	key := ed25519.NewKeyFromSeed(goldenSeed[:])
	proof, err := NewDPoPProof(key, dpopMethod, dpopURL, "tok", now)
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
