package dpop

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/oleg-tkachuk/limes/internal/jose"
)

const (
	dpopURL    = "https://data.example.com/example.data.v1.ObjectService/GetObject"
	dpopMethod = "POST"
)

func dpopKeys(t testing.TB) map[string]crypto.Signer {
	t.Helper()
	_, ed, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ec, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]crypto.Signer{"Ed25519": ed, "P-256": ec}
}

// boundJKT is the thumbprint a token bound to key carries.
func boundJKT(t testing.TB, key crypto.Signer) string {
	t.Helper()
	jkt, err := Thumbprint(key.Public())
	if err != nil {
		t.Fatal(err)
	}
	return jkt
}

func TestDPoPAcceptsAProofFromTheBoundKey(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	for name, key := range dpopKeys(t) {
		t.Run(name, func(t *testing.T) {
			c := boundJKT(t, key)
			v := newVerifier(t, VerifierConfig{Replay: newReplay(t, MemoryReplayCacheConfig{}), Now: func() time.Time { return now }})
			proof, err := Sign(context.Background(), key, SignRequest{Method: "POST", URL: dpopURL + "?x=1", Token: "the-token", IssuedAt: now})
			if err != nil {
				t.Fatal(err)
			}
			req := Proof{JWT: proof, Method: "POST", URL: dpopURL}
			if err := v.Verify(context.Background(), Request{Proof: req, Token: "the-token", JKT: c, Holder: testHolder}); err != nil {
				t.Fatalf("valid proof refused: %v", err)
			}
			if err := v.Verify(context.Background(), Request{Proof: req, Token: "the-token", JKT: c, Holder: testHolder}); !errors.Is(err, ErrReplayed) {
				t.Fatalf("replayed proof: err = %v, want ErrReplayed", err)
			}
		})
	}
}

