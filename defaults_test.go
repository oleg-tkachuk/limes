package limes

import (
	"context"
	"crypto/ed25519"
	"errors"
	"slices"
	"testing"
	"time"
	"uuid"
)

// RFC 8037 appendix A: the Ed25519 public key and its RFC 7638 thumbprint.

// A zero config field takes the default its documentation names.
func TestConfigDefaults(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := NewSigner(SignerConfig{KeyID: "k1", Key: SealKey(priv)})
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
	if !slices.Equal(v.cfg.AcceptTokenTypes, []TokenType{TokenTypeV6}) {
		t.Errorf("verifier accepts %q, want only %q", v.cfg.AcceptTokenTypes, TokenTypeV6)
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
	signer, err := NewSigner(SignerConfig{KeyID: "k1", Key: SealKey(priv)})
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
		"a Leeway beside NoLeeway": func(c *VerifierConfig) { c.Leeway, c.NoLeeway = time.Second, true },
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

// NoLeeway holds the window to the verifier's clock: a token a second past
// its expiry is refused, where the default leeway would take it.
func TestNoLeewayIsExact(t *testing.T) {
	issuer, configured, _, _ := buildIssuerVerifier(t)
	issued, token := issueFor(t, issuer)
	late := func() time.Time { return issued.ExpiresAt.Add(time.Second) }
	for _, tc := range []struct {
		noLeeway bool
		want     error
	}{{false, nil}, {true, ErrExpired}} {
		cfg := configured.cfg
		cfg.Leeway, cfg.NoLeeway, cfg.Now = 0, tc.noLeeway, late
		v, err := NewVerifier(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := v.Verify(t.Context(), VerifyRequest{Token: token, Audience: audienceData}); !errors.Is(err, tc.want) || (tc.want == nil && err != nil) {
			t.Errorf("NoLeeway %v, a second past expiry: err = %v, want %v", tc.noLeeway, err, tc.want)
		}
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
	list := ListByPrincipalRequest{TenantID: uuid.New(), PrincipalType: PrincipalTypeAgent, Subject: "a", Limit: -1}
	if err := list.Validate(); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("ListByPrincipalRequest with Limit -1: Validate = %v, want ErrInvalidRequest", err)
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
	signer, err := NewSigner(SignerConfig{KeyID: "k1", Key: SealKey(priv)})
	if err != nil {
		t.Fatal(err)
	}
	store := &issuanceOnly{}
	issuer, err := NewIssuer(IssuerConfig{Signer: signer, Store: store, IssuerName: goldenIssuer})
	if err != nil {
		t.Fatal(err)
	}
	c, _, err := issuer.issue(context.Background(), IssueRequest{
		IssuedBy: Principal{Subject: "op"}, Subject: Principal{Type: PrincipalTypeAgent, TenantID: uuid.New(), Subject: "a"},
		Audience: []string{"data"}, Caveats: Caveats{Ops: []Op{OpGet}}, TTL: time.Minute,
	})
	if err != nil || len(store.inserted) != 1 || store.inserted[0].Capability.ID != c.ID || store.inserted[0].IssuedBy.Subject != "op" {
		t.Fatalf("Issue = %v; recorded %+v", err, store.inserted)
	}
}

// AcceptTokenTypes names only formats this package can read; an empty list,
// as a nil one, accepts every format it reads.
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
	every := []TokenType{TokenTypeV6}
	for _, tc := range []struct{ types, want []TokenType }{
		{[]TokenType{TokenTypeV6}, []TokenType{TokenTypeV6}},
		{nil, every},
		{[]TokenType{}, every},
	} {
		v, err := NewVerifier(cfg(tc.types))
		if err != nil || !slices.Equal(v.cfg.AcceptTokenTypes, tc.want) {
			t.Errorf("accepting %q: %v", tc.types, err)
		}
	}
	for name, types := range map[string][]TokenType{
		"an unknown format": {"limes-cap-v7+jwt"},
		"the retired v4":    {"limes-cap-v4+jwt"},
		"the retired v5":    {"limes-cap-v5+jwt"},
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

// What Issue records is what the token carries: whole seconds, the expiry
// rounded down and the start rounded up, so no record says a token is valid
// at an instant the token itself is not.
func TestIssuedTimesAreTheTokens(t *testing.T) {
	issuer, configured, _, _ := buildIssuerVerifier(t)
	at := time.Date(2026, time.October, 12, 9, 30, 0, 700_000_000, time.UTC) // x.7s
	issuer.clock = func() time.Time { return at }
	nbf := at.Add(10 * time.Second) // also x.7s: rounds up
	issued, err := issuer.Issue(t.Context(), IssueRequest{
		IssuedBy: Principal{Subject: "op"}, Subject: Principal{Type: PrincipalTypeAgent, TenantID: uuid.New(), Subject: "a"},
		Audience: []string{audienceData}, Caveats: Caveats{Ops: []Op{OpGet}}, TTL: time.Hour, NotBefore: nbf,
	})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeToken(issued.Token)
	if err != nil {
		t.Fatal(err)
	}
	inToken := decoded.(Capability)
	got := issued.Capability
	for name, pair := range map[string][2]time.Time{
		"IssuedAt": {got.IssuedAt, inToken.IssuedAt}, "NotBefore": {got.NotBefore, inToken.NotBefore}, "ExpiresAt": {got.ExpiresAt, inToken.ExpiresAt},
	} {
		if !pair[0].Equal(pair[1]) || pair[0].Nanosecond() != 0 {
			t.Errorf("%s recorded %s, token carries %s; want the same whole second", name, pair[0], pair[1])
		}
	}
	if got.NotBefore.Before(nbf) {
		t.Errorf("NotBefore %s is before the %s asked for", got.NotBefore, nbf)
	}
	if got.ExpiresAt.After(at.Add(time.Hour)) {
		t.Errorf("ExpiresAt %s is past the TTL's end %s", got.ExpiresAt, at.Add(time.Hour))
	}
	// With no leeway the window is exact: valid at NotBefore, refused at
	// ExpiresAt (RFC 7519 §4.1.4).
	cfg := configured.cfg
	cfg.Leeway, cfg.NoLeeway = 0, true
	for name, tc := range map[string]struct {
		now  time.Time
		want error
	}{
		"just before NotBefore": {got.NotBefore.Add(-time.Nanosecond), ErrNotYetValid},
		"at NotBefore":          {got.NotBefore, nil},
		"just before ExpiresAt": {got.ExpiresAt.Add(-time.Nanosecond), nil},
		"at ExpiresAt":          {got.ExpiresAt, ErrExpired},
	} {
		cfg.Now = func() time.Time { return tc.now }
		v, err := NewVerifier(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := v.Verify(t.Context(), VerifyRequest{Token: issued.Token, Audience: audienceData}); tc.want == nil && err != nil || tc.want != nil && !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", name, err, tc.want)
		}
	}
}

// A capability that would expire within the second it is issued is refused,
// never minted to live on leeway alone.
func TestIssueRefusesALifetimeUnderASecond(t *testing.T) {
	issuer, _, store, _ := buildIssuerVerifier(t)
	at := time.Date(2026, time.October, 12, 9, 30, 0, 400_000_000, time.UTC) // x.4s
	issuer.clock = func() time.Time { return at }
	issue := func(ttl time.Duration) error {
		_, err := issuer.Issue(t.Context(), IssueRequest{
			IssuedBy: Principal{Subject: "op"}, Subject: Principal{Type: PrincipalTypeAgent, TenantID: uuid.New(), Subject: "a"},
			Audience: []string{audienceData}, Caveats: Caveats{Ops: []Op{OpGet}}, TTL: ttl,
		})
		return err
	}
	const withinTheSecond, toTheNext = 500 * time.Millisecond, 600 * time.Millisecond
	before := len(store.caps)
	if err := issue(withinTheSecond); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("a TTL ending within the second: err = %v, want ErrInvalidRequest", err)
	}
	if len(store.caps) != before {
		t.Error("a refused capability was recorded")
	}
	if err := issue(toTheNext); err != nil {
		t.Errorf("a TTL reaching the next second: %v", err)
	}
}
