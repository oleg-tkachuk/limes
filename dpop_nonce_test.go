package limes

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/oleg-tkachuk/limes/dpop"
	"github.com/oleg-tkachuk/limes/internal/jose"
)

func nonceKey(b byte) []byte { return bytes.Repeat([]byte{b}, dpop.HMACNonceKeyBytes) }

// RFC 9449 §8: with nonces required, a proof without one — or with one the
// server did not issue — is refused with use_dpop_nonce, and the server's
// nonce lets the retry through.
func TestVerifyRequiresTheServerNonce(t *testing.T) {
	issuer, base, _, _ := buildIssuerVerifier(t)
	nonces, err := dpop.NewHMACNonces(time.Minute, nonceKey(1))
	if err != nil {
		t.Fatal(err)
	}
	cfg := base.cfg
	cfg.DPoP = &dpop.Verifier{Replay: dpop.NewMemoryReplayCache(0), Nonces: nonces}
	v, err := NewStandardVerifier(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	key := dpopKeys(t)["Ed25519"]
	token := issueBound(t, issuer, key)
	proof := func(nonce string) VerifyRequest {
		p, err := dpop.Sign(key, dpop.ProofRequest{Method: dpopMethod, URL: dpopURL, Token: token, Nonce: nonce, Now: time.Now()})
		if err != nil {
			t.Fatal(err)
		}
		return VerifyRequest{Token: token, Audience: audienceData, DPoP: dpop.Proof{JWT: p, Method: dpopMethod, URL: dpopURL}}
	}
	for name, nonce := range map[string]string{"no nonce": "", "a forged nonce": jose.B64(make([]byte, 40))} {
		if _, err := v.Verify(ctx, proof(nonce)); !errors.Is(err, dpop.ErrNonceRequired) || !errors.Is(err, ErrProofOfPossession) {
			t.Errorf("%s: err = %v, want dpop.ErrNonceRequired", name, err)
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
