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
		"neg requests":         {Ops: []Op{OpGet}, Requests: RequestCeiling{Max: -1}},
		"neg budget":           {Ops: []Op{OpGet}, Budget: BudgetCeiling{Max: -5}},
		"budget past MaxNanos": {Ops: []Op{OpGet}, Budget: BudgetCeiling{Max: MaxNanos + 1}},
		"bad unit":             {Ops: []Op{OpGet}, UnitCode: "XYZ"},
		"bad cidr":             {Ops: []Op{OpGet}, SourceIPCIDR: []string{"10.0.0.0/33"}},
		"IPv4-mapped cidr":     {Ops: []Op{OpGet}, SourceIPCIDR: []string{"::ffff:10.0.0.0/104"}},
		"control in uri":       {Ops: []Op{OpGet}, ResourceURIs: []string{"a\x00b"}},
		"dot-dot in prefix":    {Ops: []Op{OpGet}, ResourcePrefixes: []string{"a/../b"}},
		"dot in uri":           {Ops: []Op{OpGet}, ResourceURIs: []string{"a/./b"}},
		"encoded dot-dot":      {Ops: []Op{OpGet}, ResourcePrefixes: []string{"a/%2E%2e/b"}},
		"dot-dot before %2F":   {Ops: []Op{OpGet}, ResourcePrefixes: []string{"corpus/..%2F"}},
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
			Subject:  Principal{Type: PrincipalAgent, TenantID: uuid.New(), Subject: "a"},
			Audience: []string{"data"},
			Caveats:  Caveats{Ops: []Op{OpGet}},
			TTL:      time.Minute,
		}
	}
	mutations := map[string]func(*IssueRequest){
		"negative ttl":       func(r *IssueRequest) { r.TTL = -time.Second },
		"nbf after expiry":   func(r *IssueRequest) { r.NotBefore = time.Now().Add(time.Hour) },
		"empty audience":     func(r *IssueRequest) { r.Audience = []string{""} },
		"negative budget":    func(r *IssueRequest) { r.Caveats.Budget = BudgetCeiling{Max: -1} },
		"negative gen":       func(r *IssueRequest) { r.Generation = -1 },
		"missing issued-by":  func(r *IssueRequest) { r.IssuedBy = Principal{} },
		"empty prefix entry": func(r *IssueRequest) { r.Caveats.ResourcePrefixes = []string{""} },
	}
	for name, mutate := range mutations {
		req := base()
		mutate(&req)
		if _, _, err := issuer.issue(ctx, req); err == nil {
			t.Errorf("%s: Issue succeeded, want an error", name)
		}
	}
}

