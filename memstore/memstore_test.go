package memstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/meter"
)

// Behavioural coverage for the reference store (T042). These are the store
// semantics the contract requires; a third party writing their own store can
// read this file as the conformance suite it effectively is.

func mkCap(id, parent uuid.UUID, tenant uuid.UUID, subject string) limes.Capability {
	return limes.Capability{
		ID: id, ParentID: parent,
		Subject:   limes.Principal{Type: limes.PrincipalTypeAgent, TenantID: tenant, Subject: subject},
		ExpiresAt: contractExpiry,
		// No ceiling of its own: a test that needs one sets it.
		Caveats: limes.Caveats{
			Requests: limes.RequestCeiling{Unlimited: true}, Budget: limes.BudgetCeiling{Unlimited: true, UnitCode: "USD"},
		},
	}
}

// Revocation must be idempotent — an operator retrying a revoke, or two
// operators racing, must not turn the second call into an error.
func TestRevokeIsIdempotent(t *testing.T) {
	ctx := context.Background()
	s := newTestStore()
	id := uuid.New()
	if err := s.Insert(ctx, limes.InsertRequest{Capability: mkCap(id, uuid.Nil, uuid.New(), "a"), IssuedBy: limes.Principal{Subject: "test-operator"}}); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	for i := range 3 {
		if err := s.Revoke(ctx, limes.RevokeRequest{CapabilityID: id, Note: "leak", Actor: "op"}); err != nil {
			t.Fatalf("Revoke #%d: %v", i+1, err)
		}
	}
	revoked, err := s.IsRevoked(ctx, id)
	if err != nil || !revoked {
		t.Errorf("IsRevoked = %v, %v; want true, nil", revoked, err)
	}
}

// Cascade is what tears down the sub-agents an orchestrator spawned. Without
// it, revoking the parent leaves its children running with live authority.
func TestRevokeCascadesToDescendants(t *testing.T) {
	ctx := context.Background()
	s := newTestStore()
	tenant := uuid.New()
	root, child, grandchild := uuid.New(), uuid.New(), uuid.New()

	for _, c := range []limes.Capability{
		mkCap(root, uuid.Nil, tenant, "orchestrator"),
		mkCap(child, root, tenant, "worker"),
		mkCap(grandchild, child, tenant, "sub-worker"),
	} {
		if err := s.Insert(ctx, limes.InsertRequest{Capability: c, IssuedBy: limes.Principal{Subject: "test-operator"}}); err != nil {
			t.Fatalf("Insert: %v", err)
		}
	}

	if err := s.Revoke(ctx, limes.RevokeRequest{CapabilityID: root, CascadeChildren: true}); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	for name, id := range map[string]uuid.UUID{"root": root, "child": child, "grandchild": grandchild} {
		revoked, _ := s.IsRevoked(ctx, id)
		if !revoked {
			t.Errorf("%s not revoked — cascade must reach the whole subtree", name)
		}
	}
}

// Revoking a parent revokes its descendants whether or not the revoke
// cascaded: IsRevoked answers for the whole chain. Before, a non-cascading
// revoke left every delegated child working until its own TTL, so stopping a
// misbehaving orchestrator without the flag stopped nothing it had spawned.
// Revocation still flows only downwards — revoking a child leaves its parent.
func TestRevokingAParentRevokesItsChildrenWithoutCascade(t *testing.T) {
	ctx := context.Background()
	s := newTestStore()
	tenant := uuid.New()
	root, child, grandchild := uuid.New(), uuid.New(), uuid.New()
	_ = s.Insert(ctx, limes.InsertRequest{Capability: mkCap(root, uuid.Nil, tenant, "parent"), IssuedBy: limes.Principal{Subject: "test-operator"}})
	_ = s.Insert(ctx, limes.InsertRequest{Capability: mkCap(child, root, tenant, "child"), IssuedBy: limes.Principal{Subject: "test-operator"}})
	_ = s.Insert(ctx, limes.InsertRequest{Capability: mkCap(grandchild, child, tenant, "grandchild"), IssuedBy: limes.Principal{Subject: "test-operator"}})

	if err := s.Revoke(ctx, limes.RevokeRequest{CapabilityID: child}); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if r, _ := s.IsRevoked(ctx, root); r {
		t.Error("revoking a child revoked its parent")
	}
	if r, _ := s.IsRevoked(ctx, grandchild); !r {
		t.Error("grandchild still live after its parent was revoked without CascadeChildren")
	}
}

// An absent record is forgery, not a missing entity (contracts §1.1).
func TestGetAbsentReturnsErrNotFound(t *testing.T) {
	s := newTestStore()
	_, err := s.GetRecord(context.Background(), uuid.New())
	if !errors.Is(err, limes.ErrNotFound) {
		t.Fatalf("want limes.ErrNotFound, got %v", err)
	}
}

