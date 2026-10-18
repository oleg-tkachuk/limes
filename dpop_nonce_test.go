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
	nonces, err := dpop.NewHMACNonces(dpop.HMACNoncesConfig{Lifetime: time.Minute, Keys: [][]byte{nonceKey(1)}})
	if err != nil {
		t.Fatal(err)
	}
	cfg := base.cfg
	cfg.DPoP = newVerifier(t, dpop.VerifierConfig{Replay: newReplay(t, dpop.MemoryReplayCacheConfig{}), Nonces: nonces})
	v, err := NewVerifier(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	key := dpopKeys(t)["Ed25519"]
	token := issueBound(t, issuer, key)
	proof := func(nonce string) VerifyRequest {
		p, err := dpop.Sign(ctx, key, dpop.SignRequest{Method: dpopMethod, URL: dpopURL, Token: token, Nonce: nonce, IssuedAt: time.Now()})
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

// holderLog records the holder of every proof id it is asked about.
type holderLog struct{ holders []string }

func (h *holderLog) Seen(_ context.Context, e dpop.ReplayEntry) (bool, error) {
	h.holders = append(h.holders, e.Holder)
	return false, nil
}

// A proof counts against the capability the token carries, so one capability
// fills one share of the replay cache whatever keys its copies are bound to.
func TestAProofCountsAgainstItsCapability(t *testing.T) {
	issuer, base, _, _ := buildIssuerVerifier(t)
	log := &holderLog{}
	cfg := base.cfg
	cfg.DPoP = newVerifier(t, dpop.VerifierConfig{Replay: log})
	v, err := NewVerifier(cfg)
	if err != nil {
		t.Fatal(err)
	}
	key := dpopKeys(t)["Ed25519"]
	token := issueBound(t, issuer, key)
	got, err := v.Verify(t.Context(), withProof(t.Context(), t, key, token, audienceData, time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	if want := got.Capability().ID.String(); len(log.holders) != 1 || log.holders[0] != want {
		t.Errorf("proof counted against %q, want the capability %s", log.holders, want)
	}
}
