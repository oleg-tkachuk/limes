package dpop

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/oleg-tkachuk/limes/internal/jose"
)

// A proof's jti is what a replay cache keeps, so one that holds a NUL —
// which a JSON string can carry and a store's text cannot — is an invalid
// proof, refused before any cache is asked; the same proof with a plain
// jti verifies.
func TestProofRefusesAJTINoCacheCanKeep(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{3}, ed25519.SeedSize))
	jkt, err := Thumbprint(key.Public())
	if err != nil {
		t.Fatal(err)
	}
	proof, err := Sign(context.Background(), key, SignRequest{Method: dpopMethod, URL: dpopURL, Token: "tok", IssuedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(proof, ".")
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims map[string]any
	if err := json.Unmarshal(raw, &claims); err != nil {
		t.Fatal(err)
	}
	withJTI := func(jti string) string {
		claims["jti"] = jti
		payload, err := json.Marshal(claims)
		if err != nil {
			t.Fatal(err)
		}
		input := parts[0] + "." + jose.B64(payload)
		return input + "." + jose.B64(ed25519.Sign(key, []byte(input)))
	}
	verify := func(p string) error {
		v := newVerifier(t, VerifierConfig{Replay: newReplay(t, MemoryReplayCacheConfig{}), Now: func() time.Time { return now }})
		return v.Verify(context.Background(), Request{Proof: Proof{JWT: p, Method: dpopMethod, URL: dpopURL}, Token: "tok", JKT: jkt, Holder: testHolder})
	}
	if err := verify(withJTI("plain-jti")); err != nil {
		t.Fatalf("a plain jti: %v", err)
	}
	if err := verify(withJTI("a\x00b")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a jti with a NUL: %v, want ErrInvalid", err)
	}
}