// The ceiling must reject WITHOUT mutating, so a rejected call is safe to
// retry and the counter cannot drift past the limit.
func TestBumpRequestRejectsWithoutMutating(t *testing.T) {
	ctx := context.Background()
	u := newTestUsage(newTestStore())
	id := uuid.New()

	for i := int64(1); i <= 2; i++ {
		r, err := u.Bump(ctx, meter.BumpRequest{Subject: recordedSubject(ctx, t, u, subjectSpec{capID: id, tenant: testTenant, requests: requestsOf(2)})})
		if err != nil || r.RequestCount != i {
			t.Fatalf("bump %d: n=%d err=%v", i, r.RequestCount, err)
		}
	}
	n, err := u.Bump(ctx, meter.BumpRequest{Subject: recordedSubject(ctx, t, u, subjectSpec{capID: id, tenant: testTenant, requests: requestsOf(2)})})
	if !errors.Is(err, limes.ErrRequestLimitExceeded) {
		t.Fatalf("want ErrRequestLimitExceeded, got %v", err)
	}
	if n.RequestCount != 2 {
		t.Errorf("returned count = %d, want the unchanged 2", n.RequestCount)
	}
	got, _ := u.GetUsage(ctx, id)
	if got.RequestCount != 2 {
		t.Errorf("stored count = %d — a rejected bump mutated state", got.RequestCount)
	}
}

// maxRequests <= 0 means "no ceiling"; it must not be read as "zero allowed".
func TestBumpRequestUnlimited(t *testing.T) {
	ctx := context.Background()
	u := newTestUsage(newTestStore())
	id := uuid.New()
	for i := range 50 {
		if _, err := u.Bump(ctx, meter.BumpRequest{Subject: recordedSubject(ctx, t, u, subjectSpec{capID: id, tenant: testTenant})}); err != nil {
			t.Fatalf("unlimited bump %d: %v", i, err)
		}
	}
}

// A tenant frozen before it counts anything names no unit, and a ceiling
// that lets something be spent names its own: none is assumed.
func TestSetTenantBudgetAssumesNoUnit(t *testing.T) {
	ctx := context.Background()
	u := newTestUsage(newTestStore())
	b, err := u.SetTenantBudget(ctx, meter.SetTenantBudgetRequest{
		TenantID: uuid.New(), Ceiling: limes.BudgetCeiling{},
	})
	if err != nil {
		t.Fatalf("SetTenantBudget: %v", err)
	}
	if b.Ceiling.UnitCode != "" {
		t.Errorf("UnitCode = %q, want none", b.Ceiling.UnitCode)
	}
	if _, err := u.SetTenantBudget(ctx, meter.SetTenantBudgetRequest{
		TenantID: uuid.New(), Ceiling: limes.BudgetCeiling{Max: limes.MustParseAmount("10")},
	}); !errors.Is(err, limes.ErrInvalidRequest) {
		t.Errorf("a ceiling of 10 in no unit: err = %v, want ErrInvalidRequest", err)
	}
}

func TestRefundReturnsSpendToEveryCounterOnce(t *testing.T) {
	ctx := context.Background()
	u := newTestUsage(newTestStore())
	id, tenant := uuid.New(), uuid.New()
	_, _ = u.SetTenantBudget(ctx, meter.SetTenantBudgetRequest{TenantID: tenant, Ceiling: limes.BudgetCeiling{Max: limes.MustParseAmount("100")}})
	receipt, err := u.Charge(ctx, meter.ChargeRequest{Subject: recordedSubject(ctx, t, u, subjectSpec{capID: id, tenant: tenant, budget: budgetOf(limes.MustParseAmount("100")), unit: "USD"}), Amount: limes.MustParseAmount("5")}, nil)
	if err != nil {
		t.Fatalf("Charge: %v", err)
	}

	// A partial refund, then a refund of "the rest": the second must return
	// exactly what the first left, and a third must return nothing — a
	// retried full refund is a no-op, not a second credit.
	if got, err := u.Refund(ctx, meter.RefundRequest{ChargeID: receipt.ChargeID, Amount: limes.MustParseAmount("2")}); err != nil || got.Amount != 2*unit {
		t.Fatalf("partial refund = %v, %v; want 2, nil", got, err)
	}
	if got, err := u.Refund(ctx, meter.RefundRequest{ChargeID: receipt.ChargeID, Remaining: true}); err != nil || got.Amount != 3*unit {
		t.Fatalf("remainder refund = %v, %v; want 3, nil", got, err)
	}
	if got, err := u.Refund(ctx, meter.RefundRequest{ChargeID: receipt.ChargeID, Remaining: true}); err != nil || got.Amount != 0 {
		t.Fatalf("repeated full refund = %v, %v; want 0, nil", got, err)
	}
	if _, err := u.Refund(ctx, meter.RefundRequest{ChargeID: receipt.ChargeID, Amount: limes.MustParseAmount("1")}); !errors.Is(err, meter.ErrRefundExceedsCharge) {
		t.Fatalf("over-refund err = %v, want ErrRefundExceedsCharge", err)
	}
	if _, err := u.Refund(ctx, meter.RefundRequest{ChargeID: uuid.New(), Remaining: true}); !errors.Is(err, meter.ErrChargeNotFound) {
		t.Fatalf("unknown charge err = %v, want ErrChargeNotFound", err)
	}

	got, _ := u.GetUsage(ctx, id)
	if got.SpentAmount != 0 {
		t.Errorf("capability spend = %v, want 0", got.SpentAmount)
	}
	b, _ := u.GetTenantBudget(ctx, tenant)
	if b.SpentAmount != 0 {
		t.Errorf("tenant spend = %v, want 0", b.SpentAmount)
	}
}

