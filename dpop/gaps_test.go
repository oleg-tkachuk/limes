package dpop

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/oleg-tkachuk/limes/internal/jose"
)

// errNoncesDown is a nonce store that cannot answer.
var errNoncesDown = errors.New("nonce store down")

type downNonces struct{}

func (downNonces) Nonce(context.Context) (string, error)       { return "", errNoncesDown }
func (downNonces) Valid(context.Context, string) (bool, error) { return false, errNoncesDown }

// A nonce store that cannot answer refuses the proof: an unanswered check is
// never read as a valid nonce.
func TestVerifyFailsClosedWhenTheNonceCannotBeChecked(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	v := newVerifier(t, VerifierConfig{
		Replay: newReplay(t, MemoryReplayCacheConfig{}), Nonces: downNonces{}, Now: func() time.Time { return now },
	})
	proof, err := Sign(context.Background(), priv, ProofRequest{Method: "GET", URL: dpopURL, Token: "tok", Nonce: "n", IssuedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	err = v.Verify(context.Background(), Request{Proof: Proof{JWT: proof, Method: "GET", URL: dpopURL}, Token: "tok", JKT: boundJKT(t, priv), Holder: testHolder})
	if !errors.Is(err, errNoncesDown) {
		t.Errorf("Verify with the nonce store down: err = %v, want its error", err)
	}
}

// A proof with no jti cannot be told from its replay, so it is refused.
func TestVerifyRefusesAProofWithoutAJTI(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	jwk, _ := publicJWK(priv.Public())
	header := fmt.Sprintf(`{"typ":%q,"alg":%q,"jwk":{"kty":%q,"crv":%q,"x":%q}}`, tokenType, jose.AlgEd25519, jwk.Kty, jwk.Crv, jwk.X)
	claims := fmt.Sprintf(`{"jti":"","htm":"GET","htu":%q,"iat":%d,"ath":%q}`, dpopURL, now.Unix(), accessTokenHash("tok"))
	input := jose.B64([]byte(header)) + "." + jose.B64([]byte(claims))
	proof := input + "." + jose.B64(ed25519.Sign(priv, []byte(input)))
	v := newVerifier(t, VerifierConfig{Replay: newReplay(t, MemoryReplayCacheConfig{}), Now: func() time.Time { return now }})
	err = v.Verify(context.Background(), Request{Proof: Proof{JWT: proof, Method: "GET", URL: dpopURL}, Token: "tok", JKT: boundJKT(t, priv), Holder: testHolder})
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("a proof without a jti: err = %v, want ErrInvalid", err)
	}
}

// The cache's bound holds across keys, not only within one: ids of many keys
// cannot together grow it past MaxEntries.
func TestMemoryReplayCacheBoundsAllKeysTogether(t *testing.T) {
	const entries = 2
	now := time.Unix(1_800_000_000, 0)
	c := newReplay(t, MemoryReplayCacheConfig{MaxEntries: entries, Now: func() time.Time { return now }})
	expires := now.Add(time.Minute)
	for i := range entries {
		if seen(t, c, fmt.Sprintf("key-%d", i), "a", expires) {
			t.Fatalf("id %d of %d refused while there was room", i+1, entries)
		}
	}
	if !seen(t, c, "another-key", "a", expires) {
		t.Error("a full cache recorded an id of yet another key")
	}
}
