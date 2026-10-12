package jwks

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"strings"
	"testing"

	"github.com/oleg-tkachuk/limes/internal/jose"
)

// A key a resolver hands out is the caller's own: changing it leaves the key
// the resolver verifies with.
func TestResolversHandOutKeysOfTheirOwn(t *testing.T) {
	kid, pub, _, err := generateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	want := append(ed25519.PublicKey{}, pub...)
	static, err := NewStatic(StaticConfig{Keys: map[string]ed25519.PublicKey{kid: pub}})
	if err != nil {
		t.Fatal(err)
	}
	_, remote, _, remoteKid, remotePub := newRemoteFixture(t)
	wantRemote := append(ed25519.PublicKey{}, remotePub...)
	type resolver interface {
		PublicKey(ctx context.Context, kid string) (ed25519.PublicKey, error)
	}
	for name, tc := range map[string]struct {
		r    resolver
		kid  string
		want ed25519.PublicKey
	}{
		"Static": {static, kid, want},
		"Remote": {remote, remoteKid, wantRemote},
	} {
		got, err := tc.r.PublicKey(context.Background(), tc.kid)
		if err != nil {
			t.Fatal(err)
		}
		got[0] ^= 0xff
		again, err := tc.r.PublicKey(context.Background(), tc.kid)
		if err != nil || !again.Equal(tc.want) {
			t.Errorf("%s: the key after changing one handed out = %x, %v; want %x", name, again, err, tc.want)
		}
	}
	snapshot := static.Keys()
	snapshot[kid][0] ^= 0xff
	if got, _ := static.PublicKey(context.Background(), kid); !got.Equal(want) {
		t.Errorf("Static key after changing a Keys snapshot = %x, want %x", got, want)
	}
}

// An Ed25519 key labelled with any algorithm but EdDSA's is skipped: the
// label says what the key may sign with, and that is not this verifier's.
func TestParseSkipsAKeyForAnotherAlgorithm(t *testing.T) {
	_, pub, _, err := generateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	entry := map[string]string{"kty": jose.KtyOKP, "crv": jose.CrvEd25519, "kid": "k", "x": jose.B64(pub), "alg": "RS256"}
	doc, _ := json.Marshal(map[string]any{"keys": []any{entry}})
	keys, skipped, err := parse(doc)
	if err != nil || len(keys) != 0 || skipped != 1 {
		t.Errorf("parse of a key labelled RS256 = %d keys, %d skipped, %v; want it skipped", len(keys), skipped, err)
	}
}

// A key's x is read strictly: base64url with its unused trailing bits set is
// another spelling of the same key, and is skipped rather than accepted.
func TestParseReadsXStrictly(t *testing.T) {
	_, pub, _, err := generateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	x := jose.B64(pub)
	// 32 bytes take 43 characters, whose last two bits are unused.
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	last := strings.IndexByte(alphabet, x[len(x)-1])
	loose := x[:len(x)-1] + string(alphabet[last|1])
	for name, tc := range map[string]struct {
		x    string
		want int
	}{
		"canonical":         {x, 1},
		"trailing bits set": {loose, 0},
	} {
		entry := map[string]string{"kty": jose.KtyOKP, "crv": jose.CrvEd25519, "kid": "k", "x": tc.x}
		doc, _ := json.Marshal(map[string]any{"keys": []any{entry}})
		if keys, _, err := parse(doc); err != nil || len(keys) != tc.want {
			t.Errorf("%s: parse = %d keys, %v; want %d", name, len(keys), err, tc.want)
		}
	}
}