func TestChargeRejectsInvalidAmounts(t *testing.T) {
	ctx := context.Background()
	u := newTestUsage(newTestStore())
	for _, amt := range []limes.Nanos{-1, limes.MaxNanos + 1} {
		_, err := u.Charge(ctx, meter.ChargeRequest{Subject: recordedSubject(ctx, t, u, subjectSpec{capID: uuid.New(), tenant: uuid.New()}), Amount: amt}, nil)
		if !errors.Is(err, limes.ErrInvalidAmount) {
			t.Errorf("Charge(%v) err = %v, want ErrInvalidAmount", amt, err)
		}
	}
}

// A revoked Biscuit copy is found by any of a token's ids, belongs to a
// capability on record, and goes when that capability is purged.
func TestRevokeBiscuit(t *testing.T) {
	ctx := context.Background()
	s := newTestStore()
	id := uuid.New()
	if err := s.Insert(ctx, limes.InsertRequest{Capability: mkCap(id, uuid.Nil, uuid.New(), "a"), IssuedBy: limes.Principal{Subject: "op"}}); err != nil {
		t.Fatal(err)
	}
	copyID, other := []byte("copy"), []byte("other")

	if err := s.RevokeBiscuit(ctx, limes.RevokeBiscuitRequest{Copy: biscuitCopy(uuid.New(), copyID)}); !errors.Is(err, limes.ErrNotFound) {
		t.Fatalf("unknown capability: err = %v, want ErrNotFound", err)
	}
	if err := s.RevokeBiscuit(ctx, limes.RevokeBiscuitRequest{Copy: biscuitCopy(id, nil)}); err == nil {
		t.Fatal("empty revocation id accepted")
	}
	for range 2 { // idempotent
		if err := s.RevokeBiscuit(ctx, limes.RevokeBiscuitRequest{Copy: biscuitCopy(id, copyID)}); err != nil {
			t.Fatal(err)
		}
	}
	for name, tc := range map[string]struct {
		ids  [][]byte
		want bool
	}{
		"the copy alone":          {[][]byte{copyID}, true},
		"a copy attenuated after": {[][]byte{copyID, other}, true},
		"another copy":            {[][]byte{other}, false},
		"no ids":                  {nil, false},
	} {
		if got, err := s.IsBiscuitRevoked(ctx, tc.ids); err != nil || got != tc.want {
			t.Errorf("%s: IsBiscuitRevoked = %v, %v; want %v", name, got, err, tc.want)
		}
	}

	s.nowFn = func() time.Time { return time.Now().Add(2 * time.Hour) }
	if _, err := s.Purge(ctx, limes.PurgeRequest{}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.IsBiscuitRevoked(ctx, [][]byte{copyID}); got {
		t.Error("revoked copy outlived its purged capability")
	}
}

// A hold of a capability no longer on record cannot be settled or released
// by anyone, so PurgeOrphans releases it with the counters, or the tenant's
// ceiling stays short by it for good.
func TestPurgeOrphansReleasesHolds(t *testing.T) {
	ctx := context.Background()
	records := newTestStore()
	u := newTestUsage(records)
	tenant, gone := uuid.New(), uuid.New() // held while on record, then purged
	const held = 3 * limes.NanosPerUnit
	res, err := u.Reserve(ctx, meter.ReserveRequest{Subject: recordedSubject(ctx, t, u, subjectSpec{capID: gone, tenant: tenant}), Amount: held})
	if err != nil {
		t.Fatal(err)
	}
	dropRecord(records, gone)
	if r, err := u.PurgeOrphans(ctx, meter.PurgeOrphansRequest{}); err != nil || r.Purged != 1 {
		t.Fatalf("PurgeOrphans = %+v, %v; want the one orphan row", r, err)
	}
	if _, err := u.GetReservation(ctx, res.ID); !errors.Is(err, meter.ErrReservationNotFound) {
		t.Errorf("the orphan's hold: err = %v, want ErrReservationNotFound", err)
	}
	if b, err := u.GetTenantBudget(ctx, tenant); err != nil || b.ReservedAmount != 0 {
		t.Errorf("tenant after the purge = %+v, %v; want nothing held", b, err)
	}
	if _, err := u.GetUsage(ctx, gone); !errors.Is(err, meter.ErrUsageNotFound) {
		t.Errorf("the orphan's usage row: err = %v, want ErrUsageNotFound", err)
	}
}
