package jwks

import (
	"crypto/ed25519"
	"net/http"
	"testing"
	"time"

	"github.com/oleg-tkachuk/limes/internal/httpx"
)

// The readers below read bytes an issuer's endpoint chooses. Each must refuse
// what it cannot read — never panic — and what it accepts must hold the
// invariants the code after it relies on.

// fuzzKID names the seed key.
const fuzzKID = "fuzz-key"

// FuzzParseJWKS holds jwks.Parse to keys a verifier can use: every key it
// returns is an Ed25519 public key under a kid.
func FuzzParseJWKS(f *testing.F) {
	pub := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize)).Public().(ed25519.PublicKey)
	doc, err := Marshal(map[string]ed25519.PublicKey{fuzzKID: pub})
	if err != nil {
		f.Fatal(err)
	}
	f.Add(doc)
	f.Add([]byte(`{"keys":[{"kty":"OKP","crv":"Ed25519","kid":"k","x":"AA"}]}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		keys, err := Parse(raw)
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

// FuzzResponseFreshness holds the Cache-Control reader to a freshness the
// resolver can clamp: never negative, never past what delta-seconds can say,
// and zero when the response says nothing.
func FuzzResponseFreshness(f *testing.F) {
	for _, s := range [][2]string{{"public, max-age=60", "10"}, {`max-age="5", max-age=9`, ""}, {"no-cache", "x"}, {"max-age=99999999999", "-1"}} {
		f.Add(s[0], s[1])
	}
	f.Fuzz(func(t *testing.T, cacheControl, age string) {
		h := http.Header{}
		h.Set(httpx.HeaderCacheControl, cacheControl)
		h.Set(httpx.HeaderAge, age)
		freshness, ok := responseFreshness(h)
		if freshness < 0 || freshness > time.Duration(maxDeltaSeconds)*time.Second || !ok && freshness != 0 {
			t.Fatalf("Cache-Control %q, Age %q read as %v, %v", cacheControl, age, freshness, ok)
		}
	})
}
