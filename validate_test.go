package limes

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

// ─── validation ────────────────────────────────────────────────────────────

func TestCaveatsValidate(t *testing.T) {
	ok := Caveats{Ops: []Op{OpGet, "tool:search"}, ResourcePrefixes: []string{"a/"}, UnitCode: "EUR", SourceIPCIDR: []string{"10.0.0.0/8"}}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid caveats rejected: %v", err)
	}
	bad := map[string]Caveats{
		"no ops":               {},
		"unknown op":           {Ops: []Op{"teleport"}},
		"op with space":        {Ops: []Op{"tool:do it"}},
		"empty prefix":         {Ops: []Op{OpGet}, ResourcePrefixes: []string{""}},
		"empty uri":            {Ops: []Op{OpGet}, ResourceURIs: []string{""}},
		"neg requests":         {Ops: []Op{OpGet}, MaxRequests: -1},
		"neg budget":           {Ops: []Op{OpGet}, MaxBudgetAmount: -5},
		"budget past MaxNanos": {Ops: []Op{OpGet}, MaxBudgetAmount: MaxNanos + 1},
		"bad unit":             {Ops: []Op{OpGet}, UnitCode: "XYZ"},
		"bad cidr":             {Ops: []Op{OpGet}, SourceIPCIDR: []string{"10.0.0.0/33"}},
		"control in uri":       {Ops: []Op{OpGet}, ResourceURIs: []string{"a\x00b"}},
		"dot-dot in prefix":    {Ops: []Op{OpGet}, ResourcePrefixes: []string{"a/../b"}},
		"dot in uri":           {Ops: []Op{OpGet}, ResourceURIs: []string{"a/./b"}},
		"encoded dot-dot":      {Ops: []Op{OpGet}, ResourcePrefixes: []string{"a/%2E%2e/b"}},
		"leading dot-dot":      {Ops: []Op{OpGet}, ResourceURIs: []string{"../b"}},
	}
	for name, c := range bad {
		if err := c.Validate(); !errors.Is(err, ErrInvalidCaveats) {
			t.Errorf("%s: Validate = %v, want ErrInvalidCaveats", name, err)
		}
	}
}

func TestIssueRejectsMalformedRequests(t *testing.T) {
	issuer, _, _, _ := buildIssuerVerifier(t)
	ctx := context.Background()
	base := func() IssueRequest {
		return IssueRequest{
			IssuedBy: Principal{Subject: "op"},
			Subject:  Principal{TenantID: uuid.New(), Subject: "a"},
			Audience: []string{audienceData},
			Caveats:  Caveats{Ops: []Op{OpGet}},
			TTL:      time.Minute,
		}
	}
	mutations := map[string]func(*IssueRequest){
		"negative ttl":       func(r *IssueRequest) { r.TTL = -time.Second },
		"nbf after expiry":   func(r *IssueRequest) { r.NotBefore = time.Now().Add(time.Hour) },
		"empty audience":     func(r *IssueRequest) { r.Audience = []string{""} },
		"negative budget":    func(r *IssueRequest) { r.Caveats.MaxBudgetAmount = -1 },
		"negative gen":       func(r *IssueRequest) { r.Generation = -1 },
		"missing issued-by":  func(r *IssueRequest) { r.IssuedBy = Principal{} },
		"empty prefix entry": func(r *IssueRequest) { r.Caveats.ResourcePrefixes = []string{""} },
	}
	for name, mutate := range mutations {
		req := base()
		mutate(&req)
		if _, _, err := issuer.Issue(ctx, req); err == nil {
			t.Errorf("%s: Issue succeeded, want an error", name)
		}
	}
}

func issueRoot(t *testing.T, issuer *Issuer, caveats Caveats) Capability {
	t.Helper()
	c, _, err := issuer.Issue(context.Background(), IssueRequest{
		IssuedBy: Principal{Subject: "op"},
		Subject:  Principal{TenantID: uuid.New(), Subject: "root"},
		Audience: []string{audienceData},
		Caveats:  caveats,
		TTL:      time.Hour,
	})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	return c
}