func TestDPoPRefusals(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	keys := dpopKeys(t)
	key, other := keys["Ed25519"], keys["P-256"]
	c := boundJKT(t, key)
	fresh := func(k crypto.Signer, method, url, token string, at time.Time) string {
		p, err := Sign(context.Background(), k, SignRequest{Method: method, URL: url, Token: token, IssuedAt: at})
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	base := Proof{Method: "POST", URL: dpopURL}
	cases := map[string]struct {
		proof string
		want  error
	}{
		"no proof":           {"", ErrRequired},
		"another key":        {fresh(other, "POST", dpopURL, "the-token", now), ErrInvalid},
		"another method":     {fresh(key, "GET", dpopURL, "the-token", now), ErrInvalid},
		"another path":       {fresh(key, "POST", "https://data.example.com/other", "the-token", now), ErrInvalid},
		"another host":       {fresh(key, "POST", "https://evil.example.com/example.data.v1.ObjectService/GetObject", "the-token", now), ErrInvalid},
		"another token":      {fresh(key, "POST", dpopURL, "a-stolen-token", now), ErrInvalid},
		"stale":              {fresh(key, "POST", dpopURL, "the-token", now.Add(-2*time.Minute)), ErrInvalid},
		"from the future":    {fresh(key, "POST", dpopURL, "the-token", now.Add(2*time.Minute)), ErrInvalid},
		"tampered signature": {fresh(key, "POST", dpopURL, "the-token", now) + "AA", ErrInvalid},
		"garbage":            {"not.a.proof", ErrInvalid},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			v := newVerifier(t, VerifierConfig{Replay: newReplay(t, MemoryReplayCacheConfig{}), Now: func() time.Time { return now }})
			req := base
			req.JWT = tc.proof
			err := v.Verify(context.Background(), Request{Proof: req, Token: "the-token", JKT: c, Holder: testHolder})
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestDPoPPathOnlyIgnoresHostButNotPath(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	key := dpopKeys(t)["Ed25519"]
	c := boundJKT(t, key)
	v := newVerifier(t, VerifierConfig{Replay: newReplay(t, MemoryReplayCacheConfig{}), Now: func() time.Time { return now }, MatchPathOnly: true})
	proof, _ := Sign(context.Background(), key, SignRequest{Method: "POST", URL: dpopURL, Token: "tok", IssuedAt: now})
	req := Proof{JWT: proof, Method: "POST", URL: "http://10.0.0.7:8080/example.data.v1.ObjectService/GetObject"}
	if err := v.Verify(context.Background(), Request{Proof: req, Token: "tok", JKT: c, Holder: testHolder}); err != nil {
		t.Fatalf("path-only match behind a proxy: %v", err)
	}
	proof, _ = Sign(context.Background(), key, SignRequest{Method: "POST", URL: "https://gw.example.com/api/example.data.v1.ObjectService/GetObject", Token: "tok", IssuedAt: now})
	req.JWT = proof
	if err := v.Verify(context.Background(), Request{Proof: req, Token: "tok", JKT: c, Holder: testHolder}); err != nil {
		t.Fatalf("path-only match behind a prefix-stripping proxy: %v", err)
	}
	proof, _ = Sign(context.Background(), key, SignRequest{Method: "POST", URL: dpopURL, Token: "tok", IssuedAt: now})
	req.JWT, req.URL = proof, "http://10.0.0.7:8080/example.data.v1.ObjectService/DeleteObject"
	if err := v.Verify(context.Background(), Request{Proof: req, Token: "tok", JKT: c, Holder: testHolder}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("path-only with another path: err = %v, want ErrInvalid", err)
	}
}

// RFC 7638 §3.1's worked example.
func TestThumbprintMatchesRFC7638(t *testing.T) {
	// The RFC's example is RSA; for OKP, RFC 8037 §A.3 gives one.
	j := jose.JWK{Kty: "OKP", Crv: "Ed25519", X: "11qYAYKxCrfVS_7TyWQHOg7hcvPapiMlrwIaaPcHURo"}
	got, err := j.Thumbprint()
	if err != nil {
		t.Fatal(err)
	}
	if want := "kPrK_qmxVWaYVA9wwBF6Iuo3vVzz7TxHCTwXBygrS4k"; got != want {
		t.Fatalf("thumbprint = %s, want %s", got, want)
	}
}

func TestMemoryReplayCacheIsBoundedAndForgetsExpired(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	c := newReplay(t, MemoryReplayCacheConfig{MaxEntries: 2})
	c.now = func() time.Time { return now }
	if seen(t, c, "k", "a", now.Add(time.Minute)) || seen(t, c, "k", "b", now.Add(time.Second)) {
		t.Fatal("fresh ids reported seen")
	}
	if !seen(t, c, "k", "c", now.Add(time.Minute)) {
		t.Fatal("a full cache accepted an id it could not record")
	}
	now = now.Add(2 * time.Second) // b expires
	if seen(t, c, "k", "c", now.Add(time.Minute)) {
		t.Fatal("an id refused while full was not accepted once room freed up")
	}
	if !seen(t, c, "k", "a", now.Add(time.Minute)) {
		t.Fatal("a live id was forgotten")
	}
}

func TestDPoPProofFromAnOpaqueSigner(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	for name, key := range dpopKeys(t) {
		t.Run(name, func(t *testing.T) {
			c := boundJKT(t, key)
			proof, err := Sign(context.Background(), opaqueSigner{key}, SignRequest{Method: "POST", URL: dpopURL, Token: "tok", IssuedAt: now})
			if err != nil {
				t.Fatal(err)
			}
			v := newVerifier(t, VerifierConfig{Replay: newReplay(t, MemoryReplayCacheConfig{}), Now: func() time.Time { return now }})
			if err := v.Verify(context.Background(), Request{Proof: Proof{JWT: proof, Method: "POST", URL: dpopURL}, Token: "tok", JKT: c, Holder: testHolder}); err != nil {
				t.Fatalf("proof from an opaque signer refused: %v", err)
			}
		})
	}
}

// Expired ids leave in expiry order as time passes, whatever order they
// came in, and only they do.
func TestMemoryReplayCacheForgetsInExpiryOrder(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	c := newReplay(t, MemoryReplayCacheConfig{})
	c.now = func() time.Time { return now }
	for i, d := range []time.Duration{5, 1, 4, 2, 3} {
		if seen(t, c, "k", fmt.Sprint(i), now.Add(d*time.Second)) {
			t.Fatalf("fresh id %d reported seen", i)
		}
	}
	now = now.Add(3 * time.Second) // the ids expiring at 1s, 2s and 3s lapse
	_ = seen(t, c, "k", "probe", now.Add(time.Hour))
	if got := len(c.seen); got != 3 {
		t.Errorf("after the clock passed three expiries, %d ids are held, want 3", got)
	}
	for _, live := range []string{"0", "2"} {
		if !seen(t, c, "k", live, now.Add(time.Hour)) {
			t.Errorf("live id %s was forgotten", live)
		}
	}
}

// BenchmarkMemoryReplayCacheFull measures a new id offered to a full cache
// in which nothing has expired — the case a flood of proofs produces.
func BenchmarkMemoryReplayCacheFull(b *testing.B) {
	const entries = 100_000
	now := time.Unix(1_800_000_000, 0)
	c := newReplay(b, MemoryReplayCacheConfig{MaxEntries: entries, Now: func() time.Time { return now }})
	for i := range entries {
		seen(b, c, fmt.Sprint(i), fmt.Sprint(i), now.Add(time.Hour))
	}
	b.ResetTimer()
	for i := 0; b.Loop(); i++ {
		seen(b, c, fmt.Sprint("new", i), fmt.Sprint("new", i), now.Add(time.Hour))
	}
}

// RFC 9449 §4.3: a proof whose jwk carries a private key is refused, though
// the signature checks out — the holder has published the key the binding
// rests on.
func TestDPoPRefusesAPrivateJWK(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	c := boundJKT(t, priv)
	jwk, err := publicJWK(priv.Public())
	if err != nil {
		t.Fatal(err)
	}
	header := fmt.Sprintf(`{"typ":%q,"alg":%q,"jwk":{"kty":%q,"crv":%q,"x":%q,"d":%q}}`,
		tokenType, jose.AlgEdDSA, jwk.Kty, jwk.Crv, jwk.X, jose.B64(priv.Seed()))
	claims := fmt.Sprintf(`{"jti":"j1","htm":"POST","htu":%q,"iat":%d,"ath":%q}`,
		dpopURL, now.Unix(), accessTokenHash("the-token"))
	input := jose.B64([]byte(header)) + "." + jose.B64([]byte(claims))
	proof := input + "." + jose.B64(ed25519.Sign(priv, []byte(input)))

	v := newVerifier(t, VerifierConfig{Replay: newReplay(t, MemoryReplayCacheConfig{}), Now: func() time.Time { return now }})
	err = v.Verify(context.Background(), Request{Proof: Proof{JWT: proof, Method: "POST", URL: dpopURL}, Token: "the-token", JKT: c, Holder: testHolder})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("a proof carrying its private key: err = %v, want ErrInvalid", err)
	}
}

// One holder cannot fill the cache, however many keys it signs with: past
// its share only its own proofs are refused, and every other holder's fresh
// proof is still recorded.
func TestMemoryReplayCacheBoundsEachHolder(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	const share = 3
	c := newReplay(t, MemoryReplayCacheConfig{MaxEntries: 100, EntriesPerHolder: share})
	c.now = func() time.Time { return now }
	for i := range share {
		if seenAs(t, c, "noisy", fmt.Sprint("key-", i), fmt.Sprint(i), now.Add(time.Minute)) {
			t.Fatalf("proof %d of the noisy holder refused within its share", i)
		}
	}
	if !seenAs(t, c, "noisy", "yet another key", "one more", now.Add(time.Minute)) {
		t.Error("the noisy holder recorded past its share under a new key")
	}
	if seenAs(t, c, "quiet", "key-0", "first", now.Add(time.Minute)) {
		t.Error("another holder's fresh proof refused because one holder filled its share")
	}
	// The same id under two keys is two proofs.
	if seenAs(t, c, "other", "another key", "0", now.Add(time.Minute)) {
		t.Error("an id another key used was refused")
	}
	now = now.Add(2 * time.Minute) // everything lapses, and the share with it
	if seenAs(t, c, "noisy", "key-0", "later", now.Add(time.Minute)) {
		t.Error("the noisy holder's share did not free as its ids lapsed")
	}
	if got := newReplay(t, MemoryReplayCacheConfig{MaxEntries: 2}).maxPerHolder; got != 2 {
		t.Errorf("the default share in a cache smaller than it = %d, want the cache's size", got)
	}
}

// RFC 9110 §9.1: a method is case-sensitive, so a proof for POST is not one
// for "post". RFC 7519 §2: a NumericDate may carry a fraction.
func TestDPoPReadsMethodAndTimeAsTheRFCsDo(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	c := boundJKT(t, priv)
	jwk, _ := publicJWK(priv.Public())
	sign := func(claims string) string {
		header := fmt.Sprintf(`{"typ":%q,"alg":%q,"jwk":{"kty":%q,"crv":%q,"x":%q}}`, tokenType, jose.AlgEd25519, jwk.Kty, jwk.Crv, jwk.X)
		input := jose.B64([]byte(header)) + "." + jose.B64([]byte(claims))
		return input + "." + jose.B64(ed25519.Sign(priv, []byte(input)))
	}
	claims := func(jti, iat string) string {
		return fmt.Sprintf(`{"jti":%q,"htm":"POST","htu":%q,"iat":%s,"ath":%q}`, jti, dpopURL, iat, accessTokenHash("tok"))
	}
	v := newVerifier(t, VerifierConfig{Replay: newReplay(t, MemoryReplayCacheConfig{}), Now: func() time.Time { return now }})
	ctx := context.Background()
	if err := v.Verify(ctx, Request{Proof: Proof{JWT: sign(claims("j1", "1800000000")), Method: "post", URL: dpopURL}, Token: "tok", JKT: c, Holder: testHolder}); !errors.Is(err, ErrInvalid) {
		t.Errorf("a POST proof for a post request: err = %v, want ErrInvalid", err)
	}
	if err := v.Verify(ctx, Request{Proof: Proof{JWT: sign(claims("j2", "1800000000.25")), Method: "POST", URL: dpopURL}, Token: "tok", JKT: c, Holder: testHolder}); err != nil {
		t.Errorf("a proof with a fractional iat: %v", err)
	}
}

// A proof is accepted for the request URL however either is spelled, as long
// as both normalise to the same URI.
func TestDPoPMatchesNormalizedURIs(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	key := dpopKeys(t)["Ed25519"]
	c := boundJKT(t, key)
	for _, tc := range []struct {
		proofURL, requestURL string
		ok                   bool
	}{
		{"https://data.example.com:443/v1/%7Eobj", "https://DATA.example.com/v1/~obj", true},
		{"https://data.example.com/v1/a/../obj", "https://data.example.com/v1/obj", true},
		{"https://data.example.com/v1/obj", "https://data.example.com/v1/obj/", false},
		{"https://data.example.com/v1/obj", "http://data.example.com/v1/obj", false},
		{"https://data.example.com/v1/obj", "https://data.example.com:8443/v1/obj", false},
	} {
		v := newVerifier(t, VerifierConfig{Replay: newReplay(t, MemoryReplayCacheConfig{}), Now: func() time.Time { return now }})
		proof, err := Sign(context.Background(), key, SignRequest{Method: dpopMethod, URL: tc.proofURL, Token: "tok", IssuedAt: now})
		if err != nil {
			t.Fatal(err)
		}
		err = v.Verify(context.Background(), Request{Proof: Proof{JWT: proof, Method: dpopMethod, URL: tc.requestURL}, Token: "tok", JKT: c, Holder: testHolder})
		if tc.ok && err != nil {
			t.Errorf("proof for %s, request %s: %v", tc.proofURL, tc.requestURL, err)
		}
		if !tc.ok && !errors.Is(err, ErrInvalid) {
			t.Errorf("proof for %s, request %s: err = %v, want ErrInvalid", tc.proofURL, tc.requestURL, err)
		}
	}
}

// Under MatchPathOnly the proof's htu is still a URI the request could have
// been made to — absolute, with a host — and a request with no path matches
// nothing.
func TestHTUPathOnlyStillReadsTheProof(t *testing.T) {
	cases := []struct {
		htu, request string
		ok           bool
	}{
		{"https://gw.example.com/api/v1/objects", "http://10.0.0.7:8080/v1/objects", true},
		{"https://gw.example.com/v1/objects", "/v1/objects", true},
		{"x/v1/objects", "/v1/objects", false},          // relative htu
		{"urn:anything:/", "/", false},                  // no host
		{"https://u:p@gw.example.com/v1", "/v1", false}, // userinfo
		{"https://gw.example.com/anything/", "", false}, // no request path
		{"https://gw.example.com/a/../v1/x", "/v1/x", true},
		// The root matches only the root: as a tail it would match every
		// path ending in a slash.
		{"https://gw.example.com/", "/", true},
		{"https://gw.example.com", "/", true},
		{"https://gw.example.com/admin/", "/", false},
		{"https://gw.example.com/api/", "/", false},
		{"https://gw.example.com/api/items/", "/items/", true},
	}
	for _, tc := range cases {
		if got := htuMatches(tc.htu, tc.request, true); got != tc.ok {
			t.Errorf("htuMatches(%q, %q, pathOnly) = %v, want %v", tc.htu, tc.request, got, tc.ok)
		}
	}
}

// seen is c.Seen with a failure of the cache failing the test.
// testHolder is the holder the proofs of these tests count against.
const testHolder = "capability"

func seen(t testing.TB, c ReplayCache, jkt, jti string, expires time.Time) bool {
	t.Helper()
	return seenAs(t, c, testHolder, jkt, jti, expires)
}

// seenAs is seen, counted against holder.
func seenAs(t testing.TB, c ReplayCache, holder, jkt, jti string, expires time.Time) bool {
	t.Helper()
	ok, err := c.Seen(context.Background(), ReplayEntry{JKT: jkt, JTI: jti, Holder: holder, ExpiresAt: expires})
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

// valid is n.Valid with a failure of the nonces failing the test.
func valid(t testing.TB, n Nonces, nonce string) bool {
	t.Helper()
	ok, err := n.Valid(context.Background(), nonce)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

// opaqueSigner hides the concrete key type, as a KMS or HSM signer does.
type opaqueSigner struct{ crypto.Signer }

// RFC 9864: a proof names Ed25519, and one from a tool that predates it,
// naming EdDSA, is still read.
func TestSignWritesTheFullySpecifiedAlgorithm(t *testing.T) {
	key := dpopKeys(t)["Ed25519"]
	proof, err := Sign(context.Background(), key, SignRequest{Method: dpopMethod, URL: dpopURL, Token: "tok", IssuedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	pp, _ := jose.Split(proof)
	var ph proofHeader
	if err := decodeSegment(pp.Header, &ph); err != nil || ph.Alg != jose.AlgEd25519 {
		t.Errorf("proof alg = %q, %v; want %q", ph.Alg, err, jose.AlgEd25519)
	}
	_, priv, _ := ed25519.GenerateKey(nil)
	if !verifySignature(priv.Public(), jose.AlgEdDSA, []byte("in"), ed25519.Sign(priv, []byte("in"))) {
		t.Error("a proof signed under EdDSA was refused")
	}
}

// A zero bound takes the documented default.
func TestMemoryReplayCacheDefaultBound(t *testing.T) {
	if c := newReplay(t, MemoryReplayCacheConfig{}); c.maxEntries != DefaultReplayCacheEntries {
		t.Errorf("replay cache bound = %d, want %d", c.maxEntries, DefaultReplayCacheEntries)
	}
}

// newReplay is NewMemoryReplayCache, failing the test on a refused
// configuration.
func newReplay(t testing.TB, cfg MemoryReplayCacheConfig) *MemoryReplayCache {
	t.Helper()
	c, err := NewMemoryReplayCache(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// newVerifier is NewVerifier, failing the test on a refused configuration.
func newVerifier(t testing.TB, cfg VerifierConfig) *Verifier {
	t.Helper()
	v, err := NewVerifier(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// Each constructor refuses what it cannot run: no replay cache, a negative
// window, bound or lifetime, no nonce key.
func TestConstructorsRefuseABadConfiguration(t *testing.T) {
	replay := newReplay(t, MemoryReplayCacheConfig{})
	for name, cfg := range map[string]VerifierConfig{
		"no replay cache": {},
		"negative window": {Replay: replay, Window: -time.Second},
	} {
		if _, err := NewVerifier(cfg); err == nil {
			t.Errorf("NewVerifier, %s: accepted", name)
		}
	}
	for name, cfg := range map[string]MemoryReplayCacheConfig{
		"negative bound":         {MaxEntries: -1},
		"negative per holder":    {EntriesPerHolder: -1},
		"a share past the cache": {MaxEntries: 2, EntriesPerHolder: 10},
	} {
		if _, err := NewMemoryReplayCache(cfg); err == nil {
			t.Errorf("NewMemoryReplayCache, %s: accepted", name)
		}
	}
	if _, err := NewHMACNonces(HMACNoncesConfig{Keys: [][]byte{nonceKey(1)}, Lifetime: -time.Second}); err == nil {
		t.Error("NewHMACNonces accepted a negative lifetime")
	}
}

// A proof is remembered for longer than it is accepted, so it is never
// accepted twice: not at the very edge of the window, and not when the
// cache's clock reads a little later than the verifier's.
func TestDPoPReplayIsCaughtAtTheWindowEdge(t *testing.T) {
	iat := time.Unix(1_800_000_000, 0)
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	c := boundJKT(t, priv)
	proof, err := Sign(context.Background(), priv, SignRequest{Method: "POST", URL: dpopURL, Token: "tok", IssuedAt: iat})
	if err != nil {
		t.Fatal(err)
	}
	req := Request{Proof: Proof{JWT: proof, Method: "POST", URL: dpopURL}, Token: "tok", JKT: c, Holder: testHolder}
	for name, skew := range map[string]time.Duration{
		"clocks agree":                        0,
		"the cache reads later":               ReplayClockSkew / 2,
		"the cache reads just under the skew": ReplayClockSkew - time.Nanosecond,
	} {
		t.Run(name, func(t *testing.T) {
			at := iat.Add(DefaultWindow)
			replay := newReplay(t, MemoryReplayCacheConfig{Now: func() time.Time { return at.Add(skew) }})
			v := newVerifier(t, VerifierConfig{Replay: replay, Now: func() time.Time { return at }})
			if err := v.Verify(context.Background(), req); err != nil {
				t.Fatalf("the proof at the window's edge: %v", err)
			}
			if err := v.Verify(context.Background(), req); !errors.Is(err, ErrReplayed) {
				t.Fatalf("the same proof again: err = %v, want ErrReplayed", err)
			}
		})
	}
}

// ctxKey marks the context a ctxSigner must be handed.
type ctxKey struct{}

// ctxSigner records the context SignContext was given.
type ctxSigner struct {
	crypto.Signer
	got context.Context
}

func (s *ctxSigner) SignContext(ctx context.Context, r io.Reader, m []byte, o crypto.SignerOpts) ([]byte, error) {
	s.got = ctx
	return s.Sign(r, m, o)
}

// A key that signs under a context is handed Sign's, for each algorithm.
func TestSignHandsTheContextToAContextSigner(t *testing.T) {
	for name, key := range dpopKeys(t) {
		ctx := context.WithValue(context.Background(), ctxKey{}, name)
		s := &ctxSigner{Signer: key}
		if _, err := Sign(ctx, s, SignRequest{Method: dpopMethod, URL: dpopURL, Token: "tok", IssuedAt: time.Now()}); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if s.got == nil || s.got.Value(ctxKey{}) != name {
			t.Errorf("%s: SignContext was not given Sign's context", name)
		}
	}
}

// A proof with no time is refused, not dated 1970.
func TestSignRequiresIssuedAt(t *testing.T) {
	key := dpopKeys(t)["Ed25519"]
	if _, err := Sign(context.Background(), key, SignRequest{Method: dpopMethod, URL: dpopURL, Token: "tok"}); err == nil {
		t.Error("Sign accepted a zero IssuedAt")
	}
}

// A proof past MaxProofBytes is refused unread, a jti past MaxJTIBytes is
// never stored, and a request naming no holder is the caller's error.
func TestDPoPBoundsWhatItReadsAndStores(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	c := boundJKT(t, priv)
	jwk, _ := publicJWK(priv.Public())
	sign := func(jti string) string {
		header := fmt.Sprintf(`{"typ":%q,"alg":%q,"jwk":{"kty":%q,"crv":%q,"x":%q}}`, tokenType, jose.AlgEd25519, jwk.Kty, jwk.Crv, jwk.X)
		claims := fmt.Sprintf(`{"jti":%q,"htm":"POST","htu":%q,"iat":%d,"ath":%q}`, jti, dpopURL, now.Unix(), accessTokenHash("tok"))
		input := jose.B64([]byte(header)) + "." + jose.B64([]byte(claims))
		return input + "." + jose.B64(ed25519.Sign(priv, []byte(input)))
	}
	replay := newReplay(t, MemoryReplayCacheConfig{})
	v := newVerifier(t, VerifierConfig{Replay: replay, Now: func() time.Time { return now }})
	ctx := context.Background()
	verify := func(jwt, holder string) error {
		return v.Verify(ctx, Request{Proof: Proof{JWT: jwt, Method: "POST", URL: dpopURL}, Token: "tok", JKT: c, Holder: holder})
	}
	if err := verify(sign(strings.Repeat("j", MaxJTIBytes)), testHolder); err != nil {
		t.Errorf("a jti of MaxJTIBytes: %v", err)
	}
	if err := verify(sign(strings.Repeat("k", MaxJTIBytes+1)), testHolder); !errors.Is(err, ErrInvalid) {
		t.Errorf("a jti past MaxJTIBytes: err = %v, want ErrInvalid", err)
	}
	if err := verify(sign(strings.Repeat("l", DefaultMaxProofBytes)), testHolder); !errors.Is(err, ErrInvalid) {
		t.Errorf("a proof past DefaultMaxProofBytes: err = %v, want ErrInvalid", err)
	}
	if n := len(replay.seen); n != 1 {
		t.Errorf("the cache holds %d ids, want only the one within bounds", n)
	}
	if err := verify(sign("fresh"), ""); err == nil || errors.Is(err, ErrInvalid) {
		t.Errorf("a request naming no holder: err = %v, want the caller's error", err)
	}
	if _, err := NewVerifier(VerifierConfig{Replay: replay, MaxProofBytes: -1}); err == nil {
		t.Error("NewVerifier accepted a negative MaxProofBytes")
	}
}

// Check records nothing: a proof it accepts is new to the replay cache until
// Record records it, once.
func TestCheckRecordsOnlyOnRecord(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	key := dpopKeys(t)["Ed25519"]
	v := newVerifier(t, VerifierConfig{Replay: newReplay(t, MemoryReplayCacheConfig{}), Now: func() time.Time { return now }})
	proof, err := Sign(ctx, key, SignRequest{Method: dpopMethod, URL: dpopURL, Token: "the-token", IssuedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	req := Request{Proof: Proof{JWT: proof, Method: dpopMethod, URL: dpopURL}, Token: "the-token", JKT: boundJKT(t, key), Holder: testHolder}

	first, err := v.Check(ctx, req)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	second, err := v.Check(ctx, req)
	if err != nil {
		t.Fatalf("Check again before Record: %v, want the proof still new", err)
	}
	if err := v.Record(ctx, first); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if err := v.Record(ctx, second); !errors.Is(err, ErrReplayed) {
		t.Fatalf("Record of the same proof again: err = %v, want ErrReplayed", err)
	}
	if err := v.Verify(ctx, req); !errors.Is(err, ErrReplayed) {
		t.Fatalf("Verify after Record: err = %v, want ErrReplayed", err)
	}
}

// Record refuses a CheckedProof Check did not make, rather than record an
// empty id.
func TestRecordRefusesAnUncheckedProof(t *testing.T) {
	v := newVerifier(t, VerifierConfig{Replay: newReplay(t, MemoryReplayCacheConfig{})})
	if err := v.Record(context.Background(), CheckedProof{}); err == nil || errors.Is(err, ErrReplayed) {
		t.Fatalf("Record of a zero CheckedProof: err = %v, want a refusal that is not ErrReplayed", err)
	}
}
