package limes

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"testing"
	"time"
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
		Keys:           NewStaticKeyResolver(map[string]ed25519.PublicKey{"k1": pub}),
		Revocations:    newMemStore(),
		TrustedIssuers: []string{goldenIssuer},
	})
	if err != nil {
		t.Fatal(err)
	}
	if v.cfg.Leeway != 30*time.Second {
		t.Errorf("verifier leeway = %s, want 30s", v.cfg.Leeway)
	}

	r, err := NewRemoteJWKSResolver(RemoteJWKSConfig{URL: "https://issuer.example.com/jwks"})
	if err != nil {
		t.Fatal(err)
	}
	if r.cfg.Client.Timeout != 10*time.Second || r.cfg.RefreshInterval != 5*time.Minute ||
		r.cfg.MinRefreshInterval != 10*time.Second || r.cfg.MaxStale != time.Hour || r.cfg.MaxDocumentBytes != 1<<20 {
		t.Errorf("remote JWKS defaults = timeout %s, refresh %s, min refresh %s, max stale %s, max bytes %d",
			r.cfg.Client.Timeout, r.cfg.RefreshInterval, r.cfg.MinRefreshInterval, r.cfg.MaxStale, r.cfg.MaxDocumentBytes)
	}

	if c := NewMemoryReplayCache(0); c.maxEntries != 100_000 {
		t.Errorf("replay cache bound = %d, want 100 000", c.maxEntries)
	}
}
