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

const dpopURL = "https://data.example.com/paladin.data.v1.ObjectService/GetObject"

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
			proof, err := NewDPoPProof(key, "post", dpopURL+"?x=1", "the-token", now)
			if err != nil {
				t.Fatal(err)
			}
			req := DPoPRequest{Proof: proof, Method: "POST", URL: dpopURL, Token: "the-token"}
			if err := v.Check(context.Background(), c, req); err != nil {
				t.Fatalf("valid proof refused: %v", err)
			}
			if err := v.Check(context.Background(), c, req); !errors.Is(err, ErrDPoPReplayed) {
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
		p, err := NewDPoPProof(k, method, url, token, at)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	base := DPoPRequest{Method: "POST", URL: dpopURL, Token: "the-token"}
	cases := map[string]struct {
		proof string
		mod   func(*DPoPRequest)
		want  error
	}{
		"no proof":           {"", nil, ErrDPoPRequired},
		"another key":        {fresh(other, "POST", dpopURL, "the-token", now), nil, ErrDPoPInvalid},
		"another method":     {fresh(key, "GET", dpopURL, "the-token", now), nil, ErrDPoPInvalid},
		"another path":       {fresh(key, "POST", "https://data.example.com/other", "the-token", now), nil, ErrDPoPInvalid},
		"another host":       {fresh(key, "POST", "https://evil.example.com/paladin.data.v1.ObjectService/GetObject", "the-token", now), nil, ErrDPoPInvalid},
		"another token":      {fresh(key, "POST", dpopURL, "a-stolen-token", now), nil, ErrDPoPInvalid},
		"stale":              {fresh(key, "POST", dpopURL, "the-token", now.Add(-2*time.Minute)), nil, ErrDPoPInvalid},
		"from the future":    {fresh(key, "POST", dpopURL, "the-token", now.Add(2*time.Minute)), nil, ErrDPoPInvalid},
		"tampered signature": {fresh(key, "POST", dpopURL, "the-token", now) + "AA", nil, ErrDPoPInvalid},
		"garbage":            {"not.a.proof", nil, ErrDPoPInvalid},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			v := &DPoPVerifier{Replay: NewMemoryReplayCache(0), Now: func() time.Time { return now }}
			req := base
			req.Proof = tc.proof
			if tc.mod != nil {
				tc.mod(&req)
			}
			err := v.Check(context.Background(), c, req)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if !errors.Is(err, ErrInvalidSignature) {
				t.Fatalf("%v does not match ErrInvalidSignature", err)
			}
		})
	}
}

func TestDPoPPathOnlyIgnoresHostButNotPath(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	key := dpopKeys(t)["Ed25519"]
	c := boundCap(t, key)
	v := &DPoPVerifier{Replay: NewMemoryReplayCache(0), Now: func() time.Time { return now }}
	proof, _ := NewDPoPProof(key, "POST", dpopURL, "tok", now)
	req := DPoPRequest{Proof: proof, Method: "POST", URL: "http://10.0.0.7:8080/paladin.data.v1.ObjectService/GetObject", MatchPathOnly: true, Token: "tok"}
	if err := v.Check(context.Background(), c, req); err != nil {
		t.Fatalf("path-only match behind a proxy: %v", err)
	}
	proof, _ = NewDPoPProof(key, "POST", "https://gw.example.com/paladin-api/paladin.data.v1.ObjectService/GetObject", "tok", now)
	req.Proof = proof
	if err := v.Check(context.Background(), c, req); err != nil {
		t.Fatalf("path-only match behind a prefix-stripping proxy: %v", err)
	}
	proof, _ = NewDPoPProof(key, "POST", dpopURL, "tok", now)
	req.Proof, req.URL = proof, "http://10.0.0.7:8080/paladin.data.v1.ObjectService/DeleteObject"
	if err := v.Check(context.Background(), c, req); !errors.Is(err, ErrDPoPInvalid) {
		t.Fatalf("path-only with another path: err = %v, want ErrDPoPInvalid", err)
	}
}