func issueRoot(t *testing.T, issuer *Issuer, caveats Caveats) Capability {
	t.Helper()
	c, _, err := issuer.issue(context.Background(), IssueRequest{
		IssuedBy: Principal{Subject: "op"},
		Subject:  Principal{Type: PrincipalAgent, TenantID: uuid.New(), Subject: "root"},
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
	parent := issueRoot(t, issuer, Caveats{Ops: []Op{OpGet, OpShare}, Budget: BudgetCeiling{Max: MustParseAmount("25")}, UnitCode: "USD"})

	if _, _, err := issuer.delegate(ctx, DelegateRequest{
		Subject: Principal{Type: PrincipalAgent, Subject: "child"},
		Parent:  asVerified(parent), Caveats: Caveats{Budget: BudgetCeiling{Max: MustParseAmount("2")}},
	}); !errors.Is(err, ErrInvalidCaveats) {
		t.Fatalf("delegate with ops omitted = %v, want ErrInvalidCaveats", err)
	}

	child, _, err := issuer.delegate(ctx, DelegateRequest{Subject: Principal{Type: PrincipalAgent, Subject: "child"}, Parent: asVerified(parent), InheritCaveats: true})
	if err != nil {
		t.Fatalf("delegate with InheritCaveats: %v", err)
	}
	if child.Caveats.Budget != (BudgetCeiling{Max: 25 * NanosPerUnit}) {
		t.Errorf("inherited budget = %v, want 25", child.Caveats.Budget)
	}
}

// foreignIssuer names an issuer other than the one under test.
const foreignIssuer = "someone-else"

// A parent another issuer named is refused: this issuer's store has no record
// of it, so revoking it at its own issuer would never stop the child.
func TestDelegateRefusesRevokedOrExpiredParent(t *testing.T) {
	issuer, _, store, _ := buildIssuerVerifier(t)
	ctx := context.Background()
	parent := issueRoot(t, issuer, Caveats{Ops: []Op{OpGet, OpShare}})

	expired := parent
	expired.ExpiresAt = time.Now().Add(-time.Minute)
	if _, _, err := issuer.delegate(ctx, DelegateRequest{Subject: Principal{Type: PrincipalAgent, Subject: "child"}, Parent: asVerified(expired), InheritCaveats: true}); !errors.Is(err, ErrExpired) {
		t.Errorf("delegate from expired parent = %v, want ErrExpired", err)
	}

	foreign := parent
	foreign.Issuer = foreignIssuer
	if _, _, err := issuer.delegate(ctx, DelegateRequest{Subject: Principal{Type: PrincipalAgent, Subject: "child"}, Parent: asVerified(foreign), InheritCaveats: true}); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("delegate from another issuer's parent = %v, want ErrInvalidRequest", err)
	}

	_ = store.Revoke(ctx, RevokeRequest{CapabilityID: parent.ID})
	if _, _, err := issuer.delegate(ctx, DelegateRequest{Subject: Principal{Type: PrincipalAgent, Subject: "child"}, Parent: asVerified(parent), InheritCaveats: true}); !errors.Is(err, ErrRevoked) {
		t.Errorf("delegate from revoked parent = %v, want ErrRevoked", err)
	}
}

// A capability's id is a UUIDv7, as Capability.ID promises: sortable by
// issuance, so a store's index on it stays tight. Delegated ones too.
func TestIssuedIDsAreVersion7(t *testing.T) {
	issuer, _, _, _ := buildIssuerVerifier(t)
	ctx := context.Background()
	parent, _, err := issuer.issue(ctx, IssueRequest{
		IssuedBy: Principal{Subject: "op"}, Subject: Principal{Type: PrincipalAgent, TenantID: uuid.New(), Subject: "a"},
		Audience: []string{audienceData}, Caveats: Caveats{Ops: []Op{OpGet, OpShare}}, TTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	child, _, err := issuer.delegate(ctx, DelegateRequest{Subject: Principal{Type: PrincipalAgent, Subject: "child"}, Parent: asVerified(parent), InheritCaveats: true})
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

// Issue, Delegate and Biscuit hand back what their unexported forms mint, in
// result structs that may grow: the capability as recorded and its token.
func TestMintingResults(t *testing.T) {
	issuer, v, _, _ := buildIssuerVerifier(t)
	ctx := context.Background()
	issued, err := issuer.Issue(ctx, IssueRequest{
		IssuedBy: Principal{Subject: "op"}, Subject: Principal{Type: PrincipalAgent, TenantID: uuid.New(), Subject: "a"},
		Audience: []string{audienceData}, Caveats: Caveats{Ops: []Op{OpGet, OpShare}}, TTL: time.Hour,
	})
	if err != nil || issued.Token == "" || issued.Capability.ID == uuid.Nil {
		t.Fatalf("Issue = %+v, %v; want a capability and its token", issued, err)
	}
	vc, err := v.Verify(ctx, VerifyRequest{Token: issued.Token, Audience: audienceData})
	if err != nil || vc.Capability().ID != issued.Capability.ID {
		t.Fatalf("the issued token verifies as %s, %v; want %s", vc.Capability().ID, err, issued.Capability.ID)
	}
	delegated, err := issuer.Delegate(ctx, DelegateRequest{Parent: vc, Subject: Principal{Type: PrincipalAgent, Subject: "child"}, InheritCaveats: true})
	if err != nil || delegated.Token == "" || delegated.Capability.ParentID != issued.Capability.ID {
		t.Fatalf("Delegate = %+v, %v; want a child of %s", delegated, err, issued.Capability.ID)
	}
	if b, err := issuer.Biscuit(ctx, BiscuitRequest{Capability: vc}); err != nil || b.Token == "" {
		t.Fatalf("Biscuit = %+v, %v; want a token", b, err)
	}
	if r, err := issuer.Issue(ctx, IssueRequest{}); err == nil || r.Token != "" {
		t.Errorf("a refused Issue = %+v, %v; want the zero result and an error", r, err)
	}
	if b, err := issuer.Biscuit(ctx, BiscuitRequest{}); err == nil || b.Token != "" {
		t.Errorf("a refused Biscuit = %+v, %v; want the zero result and an error", b, err)
	}
}

// Delegating is the share operation: a parent granted without OpShare hands
// on nothing, and nothing is recorded for the refused child.
func TestDelegateRequiresOpShare(t *testing.T) {
	issuer, _, store, _ := buildIssuerVerifier(t)
	ctx := context.Background()
	for name, tc := range map[string]struct {
		ops  []Op
		want error
	}{
		"a parent without share": {[]Op{OpGet}, ErrOpNotAllowed},
		"a parent with share":    {[]Op{OpGet, OpShare}, nil},
	} {
		parent := issueRoot(t, issuer, Caveats{Ops: tc.ops})
		before := len(store.caps)
		_, err := issuer.Delegate(ctx, DelegateRequest{Parent: asVerified(parent), Subject: Principal{Type: PrincipalAgent, Subject: "child"}, Caveats: Caveats{Ops: []Op{OpGet}}})
		if tc.want == nil && err != nil || tc.want != nil && !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", name, err, tc.want)
		}
		if tc.want != nil && len(store.caps) != before {
			t.Errorf("%s: a refused delegation was recorded", name)
		}
	}
}
