package limes

import (
	"crypto/ed25519"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes/jwks"
)

// RFC 8037 appendix A: the Ed25519 public key and its RFC 7638 thumbprint.

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

// A key binding with no kid in it refuses every token; it is an error, not
// a configuration to run with.
func TestVerifierRefusesAnEmptyKeyIssuerBinding(t *testing.T) {
	_, configured, _, _ := buildIssuerVerifier(t)
	cfg := configured.cfg
	cfg.KeyIssuers = map[string]string{}
	if _, err := NewStandardVerifier(cfg); err == nil {
		t.Error("NewStandardVerifier accepted an empty KeyIssuers")
	}
}

// A negative page size is the request's error, not the default page.
func TestListRequestsRefuseANegativeLimit(t *testing.T) {
	list := ListByPrincipalRequest{TenantID: uuid.New(), PrincipalType: PrincipalAgent, Subject: "a", Limit: -1}
	if err := list.Validate(); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("ListByPrincipalRequest with Limit -1: Validate = %v, want ErrInvalidRequest", err)
	}
	budgets := ListTenantBudgetsRequest{Limit: -1}
	if err := budgets.Validate(); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("ListTenantBudgetsRequest with Limit -1: Validate = %v, want ErrInvalidRequest", err)
	}
	if err := (ListTenantBudgetsRequest{ThresholdPct: maxThresholdPct + 1}).Validate(); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("a threshold past 100%%: Validate = %v, want ErrInvalidRequest", err)
	}
}
