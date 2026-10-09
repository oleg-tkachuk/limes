package memstore

import (
	"context"
	"crypto/ed25519"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes"
)

// tree inserts a root with the given caveats and n children each holding
// childCaveats, returning the root and children IDs.
func tree(t *testing.T, s *Store[struct{}], root limes.Caveats, n int, child limes.Caveats) (uuid.UUID, []uuid.UUID, uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	tenant := uuid.New()
	rootID := uuid.New()
	rc := mkCap(rootID, uuid.Nil, tenant, "orchestrator")
	rc.Caveats = root
	if err := s.Insert(ctx, rc, limes.Principal{Subject: "op"}); err != nil {
		t.Fatal(err)
	}
	var kids []uuid.UUID
	for range n {
		id := uuid.New()
		c := mkCap(id, rootID, tenant, "worker")
		c.Caveats = child
		if err := s.Insert(ctx, c, limes.Principal{Subject: "orchestrator"}); err != nil {
			t.Fatal(err)
		}
		kids = append(kids, id)
	}
	return rootID, kids, tenant
}

// The parent's budget bounds the sum of its children's spend. Before, each
// child was metered alone, so a 25.00 parent with ten 20.00 children could
// spend 200.00.
func TestChildrenCannotSpendPastTheirParentsBudget(t *testing.T) {
	ctx := context.Background()
	s := New[struct{}]()
	u := NewUsage(s)
	rootID, kids, tenant := tree(t, s,
		limes.Caveats{Ops: []limes.Op{limes.OpGet}, MaxBudgetAmount: limes.MustParseAmount("25"), UnitCode: "USD"},
		2,
		limes.Caveats{Ops: []limes.Op{limes.OpGet}, MaxBudgetAmount: limes.MustParseAmount("20"), UnitCode: "USD"},
	)

	charge := func(id uuid.UUID, amount limes.Nanos) (limes.ChargeReceipt, error) {
		return u.Charge(ctx, limes.ChargeRequest{
			CapabilityID: id, TenantID: tenant, Amount: amount, MaxBudget: limes.MustParseAmount("20"), UnitCode: "USD",
		}, nil)
	}
	first, err := charge(kids[0], 20*unit)
	if err != nil {
		t.Fatalf("first child within budget: %v", err)
	}
	if _, err := charge(kids[1], 20*unit); !errors.Is(err, limes.ErrBudgetExceeded) {
		t.Fatalf("second child past the parent's ceiling: err = %v, want ErrBudgetExceeded", err)
	}
	// The rejection mutated nothing: the second child is still at zero.
	if got, err := u.GetUsage(ctx, kids[1]); err == nil && got.SpentAmount != 0 {
		t.Errorf("rejected child's spend = %v, want 0", got.SpentAmount)
	}
	if _, err := charge(kids[1], 5*unit); err != nil {
		t.Fatalf("second child within what the parent has left: %v", err)
	}
	root, _ := u.GetUsage(ctx, rootID)
	if root.SpentAmount != 25*unit {
		t.Errorf("parent subtree spend = %v, want 25", root.SpentAmount)
	}

	// A refund returns spend to the parent too, re-opening room for siblings.
	if _, err := u.Refund(ctx, limes.RefundRequest{ChargeID: first.ChargeID}); err != nil {
		t.Fatalf("Refund: %v", err)
	}
	root, _ = u.GetUsage(ctx, rootID)
	if root.SpentAmount != 5*unit {
		t.Errorf("parent subtree spend after refund = %v, want 5", root.SpentAmount)
	}
	if _, err := charge(kids[1], 15*unit); err != nil {
		t.Fatalf("sibling after refund: %v", err)
	}
}

func TestChildrenCannotExceedTheirParentsRequestCount(t *testing.T) {
	ctx := context.Background()
	s := New[struct{}]()
	u := NewUsage(s)
	rootID, kids, tenant := tree(t, s,
		limes.Caveats{Ops: []limes.Op{limes.OpGet}, MaxRequests: 3},
		2,
		limes.Caveats{Ops: []limes.Op{limes.OpGet}, MaxRequests: 3},
	)
	bump := func(id uuid.UUID) error {
		_, err := u.Bump(ctx, limes.BumpRequest{CapabilityID: id, TenantID: tenant, MaxRequests: 3})
		return err
	}
	for i, id := range []uuid.UUID{kids[0], kids[0], kids[1]} {
		if err := bump(id); err != nil {
			t.Fatalf("bump %d: %v", i, err)
		}
	}
	if err := bump(kids[1]); !errors.Is(err, limes.ErrRequestLimitExceeded) {
		t.Fatalf("fourth request across the subtree: err = %v, want ErrRequestLimitExceeded", err)
	}
	root, _ := u.GetUsage(ctx, rootID)
	if root.RequestCount != 3 {
		t.Errorf("parent subtree requests = %d, want 3", root.RequestCount)
	}
}

// End to end: a token delegated from a parent stops verifying when the
// parent is revoked, without CascadeChildren and without touching the child.
func TestVerifierRejectsChildOfRevokedParent(t *testing.T) {
	ctx := context.Background()
	kid, pub, priv, err := limes.GenerateEd25519Keypair()
	if err != nil {
		t.Fatal(err)
	}
	signer, _ := limes.NewEd25519Signer(kid, priv)
	records := New[struct{}]()
	issuer, _ := limes.NewIssuer(limes.IssuerConfig{Signer: signer, Store: records, IssuerName: "iss"})
	verifier, _ := limes.NewStandardVerifier(limes.VerifierConfig{
		Keys:           limes.NewStaticKeyResolver(map[string]ed25519.PublicKey{kid: pub}),
		Revocations:    records,
		TrustedIssuers: []string{"iss"},
	})

	parent, _, err := issuer.Issue(ctx, limes.IssueRequest{
		IssuedBy: limes.Principal{Subject: "op"},
		Subject:  limes.Principal{TenantID: uuid.New(), Subject: "orchestrator"},
		Audience: []string{"data"},
		Caveats:  limes.Caveats{Ops: []limes.Op{limes.OpGet}},
		TTL:      time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, childToken, err := issuer.Delegate(ctx, limes.DelegateRequest{Parent: parent, InheritCaveats: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifier.Verify(ctx, childToken, "data"); err != nil {
		t.Fatalf("child before revoke: %v", err)
	}
	if err := records.Revoke(ctx, limes.RevokeRequest{ID: parent.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := verifier.Verify(ctx, childToken, "data"); !errors.Is(err, limes.ErrRevoked) {
		t.Fatalf("child after parent revoke: err = %v, want ErrRevoked", err)
	}
}
