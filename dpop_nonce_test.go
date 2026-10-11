package limes

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"
)

func nonceKey(b byte) []byte { return bytes.Repeat([]byte{b}, HMACNonceKeyBytes) }

func TestHMACNonces(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	const lifetime = time.Minute
	n, err := NewHMACNonces(lifetime, nonceKey(1))
	if err != nil {
		t.Fatal(err)
	}
	n.now = func() time.Time { return now }
	nonce, err := n.Nonce(ctx)
	if err != nil || !n.Valid(ctx, nonce) {
		t.Fatalf("a fresh nonce %q, %v is not valid", nonce, err)
	}

	// Another replica with the same key accepts it; one with another does not.
	same, _ := NewHMACNonces(lifetime, nonceKey(1))
	same.now = n.now
	other, _ := NewHMACNonces(lifetime, nonceKey(2))
	other.now = n.now
	if !same.Valid(ctx, nonce) || other.Valid(ctx, nonce) {
		t.Error("a nonce must be valid exactly where its key is")
	}

	// Rotation: the new key issues, the old one still checks.
	rotated, _ := NewHMACNonces(lifetime, nonceKey(2), nonceKey(1))
	rotated.now = n.now
	if !rotated.Valid(ctx, nonce) {
		t.Error("a nonce under the previous key was refused after rotation")
	}

	for name, at := range map[string]time.Time{
		"expired":     now.Add(lifetime + time.Second),
		"from before": now.Add(-HMACNonceClockSkew - time.Second),
	} {
		n.now = func() time.Time { return at }
		if n.Valid(ctx, nonce) {
			t.Errorf("%s: a nonce issued at %s accepted at %s", name, now, at)
		}
	}
	n.now = func() time.Time { return now }
	for _, bad := range []string{"", "not base64!", b64([]byte("short")), nonce[:len(nonce)-2] + "AA"} {
		if n.Valid(ctx, bad) {
			t.Errorf("Valid(%q) = true", bad)
		}
	}
}

func TestNewHMACNoncesRefusesWeakKeys(t *testing.T) {
	if _, err := NewHMACNonces(0); err == nil {
		t.Error("no key accepted")
	}
	if _, err := NewHMACNonces(0, make([]byte, HMACNonceKeyBytes-1)); err == nil {
		t.Error("a short key accepted")
	}
	n, err := NewHMACNonces(0, nonceKey(1))
	if err != nil || n.lifetime != DefaultDPoPNonceLifetime {
		t.Errorf("lifetime = %v, %v; want the default", n, err)
	}
}

// RFC 9449 §8: with nonces required, a proof without one — or with one the
// server did not issue — is refused with use_dpop_nonce, and the server's
// nonce lets the retry through.
func TestVerifyRequiresTheServerNonce(t *testing.T) {
	issuer, base, _, _ := buildIssuerVerifier(t)
	nonces, err := NewHMACNonces(time.Minute, nonceKey(1))
	if err != nil {
		t.Fatal(err)
	}
	cfg := base.cfg
	cfg.DPoP = &DPoPVerifier{Replay: NewMemoryReplayCache(0), Nonces: nonces}
	v, err := NewStandardVerifier(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	key := dpopKeys(t)["Ed25519"]
	token := issueBound(t, issuer, key)
	proof := func(nonce string) VerifyRequest {
		p, err := NewDPoPProof(key, DPoPProofRequest{Method: dpopMethod, URL: dpopURL, Token: token, Nonce: nonce, Now: time.Now()})
		if err != nil {
			t.Fatal(err)
		}
		return VerifyRequest{Token: token, Audience: audienceData, DPoP: DPoPProof{Proof: p, Method: dpopMethod, URL: dpopURL}}
	}
	for name, nonce := range map[string]string{"no nonce": "", "a forged nonce": b64(make([]byte, 40))} {
		if _, err := v.Verify(ctx, proof(nonce)); !errors.Is(err, ErrDPoPNonceRequired) || !errors.Is(err, ErrInvalidSignature) {
			t.Errorf("%s: err = %v, want ErrDPoPNonceRequired", name, err)
		}
	}
	fresh, err := v.DPoPNonce(ctx)
	if err != nil || fresh == "" {
		t.Fatalf("DPoPNonce = %q, %v", fresh, err)
	}
	if _, err := v.Verify(ctx, proof(fresh)); err != nil {
		t.Fatalf("a proof with the server's nonce: %v", err)
	}
	if got, err := base.DPoPNonce(ctx); err != nil || got != "" {
		t.Errorf("a verifier without nonces handed out %q, %v", got, err)
	}
}

// Replicas share keys, not clocks: a replica a little behind the one that
// issued a nonce — across a second boundary, where the issuance second is
// still ahead of it — accepts the nonce; one far behind does not.
func TestHMACNoncesAcrossSkewedReplicas(t *testing.T) {
	ctx := context.Background()
	issuedAt := time.Unix(1_800_000_001, 50_000_000) // just past a second boundary
	a, _ := NewHMACNonces(time.Minute, nonceKey(1))
	a.now = func() time.Time { return issuedAt }
	nonce, err := a.Nonce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := NewHMACNonces(time.Minute, nonceKey(1))
	for name, tc := range map[string]struct {
		behind time.Duration
		ok     bool
	}{
		"100ms behind":    {100 * time.Millisecond, true},
		"within the skew": {HMACNonceClockSkew, true},
		"far behind":      {HMACNonceClockSkew + 2*time.Second, false},
	} {
		b.now = func() time.Time { return issuedAt.Add(-tc.behind) }
		if got := b.Valid(ctx, nonce); got != tc.ok {
			t.Errorf("%s: Valid = %v, want %v", name, got, tc.ok)
		}
	}
}
