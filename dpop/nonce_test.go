package dpop

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/oleg-tkachuk/limes/internal/jose"
)

func TestHMACNonces(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	const lifetime = time.Minute
	n, err := NewHMACNonces(HMACNoncesConfig{Lifetime: lifetime, Keys: SealNonceKeys(nonceKey(1))})
	if err != nil {
		t.Fatal(err)
	}
	n.now = func() time.Time { return now }
	nonce, err := n.Nonce(ctx)
	if err != nil || !valid(t, n, nonce) {
		t.Fatalf("a fresh nonce %q, %v is not valid", nonce, err)
	}

	// Another replica with the same key accepts it; one with another does not.
	same, _ := NewHMACNonces(HMACNoncesConfig{Lifetime: lifetime, Keys: SealNonceKeys(nonceKey(1))})
	same.now = n.now
	other, _ := NewHMACNonces(HMACNoncesConfig{Lifetime: lifetime, Keys: SealNonceKeys(nonceKey(2))})
	other.now = n.now
	if !valid(t, same, nonce) || valid(t, other, nonce) {
		t.Error("a nonce must be valid exactly where its key is")
	}

	// Rotation: the new key issues, the old one still checks.
	rotated, _ := NewHMACNonces(HMACNoncesConfig{Lifetime: lifetime, Keys: SealNonceKeys(nonceKey(2), nonceKey(1))})
	rotated.now = n.now
	if !valid(t, rotated, nonce) {
		t.Error("a nonce under the previous key was refused after rotation")
	}

	for name, at := range map[string]time.Time{
		"expired":     now.Add(lifetime + time.Second),
		"from before": now.Add(-HMACNonceClockSkew - time.Second),
	} {
		n.now = func() time.Time { return at }
		if valid(t, n, nonce) {
			t.Errorf("%s: a nonce issued at %s accepted at %s", name, now, at)
		}
	}
	n.now = func() time.Time { return now }
	for _, bad := range []string{"", "not base64!", jose.B64([]byte("short")), nonce[:len(nonce)-2] + "AA"} {
		if valid(t, n, bad) {
			t.Errorf("Valid(%q) = true", bad)
		}
	}
}

func TestNewHMACNoncesRefusesWeakKeys(t *testing.T) {
	if _, err := NewHMACNonces(HMACNoncesConfig{}); err == nil {
		t.Error("no key accepted")
	}
	if _, err := NewHMACNonces(HMACNoncesConfig{Keys: SealNonceKeys(make([]byte, HMACNonceKeyBytes-1))}); err == nil {
		t.Error("a short key accepted")
	}
	n, err := NewHMACNonces(HMACNoncesConfig{Keys: SealNonceKeys(nonceKey(1))})
	if err != nil || n.lifetime != DefaultNonceLifetime {
		t.Errorf("lifetime = %v, %v; want the default", n, err)
	}
}

// Replicas share keys, not clocks: a replica a little behind the one that
// issued a nonce — across a second boundary, where the issuance second is
// still ahead of it — accepts the nonce; one far behind does not.
func TestHMACNoncesAcrossSkewedReplicas(t *testing.T) {
	ctx := context.Background()
	issuedAt := time.Unix(1_800_000_001, 50_000_000) // just past a second boundary
	a, _ := NewHMACNonces(HMACNoncesConfig{Lifetime: time.Minute, Keys: SealNonceKeys(nonceKey(1))})
	a.now = func() time.Time { return issuedAt }
	nonce, err := a.Nonce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := NewHMACNonces(HMACNoncesConfig{Lifetime: time.Minute, Keys: SealNonceKeys(nonceKey(1))})
	for name, tc := range map[string]struct {
		behind time.Duration
		ok     bool
	}{
		"100ms behind":    {100 * time.Millisecond, true},
		"within the skew": {HMACNonceClockSkew, true},
		"far behind":      {HMACNonceClockSkew + 2*time.Second, false},
	} {
		b.now = func() time.Time { return issuedAt.Add(-tc.behind) }
		if got := valid(t, b, nonce); got != tc.ok {
			t.Errorf("%s: Valid = %v, want %v", name, got, tc.ok)
		}
	}
}

func nonceKey(b byte) []byte { return bytes.Repeat([]byte{b}, HMACNonceKeyBytes) }
