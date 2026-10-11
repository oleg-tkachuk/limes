package limes

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"testing"
	"time"

	"github.com/oleg-tkachuk/limes/jwks"
)

// RFC 8037 appendix A: the Ed25519 public key and its RFC 7638 thumbprint.
const (
	rfc8037X          = "11qYAYKxCrfVS_7TyWQHOg7hcvPapiMlrwIaaPcHURo"
	rfc8037Thumbprint = "kPrK_qmxVWaYVA9wwBF6Iuo3vVzz7TxHCTwXBygrS4k"
)

// Thumbprints match the RFC's published value for an OKP key, and the
// RFC 7638 canonical form, written out by hand, for an EC key.
func TestThumbprintKnownAnswers(t *testing.T) {
	if got, err := (DPoPJWK{Kty: "OKP", Crv: "Ed25519", X: rfc8037X}).Thumbprint(); err != nil || got != rfc8037Thumbprint {
		t.Errorf("OKP thumbprint = %q, %v; want %q", got, err, rfc8037Thumbprint)
	}
	ec := DPoPJWK{Kty: "EC", Crv: "P-256", X: "x-coordinate", Y: "y-coordinate"}
	sum := sha256.Sum256([]byte(`{"crv":"P-256","kty":"EC","x":"x-coordinate","y":"y-coordinate"}`))
	if got, err := ec.Thumbprint(); err != nil || got != base64.RawURLEncoding.EncodeToString(sum[:]) {
		t.Errorf("EC thumbprint = %q, %v; want the hash of the RFC 7638 canonical form", got, err)
	}
}

// A zero config field takes the default its documentation names.
func TestConfigDefaults(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := NewEd25519Signer("k1", priv)
	if err != nil {
		t.Fatal(err)
	}
	iss, err := NewIssuer(IssuerConfig{Signer: signer, Store: newMemStore(), IssuerName: goldenIssuer})
	if err != nil {
		t.Fatal(err)
	}
	if iss.defaultTTL != 15*time.Minute {
		t.Errorf("Issuer default TTL = %s, want 15m", iss.defaultTTL)
	}

	v, err := NewStandardVerifier(VerifierConfig{
		Keys:           jwks.NewStatic(map[string]ed25519.PublicKey{"k1": pub}),
		Revocations:    newMemStore(),
		TrustedIssuers: []string{goldenIssuer},
	})
	if err != nil {
		t.Fatal(err)
	}
	if v.cfg.Leeway != 30*time.Second {
		t.Errorf("verifier leeway = %s, want 30s", v.cfg.Leeway)
	}

	if c := NewMemoryReplayCache(0); c.maxEntries != 100_000 {
		t.Errorf("replay cache bound = %d, want 100 000", c.maxEntries)
	}
}

// A negative duration or size is refused, not taken as given: a negative
// default TTL mints capabilities already expired, a negative leeway narrows
// the validity window, and a negative size refuses every token.
func TestConfigRefusesNegatives(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := NewEd25519Signer("k1", priv)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewIssuer(IssuerConfig{Signer: signer, Store: newMemStore(), IssuerName: goldenIssuer, DefaultTTL: -time.Minute}); err == nil {
		t.Error("NewIssuer accepted a negative DefaultTTL")
	}

	base := VerifierConfig{
		Keys:           jwks.NewStatic(map[string]ed25519.PublicKey{"k1": pub}),
		Revocations:    newMemStore(),
		TrustedIssuers: []string{goldenIssuer},
	}
	for name, mutate := range map[string]func(*VerifierConfig){
		"Leeway":        func(c *VerifierConfig) { c.Leeway = -time.Second },
		"MaxTokenBytes": func(c *VerifierConfig) { c.MaxTokenBytes = -1 },
	} {
		cfg := base
		mutate(&cfg)
		if _, err := NewStandardVerifier(cfg); err == nil {
			t.Errorf("NewStandardVerifier accepted a negative %s", name)
		}
	}
}
