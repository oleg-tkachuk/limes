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
			Audience: []string{"data"},
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
		Audience: []string{"data"},
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