// Empty child caveats used to inherit silently, overwriting whatever else
// the caller had set. Inheritance is now asked for, or refused.
func TestDelegateInheritanceIsExplicit(t *testing.T) {
	issuer, _, _, _ := buildIssuerVerifier(t)
	ctx := context.Background()
	parent := issueRoot(t, issuer, Caveats{Ops: []Op{OpGet}, MaxBudgetAmount: MustParseAmount("25"), UnitCode: "USD"})

	if _, _, err := issuer.Delegate(ctx, DelegateRequest{
		Parent: parent, Caveats: Caveats{MaxBudgetAmount: MustParseAmount("2")},
	}); !errors.Is(err, ErrInvalidCaveats) {
		t.Fatalf("delegate with ops omitted = %v, want ErrInvalidCaveats", err)
	}

	child, _, err := issuer.Delegate(ctx, DelegateRequest{Parent: parent, InheritCaveats: true})
	if err != nil {
		t.Fatalf("delegate with InheritCaveats: %v", err)
	}
	if child.Caveats.MaxBudgetAmount != 25*NanosPerUnit {
		t.Errorf("inherited budget = %v, want 25", child.Caveats.MaxBudgetAmount)
	}
}

func TestDelegateRefusesRevokedOrExpiredParent(t *testing.T) {
	issuer, _, store, _ := buildIssuerVerifier(t)
	ctx := context.Background()
	parent := issueRoot(t, issuer, Caveats{Ops: []Op{OpGet}})

	expired := parent
	expired.ExpiresAt = time.Now().Add(-time.Minute)
	if _, _, err := issuer.Delegate(ctx, DelegateRequest{Parent: expired, InheritCaveats: true}); !errors.Is(err, ErrExpired) {
		t.Errorf("delegate from expired parent = %v, want ErrExpired", err)
	}

	_ = store.Revoke(ctx, RevokeRequest{ID: parent.ID})
	if _, _, err := issuer.Delegate(ctx, DelegateRequest{Parent: parent, InheritCaveats: true}); !errors.Is(err, ErrRevoked) {
		t.Errorf("delegate from revoked parent = %v, want ErrRevoked", err)
	}
}

// A capability's id is a UUIDv7, as Capability.ID promises: sortable by
// issuance, so a store's index on it stays tight. Delegated ones too.
func TestIssuedIDsAreVersion7(t *testing.T) {
	issuer, _, _, _ := buildIssuerVerifier(t)
	ctx := context.Background()
	parent, _, err := issuer.Issue(ctx, IssueRequest{
		IssuedBy: Principal{Subject: "op"}, Subject: Principal{TenantID: uuid.New(), Subject: "a"},
		Audience: []string{audienceData}, Caveats: Caveats{Ops: []Op{OpGet}}, TTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	child, _, err := issuer.Delegate(ctx, DelegateRequest{Parent: parent, InheritCaveats: true})
	if err != nil {
		t.Fatal(err)
	}
	const v7 uuid.Version = 7
	for name, id := range map[string]uuid.UUID{"issued": parent.ID, "delegated": child.ID} {
		if v := id.Version(); v != v7 {
			t.Errorf("%s id %s is version %d, want %d", name, id, v, v7)
		}
	}
}

// RFC 8707 §2: an audience is a resource indicator, an absolute URI without
// a fragment.
func TestValidateResourceIndicator(t *testing.T) {
	for _, ok := range []string{"https://data.example.com", "https://api.example.com/v1/objects", "urn:example:resource:x", "http://localhost:8080"} {
		if err := ValidateResourceIndicator(ok); err != nil {
			t.Errorf("ValidateResourceIndicator(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"", "data", "/relative", "https://data.example.com/#frag", "https://data.example.com#", "//data.example.com", "https:"} {
		if err := ValidateResourceIndicator(bad); !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("ValidateResourceIndicator(%q) = %v, want ErrInvalidRequest", bad, err)
		}
	}
}
