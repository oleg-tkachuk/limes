package limes

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"uuid"
)

// ─── validation ────────────────────────────────────────────────────────────

func TestCaveatsValidate(t *testing.T) {
	ok := Caveats{Requests: RequestCeiling{Unlimited: true}, Ops: []Op{OpGet, "tool:search"}, ResourcePrefixes: []string{"a/"}, Budget: BudgetCeiling{UnitCode: "EUR"}, SourceIPCIDR: []string{"10.0.0.0/8"}}
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
		"bad unit":             {Ops: []Op{OpGet}, Budget: BudgetCeiling{UnitCode: "XYZ"}},
		"bad cidr":             {Ops: []Op{OpGet}, SourceIPCIDR: []string{"10.0.0.0/33"}},
		"IPv4-mapped cidr":     {Ops: []Op{OpGet}, SourceIPCIDR: []string{"::ffff:10.0.0.0/104"}},
		"control in uri":       {Ops: []Op{OpGet}, ResourceURIs: []string{"a\x00b"}},
		"dot-dot in prefix":    {Ops: []Op{OpGet}, ResourcePrefixes: []string{"a/../b"}},
		"dot in uri":           {Ops: []Op{OpGet}, ResourceURIs: []string{"a/./b"}},
		"encoded dot-dot":      {Ops: []Op{OpGet}, ResourcePrefixes: []string{"a/%2E%2e/b"}},
		"dot-dot before %2F":   {Ops: []Op{OpGet}, ResourcePrefixes: []string{"corpus/..%2F"}},
		"double-encoded dots":  {Ops: []Op{OpGet}, ResourcePrefixes: []string{"a/%252e%252e/b"}},
		"dots, hex encoded":    {Ops: []Op{OpGet}, ResourcePrefixes: []string{"a/%25%32%65%25%32%65/b"}},
		"dot-dot with params":  {Ops: []Op{OpGet}, ResourceURIs: []string{"a/..;x/b"}},
		"slash, hex encoded":   {Ops: []Op{OpGet}, ResourceURIs: []string{"a/..%25%32%66b"}},
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
		return IssueRequest{ClientID: testClientID,
			IssuedBy: Principal{Subject: "op"},
			Subject:  Principal{Type: PrincipalTypeAgent, TenantID: uuid.New(), Subject: "a"},
			Audience: []string{"data"},
			Caveats:  Caveats{Requests: RequestCeiling{Unlimited: true}, Ops: []Op{OpGet}},
			TTL:      time.Minute,
		}
	}
	mutations := map[string]func(*IssueRequest){
		"negative ttl":       func(r *IssueRequest) { r.TTL = -time.Second },
		"nbf after expiry":   func(r *IssueRequest) { r.NotBefore = time.Now().Add(time.Hour) },
		"empty audience":     func(r *IssueRequest) { r.Audience = []string{""} },
		"negative budget":    func(r *IssueRequest) { r.Caveats.Budget = BudgetCeiling{Max: -1} },
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
	c, _, err := issuer.issue(context.Background(), IssueRequest{ClientID: testClientID,
		IssuedBy: Principal{Subject: "op"},
		Subject:  Principal{Type: PrincipalTypeAgent, TenantID: uuid.New(), Subject: "root"},
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
	parent := issueRoot(t, issuer, Caveats{Requests: RequestCeiling{Unlimited: true}, Ops: []Op{OpGet, OpShare}, Budget: BudgetCeiling{Max: MustParseAmount("25"), UnitCode: "USD"}})

	if _, _, err := issuer.delegate(ctx, DelegateRequest{
		Subject: Principal{Type: PrincipalTypeAgent, Subject: "child"},
		Parent:  asVerified(parent), Caveats: Caveats{Requests: RequestCeiling{Unlimited: true}, Budget: BudgetCeiling{Max: MustParseAmount("2")}},
	}); !errors.Is(err, ErrInvalidCaveats) {
		t.Fatalf("delegate with ops omitted = %v, want ErrInvalidCaveats", err)
	}

	child, _, err := issuer.delegate(ctx, DelegateRequest{Subject: Principal{Type: PrincipalTypeAgent, Subject: "child"}, Parent: asVerified(parent), InheritCaveats: true})
	if err != nil {
		t.Fatalf("delegate with InheritCaveats: %v", err)
	}
	if child.Caveats.Budget != (BudgetCeiling{Max: 25 * NanosPerUnit, UnitCode: "USD"}) {
		t.Errorf("inherited budget = %v, want 25 USD", child.Caveats.Budget)
	}
}

// A child that names no unit counts in its parent's, so it is not refused
// as narrower in another; one that names a unit keeps it.
func TestDelegateGivesAChildItsParentsUnit(t *testing.T) {
	issuer, _, _, _ := buildIssuerVerifier(t)
	ctx := context.Background()
	const parentUnit = "EUR"
	parent := issueRoot(t, issuer, Caveats{Requests: RequestCeiling{Unlimited: true}, Ops: []Op{OpGet, OpShare}, Budget: BudgetCeiling{Max: MustParseAmount("25"), UnitCode: parentUnit}})
	for name, named := range map[string]string{"names none": "", "names the parent's": parentUnit} {
		child, _, err := issuer.delegate(ctx, DelegateRequest{
			Subject: Principal{Type: PrincipalTypeAgent, Subject: "child"},
			Parent:  asVerified(parent), Caveats: Caveats{Requests: RequestCeiling{Unlimited: true}, Ops: []Op{OpGet}, Budget: BudgetCeiling{Max: MustParseAmount("2"), UnitCode: named}},
		})
		if err != nil || child.Caveats.Budget.UnitCode != parentUnit {
			t.Errorf("a child that %s: unit %q, %v; want %q", name, child.Caveats.Budget.UnitCode, err, parentUnit)
		}
	}
	unitless := issueRoot(t, issuer, Caveats{Requests: RequestCeiling{Unlimited: true}, Ops: []Op{OpGet, OpShare}})
	child, _, err := issuer.delegate(ctx, DelegateRequest{Subject: Principal{Type: PrincipalTypeAgent, Subject: "child"}, Parent: asVerified(unitless), InheritCaveats: true})
	if err != nil || child.Caveats.Budget.UnitCode != "" {
		t.Errorf("a child of a parent with no unit: unit %q, %v; want none", child.Caveats.Budget.UnitCode, err)
	}
}

// foreignIssuer names an issuer other than the one under test.
const foreignIssuer = "someone-else"

// A parent another issuer named is refused: this issuer's store has no record
// of it, so revoking it at its own issuer would never stop the child.
func TestDelegateRefusesRevokedOrExpiredParent(t *testing.T) {
	issuer, _, store, _ := buildIssuerVerifier(t)
	ctx := context.Background()
	parent := issueRoot(t, issuer, Caveats{Requests: RequestCeiling{Unlimited: true}, Ops: []Op{OpGet, OpShare}})

	expired := parent
	expired.ExpiresAt = time.Now().Add(-time.Minute)
	if _, _, err := issuer.delegate(ctx, DelegateRequest{Subject: Principal{Type: PrincipalTypeAgent, Subject: "child"}, Parent: asVerified(expired), InheritCaveats: true}); !errors.Is(err, ErrExpired) {
		t.Errorf("delegate from expired parent = %v, want ErrExpired", err)
	}

	foreign := parent
	foreign.Issuer = foreignIssuer
	if _, _, err := issuer.delegate(ctx, DelegateRequest{Subject: Principal{Type: PrincipalTypeAgent, Subject: "child"}, Parent: asVerified(foreign), InheritCaveats: true}); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("delegate from another issuer's parent = %v, want ErrInvalidRequest", err)
	}

	_, _ = store.Revoke(ctx, RevokeRequest{CapabilityID: parent.ID})
	if _, _, err := issuer.delegate(ctx, DelegateRequest{Subject: Principal{Type: PrincipalTypeAgent, Subject: "child"}, Parent: asVerified(parent), InheritCaveats: true}); !errors.Is(err, ErrRevoked) {
		t.Errorf("delegate from revoked parent = %v, want ErrRevoked", err)
	}
}

// A capability's id is a UUIDv7, as Capability.ID promises: sortable by
// issuance, so a store's index on it stays tight. Delegated ones too.
func TestIssuedIDsAreVersion7(t *testing.T) {
	issuer, _, _, _ := buildIssuerVerifier(t)
	ctx := context.Background()
	parent, _, err := issuer.issue(ctx, IssueRequest{ClientID: testClientID,
		IssuedBy: Principal{Subject: "op"}, Subject: Principal{Type: PrincipalTypeAgent, TenantID: uuid.New(), Subject: "a"},
		Audience: []string{audienceData}, Caveats: Caveats{Requests: RequestCeiling{Unlimited: true}, Ops: []Op{OpGet, OpShare}}, TTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	child, _, err := issuer.delegate(ctx, DelegateRequest{Subject: Principal{Type: PrincipalTypeAgent, Subject: "child"}, Parent: asVerified(parent), InheritCaveats: true})
	if err != nil {
		t.Fatal(err)
	}
	// The version is the high four bits of octet 6 (RFC 9562 §4.2).
	const versionOctet, versionShift, v7 = 6, 4, 7
	for name, id := range map[string]uuid.UUID{"issued": parent.ID, "delegated": child.ID} {
		if v := id[versionOctet] >> versionShift; v != v7 {
			t.Errorf("%s id %s is version %d, want %d", name, id, v, v7)
		}
	}
}

// Issue, Delegate and Biscuit hand back what their unexported forms mint, in
// result structs that may grow: the capability as recorded and its token.
func TestMintingResults(t *testing.T) {
	issuer, v, _, _ := buildIssuerVerifier(t)
	ctx := context.Background()
	issued, err := issuer.Issue(ctx, IssueRequest{ClientID: testClientID,
		IssuedBy: Principal{Subject: "op"}, Subject: Principal{Type: PrincipalTypeAgent, TenantID: uuid.New(), Subject: "a"},
		Audience: []string{audienceData}, Caveats: Caveats{Requests: RequestCeiling{Unlimited: true}, Ops: []Op{OpGet, OpShare}}, TTL: time.Hour,
	})
	if err != nil || issued.Token == "" || issued.Capability.ID == uuid.Nil() {
		t.Fatalf("Issue = %+v, %v; want a capability and its token", issued, err)
	}
	vc, err := v.Verify(ctx, VerifyRequest{Token: issued.Token, Audience: audienceData})
	if err != nil || vc.Capability().ID != issued.Capability.ID {
		t.Fatalf("the issued token verifies as %s, %v; want %s", vc.Capability().ID, err, issued.Capability.ID)
	}
	delegated, err := issuer.Delegate(ctx, DelegateRequest{Parent: vc, Subject: Principal{Type: PrincipalTypeAgent, Subject: "child"}, InheritCaveats: true})
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
		parent := issueRoot(t, issuer, Caveats{Requests: RequestCeiling{Unlimited: true}, Ops: tc.ops})
		before := len(store.caps)
		_, err := issuer.Delegate(ctx, DelegateRequest{Parent: asVerified(parent), Subject: Principal{Type: PrincipalTypeAgent, Subject: "child"}, Caveats: Caveats{Requests: RequestCeiling{Unlimited: true}, Ops: []Op{OpGet}}})
		if tc.want == nil && err != nil || tc.want != nil && !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", name, err, tc.want)
		}
		if tc.want != nil && len(store.caps) != before {
			t.Errorf("%s: a refused delegation was recorded", name)
		}
	}
}

// A child starts no earlier than its parent, to the instant: a parent
// starting within a second — one built by hand, as a dry run may — is not
// rounded down to let a child in before it, and Delegate still mints a child
// of such a parent, starting at the next whole second.
func TestDelegationStartIsExact(t *testing.T) {
	const fraction, earlier = 700 * time.Millisecond, 500 * time.Millisecond
	second := time.Now().Truncate(time.Second)
	parent := Capability{NotBefore: second.Add(fraction), ExpiresAt: second.Add(time.Hour), Caveats: Caveats{Requests: RequestCeiling{Unlimited: true}, Ops: []Op{OpGet}}}
	for name, tc := range map[string]struct {
		start time.Time
		want  error
	}{
		"within the second, before the parent": {second.Add(fraction - earlier), ErrDelegationTooWide},
		"at the parent's start":                {second.Add(fraction), nil},
		"at the next second":                   {second.Add(time.Second), nil},
	} {
		child := parent
		child.NotBefore = tc.start
		if err := Narrows(parent, child); tc.want == nil && err != nil || tc.want != nil && !errors.Is(err, tc.want) {
			t.Errorf("%s: Narrows = %v, want %v", name, err, tc.want)
		}
	}

	issuer, _, _, _ := buildIssuerVerifier(t)
	root := issueRoot(t, issuer, Caveats{Requests: RequestCeiling{Unlimited: true}, Ops: []Op{OpGet, OpShare}})
	at := root.IssuedAt.Add(time.Second + fraction)
	root.NotBefore = at.Add(-earlier) // within the second at is in
	issuer.clock = func() time.Time { return at }
	child, _, err := issuer.delegate(t.Context(), DelegateRequest{Parent: asVerified(root), Subject: Principal{Type: PrincipalTypeAgent, Subject: "child"}, Caveats: Caveats{Requests: RequestCeiling{Unlimited: true}, Ops: []Op{OpGet}}})
	if err != nil {
		t.Fatalf("Delegate from a parent starting within the second: %v", err)
	}
	if validFrom(child).Before(validFrom(root)) {
		t.Errorf("the child starts at %s, before its parent at %s", validFrom(child), validFrom(root))
	}
}

// An insert names the capability it records, and one that expires: the zero
// id and no expiry are refused.
func TestInsertRequestValidate(t *testing.T) {
	expires := time.Now().Add(time.Hour)
	for name, tc := range map[string]struct {
		c  Capability
		ok bool
	}{
		"an id and an expiry": {Capability{ID: uuid.New(), ExpiresAt: expires}, true},
		"no id":               {Capability{ExpiresAt: expires}, false},
		"no expiry":           {Capability{ID: uuid.New()}, false},
	} {
		err := (InsertRequest{Capability: tc.c}).Validate()
		if tc.ok && err != nil {
			t.Errorf("an insert with %s: %v", name, err)
		}
		if !tc.ok && !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("an insert with %s: err = %v, want ErrInvalidRequest", name, err)
		}
	}
}