func TestDPoPUnboundCapabilityNeedsNoProof(t *testing.T) {
	v := &DPoPVerifier{Replay: NewMemoryReplayCache(0)}
	if err := v.Check(context.Background(), Capability{}, DPoPRequest{}); err != nil {
		t.Fatalf("unbound capability: %v", err)
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
		IssuedBy: Principal{Subject: "op"}, Subject: Principal{TenantID: uuid.New(), Subject: "a"},
		Audience: []string{AudiencePlaneData}, Caveats: Caveats{Ops: []Op{OpGet}}, TTL: time.Hour,
		ConfirmationJKT: jkt,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := verifier.Verify(ctx, token, AudiencePlaneData)
	if err != nil || got.ConfirmationJKT != jkt {
		t.Fatalf("verified binding = %q, %v; want %q", got.ConfirmationJKT, err, jkt)
	}

	child, _, err := issuer.Delegate(ctx, DelegateRequest{Parent: parent, InheritCaveats: true})
	if err != nil || child.ConfirmationJKT != jkt {
		t.Fatalf("child without its own key: binding %q, %v; want the parent's", child.ConfirmationJKT, err)
	}
	subKey := dpopKeys(t)["P-256"]
	subJKT, _ := KeyThumbprint(subKey.Public())
	child, _, err = issuer.Delegate(ctx, DelegateRequest{Parent: parent, InheritCaveats: true, ConfirmationJKT: subJKT})
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
	if c.Seen(context.Background(), "a", now.Add(time.Minute)) || c.Seen(context.Background(), "b", now.Add(time.Second)) {
		t.Fatal("fresh ids reported seen")
	}
	if !c.Seen(context.Background(), "c", now.Add(time.Minute)) {
		t.Fatal("a full cache accepted an id it could not record")
	}
	now = now.Add(2 * time.Second) // b expires
	if c.Seen(context.Background(), "c", now.Add(time.Minute)) {
		t.Fatal("an id refused while full was not accepted once room freed up")
	}
	if !c.Seen(context.Background(), "a", now.Add(time.Minute)) {
		t.Fatal("a live id was forgotten")
	}
}

func TestIssueRefusesAMalformedBinding(t *testing.T) {
	issuer, _, _, _ := buildIssuerVerifier(t)
	_, _, err := issuer.Issue(context.Background(), IssueRequest{
		IssuedBy: Principal{Subject: "op"}, Subject: Principal{TenantID: uuid.New(), Subject: "a"},
		Audience: []string{AudiencePlaneData}, Caveats: Caveats{Ops: []Op{OpGet}}, TTL: time.Hour,
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
			proof, err := NewDPoPProof(opaqueSigner{key}, "POST", dpopURL, "tok", now)
			if err != nil {
				t.Fatal(err)
			}
			v := &DPoPVerifier{Replay: NewMemoryReplayCache(0), Now: func() time.Time { return now }}
			if err := v.Check(context.Background(), c, DPoPRequest{Proof: proof, Method: "POST", URL: dpopURL, Token: "tok"}); err != nil {
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
		if c.Seen(ctx, fmt.Sprint(i), now.Add(d*time.Second)) {
			t.Fatalf("fresh id %d reported seen", i)
		}
	}
	now = now.Add(3 * time.Second) // the ids expiring at 1s, 2s and 3s lapse
	_ = c.Seen(ctx, "probe", now.Add(time.Hour))
	if got := len(c.seen); got != 3 {
		t.Errorf("after the clock passed three expiries, %d ids are held, want 3", got)
	}
	for _, live := range []string{"0", "2"} {
		if !c.Seen(ctx, live, now.Add(time.Hour)) {
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
		c.Seen(ctx, fmt.Sprint(i), now.Add(time.Hour))
	}
	b.ResetTimer()
	for i := 0; b.Loop(); i++ {
		c.Seen(ctx, fmt.Sprint("new", i), now.Add(time.Hour))
	}
}
