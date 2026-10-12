package limes

import (
	"context"
	"crypto/ed25519"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
)

// RFC 8037 appendix A: the Ed25519 public key and its RFC 7638 thumbprint.

// A zero config field takes the default its documentation names.
func TestConfigDefaults(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := NewSigner(SignerConfig{KeyID: "k1", Key: priv})
	if err != nil {
		t.Fatal(err)
	}
	iss, err := NewIssuer(IssuerConfig{Signer: signer, Store: newMemStore(), IssuerName: goldenIssuer})
	if err != nil {
		t.Fatal(err)
	}
	if iss.maxTokenBytes != DefaultMaxTokenBytes {
		t.Errorf("Issuer MaxTokenBytes = %d, want %d", iss.maxTokenBytes, DefaultMaxTokenBytes)
	}
	if iss.defaultTTL != 15*time.Minute {
		t.Errorf("Issuer default TTL = %s, want 15m", iss.defaultTTL)
	}

	v, err := NewVerifier(VerifierConfig{
		Keys:           staticKeys(t, map[string]ed25519.PublicKey{"k1": pub}),
		Revocations:    newMemStore(),
		TrustedIssuers: []string{goldenIssuer},
	})
	if err != nil {
		t.Fatal(err)
	}
	if v.cfg.Leeway != 30*time.Second {
		t.Errorf("verifier leeway = %s, want 30s", v.cfg.Leeway)
	}
	if !slices.Equal(v.cfg.AcceptTokenTypes, []TokenType{TokenTypeV4}) {
		t.Errorf("verifier accepts %q, want only %q", v.cfg.AcceptTokenTypes, TokenTypeV4)
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
	signer, err := NewSigner(SignerConfig{KeyID: "k1", Key: priv})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewIssuer(IssuerConfig{Signer: signer, Store: newMemStore(), IssuerName: goldenIssuer, DefaultTTL: -time.Minute}); err == nil {
		t.Error("NewIssuer accepted a negative DefaultTTL")
	}
	if _, err := NewIssuer(IssuerConfig{Signer: signer, Store: newMemStore(), IssuerName: goldenIssuer, MaxTokenBytes: -1}); err == nil {
		t.Error("NewIssuer accepted a negative MaxTokenBytes")
	}

	base := VerifierConfig{
		Keys:           staticKeys(t, map[string]ed25519.PublicKey{"k1": pub}),
		Revocations:    newMemStore(),
		TrustedIssuers: []string{goldenIssuer},
	}
	for name, mutate := range map[string]func(*VerifierConfig){
		"a negative Leeway":        func(c *VerifierConfig) { c.Leeway = -time.Second },
		"a Leeway past MaxLeeway":  func(c *VerifierConfig) { c.Leeway = MaxLeeway + time.Second },
		"a negative MaxTokenBytes": func(c *VerifierConfig) { c.MaxTokenBytes = -1 },
		"a Realm with a quote":     func(c *VerifierConfig) { c.Realm = `a"b` },
		"a Realm with a newline":   func(c *VerifierConfig) { c.Realm = "a\nb" },
	} {
		cfg := base
		mutate(&cfg)
		if _, err := NewVerifier(cfg); err == nil {
			t.Errorf("NewVerifier accepted %s", name)
		}
	}
	at := base
	at.Leeway = MaxLeeway
	if _, err := NewVerifier(at); err != nil {
		t.Errorf("NewVerifier refused a Leeway of MaxLeeway: %v", err)
	}
}

// An empty KeyIssuers is no binding, as a nil one is: enough for one issuer,
// not for several.
func TestAnEmptyKeyIssuerBindingBindsNothing(t *testing.T) {
	_, configured, _, _ := buildIssuerVerifier(t)
	cfg := configured.cfg
	cfg.KeyIssuers = map[string]string{}
	v, err := NewVerifier(cfg)
	if err != nil || v.cfg.KeyIssuers != nil {
		t.Errorf("one issuer, empty KeyIssuers = %v, binding %v; want accepted, binding nothing", err, v.cfg.KeyIssuers)
	}
	cfg.TrustedIssuers = append(cfg.TrustedIssuers, "another")
	if _, err := NewVerifier(cfg); err == nil {
		t.Error("several issuers with an empty KeyIssuers were accepted, as if bound")
	}
	cfg.SharedKeys = true
	if _, err := NewVerifier(cfg); err != nil {
		t.Errorf("an empty KeyIssuers beside SharedKeys: %v", err)
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
	if err := (ListTenantBudgetsRequest{Filter: TenantBudgetsAtOrPast, ThresholdPct: maxThresholdPct + 1}).Validate(); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("a threshold past 100%%: Validate = %v, want ErrInvalidRequest", err)
	}
}

// issuanceOnly is a store with no more than an Issuer needs.
type issuanceOnly struct{ inserted []InsertRequest }

func (s *issuanceOnly) Insert(_ context.Context, req InsertRequest) error {
	s.inserted = append(s.inserted, req)
	return nil
}
func (s *issuanceOnly) IsRevoked(context.Context, uuid.UUID) (bool, error) { return false, nil }

// An Issuer runs on IssuanceStore alone: the admin half of Store is not
// its business.
func TestIssuerNeedsOnlyAnIssuanceStore(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := NewSigner(SignerConfig{KeyID: "k1", Key: priv})
	if err != nil {
		t.Fatal(err)
	}
	store := &issuanceOnly{}
	issuer, err := NewIssuer(IssuerConfig{Signer: signer, Store: store, IssuerName: goldenIssuer})
	if err != nil {
		t.Fatal(err)
	}
	c, _, err := issuer.issue(context.Background(), IssueRequest{
		IssuedBy: Principal{Subject: "op"}, Subject: Principal{Type: PrincipalAgent, TenantID: uuid.New(), Subject: "a"},
		Audience: []string{"data"}, Caveats: Caveats{Ops: []Op{OpGet}}, TTL: time.Minute,
	})
	if err != nil || len(store.inserted) != 1 || store.inserted[0].Capability.ID != c.ID || store.inserted[0].IssuedBy.Subject != "op" {
		t.Fatalf("Issue = %v; recorded %+v", err, store.inserted)
	}
}

// AcceptTokenTypes names only formats this package can read; an empty list,
// as a nil one, accepts the current format.
func TestAcceptTokenTypesRefusesWhatItCannotRead(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg := func(types []TokenType) VerifierConfig {
		return VerifierConfig{
			Keys:             staticKeys(t, map[string]ed25519.PublicKey{"k1": pub}),
			Revocations:      newMemStore(),
			TrustedIssuers:   []string{goldenIssuer},
			AcceptTokenTypes: types,
		}
	}
	for _, types := range [][]TokenType{{TokenTypeV4}, nil, {}} {
		v, err := NewVerifier(cfg(types))
		if err != nil || !slices.Equal(v.cfg.AcceptTokenTypes, []TokenType{TokenTypeV4}) {
			t.Errorf("accepting %q: %v", types, err)
		}
	}
	for name, types := range map[string][]TokenType{
		"an unknown format": {"limes-cap-v5+jwt"},
		"an empty format":   {""},
	} {
		if _, err := NewVerifier(cfg(types)); err == nil {
			t.Errorf("%s: a verifier was built", name)
		}
	}
}

// Several trusted issuers need a decision about keys: a binding, or the
// explicit SharedKeys. Neither is never the default, and both contradict.
func TestSeveralIssuersNeedAKeyDecision(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	base := VerifierConfig{Keys: staticKeys(t, map[string]ed25519.PublicKey{"k1": pub}), Revocations: newMemStore()}
	for name, tc := range map[string]struct {
		issuers []string
		binding map[string]string
		shared  bool
		ok      bool
	}{
		"one issuer, no binding":             {[]string{"a"}, nil, false, true},
		"several issuers, no decision":       {[]string{"a", "b"}, nil, false, false},
		"several issuers, a binding":         {[]string{"a", "b"}, map[string]string{"k1": "a"}, false, true},
		"several issuers, shared keys":       {[]string{"a", "b"}, nil, true, true},
		"a binding and shared keys together": {[]string{"a", "b"}, map[string]string{"k1": "a"}, true, false},
	} {
		cfg := base
		cfg.TrustedIssuers, cfg.KeyIssuers, cfg.SharedKeys = tc.issuers, tc.binding, tc.shared
		if _, err := NewVerifier(cfg); (err == nil) != tc.ok {
			t.Errorf("%s: err = %v, want ok=%v", name, err, tc.ok)
		}
	}
}

// A tenant budget filter names itself, and one this package lacks is refused.
func TestTenantBudgetFilter(t *testing.T) {
	for f, want := range map[TenantBudgetFilter]string{TenantBudgetsAll: "all", TenantBudgetsAtOrPast: "at-or-past", TenantBudgetsUnlimited: "unlimited"} {
		if f.String() != want || f.Validate() != nil {
			t.Errorf("%d: String = %q, Validate = %v; want %q, nil", f, f.String(), f.Validate(), want)
		}
	}
	if err := (TenantBudgetsUnlimited + 1).Validate(); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("an unknown filter: err = %v, want ErrInvalidRequest", err)
	}
}