// A ceiling that lets anything be spent names the unit it counts in; the
// zero one, which lets nothing be, needs none.
func TestABudgetNeedsAUnit(t *testing.T) {
	for name, tc := range map[string]struct {
		b  BudgetCeiling
		ok bool
	}{
		"a bound in a unit":      {BudgetCeiling{Max: MustParseAmount("5"), UnitCode: "EUR"}, true},
		"a bound in no unit":     {BudgetCeiling{Max: MustParseAmount("5")}, false},
		"unlimited in a unit":    {BudgetCeiling{Unlimited: true, UnitCode: "EUR"}, true},
		"unlimited in no unit":   {BudgetCeiling{Unlimited: true}, false},
		"no spend in no unit":    {BudgetCeiling{}, true},
		"no spend in a unit":     {BudgetCeiling{UnitCode: "EUR"}, true},
		"a unit that is not one": {BudgetCeiling{UnitCode: "euro"}, false},
	} {
		err := (Caveats{Requests: RequestCeiling{Unlimited: true}, Ops: []Op{OpGet}, Budget: tc.b}).Validate()
		if tc.ok && err != nil {
			t.Errorf("%s: %v", name, err)
		}
		if !tc.ok && !errors.Is(err, ErrInvalidCaveats) {
			t.Errorf("%s: err = %v, want ErrInvalidCaveats", name, err)
		}
	}
}

// An operation neither built in nor namespaced is refused with the built-in
// ones named, so a caller that used an old unnamespaced op sees what to use.
func TestUnknownOpNamesTheBuiltins(t *testing.T) {
	err := Op("presign").Validate()
	if !errors.Is(err, ErrInvalidCaveats) {
		t.Fatalf("err = %v, want ErrInvalidCaveats", err)
	}
	for _, op := range []Op{OpGet, OpPut, OpList, OpDelete, OpShare, OpManage} {
		if !op.Builtin() || op.Validate() != nil {
			t.Errorf("%q is not accepted as built in", op)
		}
		if !strings.Contains(err.Error(), string(op)) {
			t.Errorf("error %q does not name %q", err, op)
		}
	}
	if len(builtinOps) != len(builtinOpList) {
		t.Errorf("builtinOps has %d ops, builtinOpList %d", len(builtinOps), len(builtinOpList))
	}
}
