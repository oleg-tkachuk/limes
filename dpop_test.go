package limes

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
)

const (
	dpopURL    = "https://data.example.com/example.data.v1.ObjectService/GetObject"
	dpopMethod = "POST"
)

// withProof is a VerifyRequest for token carrying a fresh proof from key,
// issued at now.
func withProof(t *testing.T, key crypto.Signer, token, audience string, now time.Time) VerifyRequest {
	t.Helper()
	proof, err := NewDPoPProof(key, DPoPProofRequest{Method: dpopMethod, URL: dpopURL, Token: token, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	return VerifyRequest{Token: token, Audience: audience, DPoP: DPoPProof{Proof: proof, Method: dpopMethod, URL: dpopURL}}
}

func dpopKeys(t *testing.T) map[string]crypto.Signer {
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

func boundCap(t *testing.T, key crypto.Signer) Capability {
	t.Helper()
	jkt, err := KeyThumbprint(key.Public())
	if err != nil {
		t.Fatal(err)
	}
	return Capability{ID: uuid.New(), ConfirmationJKT: jkt}
}

func TestDPoPAcceptsAProofFromTheBoundKey(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	for name, key := range dpopKeys(t) {
		t.Run(name, func(t *testing.T) {
			c := boundCap(t, key)
			v := &DPoPVerifier{Replay: NewMemoryReplayCache(0), Now: func() time.Time { return now }}
			proof, err := NewDPoPProof(key, DPoPProofRequest{Method: "POST", URL: dpopURL + "?x=1", Token: "the-token", Now: now})
			if err != nil {
				t.Fatal(err)
			}
			req := DPoPProof{Proof: proof, Method: "POST", URL: dpopURL}
			if err := v.check(context.Background(), c, "the-token", req); err != nil {
				t.Fatalf("valid proof refused: %v", err)
			}
			if err := v.check(context.Background(), c, "the-token", req); !errors.Is(err, ErrDPoPReplayed) {
				t.Fatalf("replayed proof: err = %v, want ErrDPoPReplayed", err)
			}
		})
	}
}

func TestDPoPRefusals(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	keys := dpopKeys(t)
	key, other := keys["Ed25519"], keys["P-256"]
	c := boundCap(t, key)
	fresh := func(k crypto.Signer, method, url, token string, at time.Time) string {
		p, err := NewDPoPProof(k, DPoPProofRequest{Method: method, URL: url, Token: token, Now: at})
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	base := DPoPProof{Method: "POST", URL: dpopURL}
	cases := map[string]struct {
		proof string
		want  error
	}{
		"no proof":           {"", ErrDPoPRequired},
		"another key":        {fresh(other, "POST", dpopURL, "the-token", now), ErrDPoPInvalid},
		"another method":     {fresh(key, "GET", dpopURL, "the-token", now), ErrDPoPInvalid},
		"another path":       {fresh(key, "POST", "https://data.example.com/other", "the-token", now), ErrDPoPInvalid},
		"another host":       {fresh(key, "POST", "https://evil.example.com/example.data.v1.ObjectService/GetObject", "the-token", now), ErrDPoPInvalid},
		"another token":      {fresh(key, "POST", dpopURL, "a-stolen-token", now), ErrDPoPInvalid},
		"stale":              {fresh(key, "POST", dpopURL, "the-token", now.Add(-2*time.Minute)), ErrDPoPInvalid},
		"from the future":    {fresh(key, "POST", dpopURL, "the-token", now.Add(2*time.Minute)), ErrDPoPInvalid},
		"tampered signature": {fresh(key, "POST", dpopURL, "the-token", now) + "AA", ErrDPoPInvalid},
		"garbage":            {"not.a.proof", ErrDPoPInvalid},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			v := &DPoPVerifier{Replay: NewMemoryReplayCache(0), Now: func() time.Time { return now }}
			req := base
			req.Proof = tc.proof
			err := v.check(context.Background(), c, "the-token", req)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if !errors.Is(err, ErrProofOfPossession) || errors.Is(err, ErrInvalidToken) {
				t.Fatalf("%v: want ErrProofOfPossession and not ErrInvalidToken", err)
			}
		})
	}
}

func TestDPoPPathOnlyIgnoresHostButNotPath(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	key := dpopKeys(t)["Ed25519"]
	c := boundCap(t, key)
	v := &DPoPVerifier{Replay: NewMemoryReplayCache(0), Now: func() time.Time { return now }, MatchPathOnly: true}
	proof, _ := NewDPoPProof(key, DPoPProofRequest{Method: "POST", URL: dpopURL, Token: "tok", Now: now})
	req := DPoPProof{Proof: proof, Method: "POST", URL: "http://10.0.0.7:8080/example.data.v1.ObjectService/GetObject"}
	if err := v.check(context.Background(), c, "tok", req); err != nil {
		t.Fatalf("path-only match behind a proxy: %v", err)
	}
	proof, _ = NewDPoPProof(key, DPoPProofRequest{Method: "POST", URL: "https://gw.example.com/api/example.data.v1.ObjectService/GetObject", Token: "tok", Now: now})
	req.Proof = proof
	if err := v.check(context.Background(), c, "tok", req); err != nil {
		t.Fatalf("path-only match behind a prefix-stripping proxy: %v", err)
	}
	proof, _ = NewDPoPProof(key, DPoPProofRequest{Method: "POST", URL: dpopURL, Token: "tok", Now: now})
	req.Proof, req.URL = proof, "http://10.0.0.7:8080/example.data.v1.ObjectService/DeleteObject"
	if err := v.check(context.Background(), c, "tok", req); !errors.Is(err, ErrDPoPInvalid) {
		t.Fatalf("path-only with another path: err = %v, want ErrDPoPInvalid", err)
	}
}

// RFC 7638 §3.1's worked example.
func TestThumbprintMatchesRFC7638(t *testing.T) {
	// The RFC's example is RSA; for OKP, RFC 8037 §A.3 gives one.
	j := DPoPJWK{Kty: "OKP", Crv: "Ed25519", X: "11qYAYKxCrfVS_7TyWQHOg7hcvPapiMlrwIaaPcHURo"}
	got, err := j.Thumbprint()
	if err != nil {
		t.Fatal(err)
	}
	if want := "kPrK_qmxVWaYVA9wwBF6Iuo3vVzz7TxHCTwXBygrS4k"; got != want {
		t.Fatalf("thumbprint = %s, want %s", got, want)
	}
}

func TestKeyBindingSurvivesTheTokenAndDelegation(t *testing.T) {
	issuer, verifier, _, _ := buildIssuerVerifier(t)
	ctx := context.Background()
	key := dpopKeys(t)["Ed25519"]
	jkt, _ := KeyThumbprint(key.Public())

	parent, token, err := issuer.Issue(ctx, IssueRequest{
		IssuedBy: Principal{Subject: "op"}, Subject: Principal{Type: PrincipalAgent, TenantID: uuid.New(), Subject: "a"},
		Audience: []string{audienceData}, Caveats: Caveats{Ops: []Op{OpGet}}, TTL: time.Hour,
		ConfirmationJKT: jkt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifier.Verify(ctx, VerifyRequest{Token: token, Audience: audienceData}); !errors.Is(err, ErrDPoPRequired) {
		t.Fatalf("bound token without a proof: err = %v, want ErrDPoPRequired", err)
	}
	got, err := verifier.Verify(ctx, withProof(t, key, token, audienceData, time.Now()))
	if err != nil || got.ConfirmationJKT != jkt {
		t.Fatalf("verified binding = %q, %v; want %q", got.ConfirmationJKT, err, jkt)
	}

	child, _, err := issuer.Delegate(ctx, DelegateRequest{Subject: Principal{Type: PrincipalAgent, Subject: "child"}, Parent: parent, InheritCaveats: true})
	if err != nil || child.ConfirmationJKT != jkt {
		t.Fatalf("child without its own key: binding %q, %v; want the parent's", child.ConfirmationJKT, err)
	}
	subKey := dpopKeys(t)["P-256"]
	subJKT, _ := KeyThumbprint(subKey.Public())
	child, _, err = issuer.Delegate(ctx, DelegateRequest{Subject: Principal{Type: PrincipalAgent, Subject: "child"}, Parent: parent, InheritCaveats: true, ConfirmationJKT: subJKT})
	if err != nil || child.ConfirmationJKT != subJKT {
		t.Fatalf("child bound to the sub-agent's key: %q, %v", child.ConfirmationJKT, err)
	}

	unbound := child
	unbound.ConfirmationJKT = ""
	if err := Narrows(parent, unbound); !errors.Is(err, ErrDelegationTooWide) {
		t.Fatalf("unbound child of a bound parent: err = %v, want ErrDelegationTooWide", err)
	}
}

func TestMemoryReplayCacheIsBoundedAndForgetsExpired(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	c := NewMemoryReplayCache(2)
	c.now = func() time.Time { return now }
	if c.Seen(context.Background(), "k", "a", now.Add(time.Minute)) || c.Seen(context.Background(), "k", "b", now.Add(time.Second)) {
		t.Fatal("fresh ids reported seen")
	}
	if !c.Seen(context.Background(), "k", "c", now.Add(time.Minute)) {
		t.Fatal("a full cache accepted an id it could not record")
	}
	now = now.Add(2 * time.Second) // b expires
	if c.Seen(context.Background(), "k", "c", now.Add(time.Minute)) {
		t.Fatal("an id refused while full was not accepted once room freed up")
	}
	if !c.Seen(context.Background(), "k", "a", now.Add(time.Minute)) {
		t.Fatal("a live id was forgotten")
	}
}

func TestIssueRefusesAMalformedBinding(t *testing.T) {
	issuer, _, _, _ := buildIssuerVerifier(t)
	_, _, err := issuer.Issue(context.Background(), IssueRequest{
		IssuedBy: Principal{Subject: "op"}, Subject: Principal{Type: PrincipalAgent, TenantID: uuid.New(), Subject: "a"},
		Audience: []string{audienceData}, Caveats: Caveats{Ops: []Op{OpGet}}, TTL: time.Hour,
		ConfirmationJKT: "not-a-thumbprint",
	})
	if err == nil {
		t.Fatal("malformed ConfirmationJKT accepted")
	}
}

// opaqueSigner hides the concrete key type, as a KMS or HSM signer does.
type opaqueSigner struct{ crypto.Signer }

func TestDPoPProofFromAnOpaqueSigner(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	for name, key := range dpopKeys(t) {
		t.Run(name, func(t *testing.T) {
			c := boundCap(t, key)
			proof, err := NewDPoPProof(opaqueSigner{key}, DPoPProofRequest{Method: "POST", URL: dpopURL, Token: "tok", Now: now})
			if err != nil {
				t.Fatal(err)
			}
			v := &DPoPVerifier{Replay: NewMemoryReplayCache(0), Now: func() time.Time { return now }}
			if err := v.check(context.Background(), c, "tok", DPoPProof{Proof: proof, Method: "POST", URL: dpopURL}); err != nil {
				t.Fatalf("proof from an opaque signer refused: %v", err)
			}
		})
	}
}

// Expired ids leave in expiry order as time passes, whatever order they
// came in, and only they do.
func TestMemoryReplayCacheForgetsInExpiryOrder(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	c := NewMemoryReplayCache(0)
	c.now = func() time.Time { return now }
	for i, d := range []time.Duration{5, 1, 4, 2, 3} {
		if c.Seen(ctx, "k", fmt.Sprint(i), now.Add(d*time.Second)) {
			t.Fatalf("fresh id %d reported seen", i)
		}
	}
	now = now.Add(3 * time.Second) // the ids expiring at 1s, 2s and 3s lapse
	_ = c.Seen(ctx, "k", "probe", now.Add(time.Hour))
	if got := len(c.seen); got != 3 {
		t.Errorf("after the clock passed three expiries, %d ids are held, want 3", got)
	}
	for _, live := range []string{"0", "2"} {
		if !c.Seen(ctx, "k", live, now.Add(time.Hour)) {
			t.Errorf("live id %s was forgotten", live)
		}
	}
}

// BenchmarkMemoryReplayCacheFull measures a new id offered to a full cache
// in which nothing has expired — the case a flood of proofs produces.
func BenchmarkMemoryReplayCacheFull(b *testing.B) {
	const entries = 100_000
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	c := NewMemoryReplayCache(entries)
	c.now = func() time.Time { return now }
	for i := range entries {
		c.Seen(ctx, fmt.Sprint(i), fmt.Sprint(i), now.Add(time.Hour))
	}
	b.ResetTimer()
	for i := 0; b.Loop(); i++ {
		c.Seen(ctx, fmt.Sprint("new", i), fmt.Sprint("new", i), now.Add(time.Hour))
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
	c := boundCap(t, priv)
	jwk, err := PublicJWK(priv.Public())
	if err != nil {
		t.Fatal(err)
	}
	header := fmt.Sprintf(`{"typ":%q,"alg":%q,"jwk":{"kty":%q,"crv":%q,"x":%q,"d":%q}}`,
		DPoPTokenType, algEdDSA, jwk.Kty, jwk.Crv, jwk.X, b64(priv.Seed()))
	claims := fmt.Sprintf(`{"jti":"j1","htm":"POST","htu":%q,"iat":%d,"ath":%q}`,
		dpopURL, now.Unix(), AccessTokenHash("the-token"))
	input := b64([]byte(header)) + "." + b64([]byte(claims))
	proof := input + "." + b64(ed25519.Sign(priv, []byte(input)))

	v := &DPoPVerifier{Replay: NewMemoryReplayCache(0), Now: func() time.Time { return now }}
	err = v.check(context.Background(), c, "the-token", DPoPProof{Proof: proof, Method: "POST", URL: dpopURL})
	if !errors.Is(err, ErrDPoPInvalid) {
		t.Fatalf("a proof carrying its private key: err = %v, want ErrDPoPInvalid", err)
	}
}

// One key cannot fill the cache: past its share only its own proofs are
// refused, and every other key's fresh proof is still recorded.
func TestMemoryReplayCacheBoundsEachKey(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	const share = 3
	c := NewMemoryReplayCache(100, WithEntriesPerKey(share))
	c.now = func() time.Time { return now }
	for i := range share {
		if c.Seen(ctx, "noisy", fmt.Sprint(i), now.Add(time.Minute)) {
			t.Fatalf("proof %d of the noisy key refused within its share", i)
		}
	}
	if !c.Seen(ctx, "noisy", "one more", now.Add(time.Minute)) {
		t.Error("the noisy key recorded past its share")
	}
	if c.Seen(ctx, "quiet", "first", now.Add(time.Minute)) {
		t.Error("another key's fresh proof refused because one key filled its share")
	}
	// The same id under two keys is two proofs.
	if c.Seen(ctx, "other", "0", now.Add(time.Minute)) {
		t.Error("an id another key used was refused")
	}
	now = now.Add(2 * time.Minute) // everything lapses, and the share with it
	if c.Seen(ctx, "noisy", "later", now.Add(time.Minute)) {
		t.Error("the noisy key's share did not free as its ids lapsed")
	}
	if got := NewMemoryReplayCache(2, WithEntriesPerKey(10)).maxPerKey; got != 2 {
		t.Errorf("a share larger than the cache = %d, want the cache's size", got)
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
	c := boundCap(t, priv)
	jwk, _ := PublicJWK(priv.Public())
	sign := func(claims string) string {
		header := fmt.Sprintf(`{"typ":%q,"alg":%q,"jwk":{"kty":%q,"crv":%q,"x":%q}}`, DPoPTokenType, algEd25519, jwk.Kty, jwk.Crv, jwk.X)
		input := b64([]byte(header)) + "." + b64([]byte(claims))
		return input + "." + b64(ed25519.Sign(priv, []byte(input)))
	}
	claims := func(jti, iat string) string {
		return fmt.Sprintf(`{"jti":%q,"htm":"POST","htu":%q,"iat":%s,"ath":%q}`, jti, dpopURL, iat, AccessTokenHash("tok"))
	}
	v := &DPoPVerifier{Replay: NewMemoryReplayCache(0), Now: func() time.Time { return now }}
	ctx := context.Background()
	if err := v.check(ctx, c, "tok", DPoPProof{Proof: sign(claims("j1", "1800000000")), Method: "post", URL: dpopURL}); !errors.Is(err, ErrDPoPInvalid) {
		t.Errorf("a POST proof for a post request: err = %v, want ErrDPoPInvalid", err)
	}
	if err := v.check(ctx, c, "tok", DPoPProof{Proof: sign(claims("j2", "1800000000.25")), Method: "POST", URL: dpopURL}); err != nil {
		t.Errorf("a proof with a fractional iat: %v", err)
	}
}
