package memstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes"
)

// Behavioural coverage for the reference store (T042). These are the store
// semantics the contract requires; a third party writing their own store can
// read this file as the conformance suite it effectively is.

func mkCap(id, parent uuid.UUID, tenant uuid.UUID, subject string) limes.Capability {
	return limes.Capability{
		ID: id, ParentID: parent,
		Subject:   limes.Principal{Type: limes.PrincipalAgent, TenantID: tenant, Subject: subject},
		ExpiresAt: time.Now().Add(time.Hour),
	}
}

// Revocation must be idempotent — an operator retrying a revoke, or two
// operators racing, must not turn the second call into an error.
func TestRevokeIsIdempotent(t *testing.T) {
	ctx := context.Background()
	s := New[struct{}]()
	id := uuid.New()
	if err := s.Insert(ctx, mkCap(id, uuid.Nil, uuid.New(), "a"), limes.Principal{Subject: "test-operator"}); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	for i := range 3 {
		if err := s.Revoke(ctx, limes.RevokeRequest{ID: id, Reason: "leak", Actor: "op"}); err != nil {
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
	s := New[struct{}]()
	tenant := uuid.New()
	root, child, grandchild := uuid.New(), uuid.New(), uuid.New()

	for _, c := range []limes.Capability{
		mkCap(root, uuid.Nil, tenant, "orchestrator"),
		mkCap(child, root, tenant, "worker"),
		mkCap(grandchild, child, tenant, "sub-worker"),
	} {
		if err := s.Insert(ctx, c, limes.Principal{Subject: "test-operator"}); err != nil {
			t.Fatalf("Insert: %v", err)
		}
	}

	if err := s.Revoke(ctx, limes.RevokeRequest{ID: root, CascadeChildren: true}); err != nil {
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
	s := New[struct{}]()
	tenant := uuid.New()
	root, child, grandchild := uuid.New(), uuid.New(), uuid.New()
	_ = s.Insert(ctx, mkCap(root, uuid.Nil, tenant, "parent"), limes.Principal{Subject: "test-operator"})
	_ = s.Insert(ctx, mkCap(child, root, tenant, "child"), limes.Principal{Subject: "test-operator"})
	_ = s.Insert(ctx, mkCap(grandchild, child, tenant, "grandchild"), limes.Principal{Subject: "test-operator"})

	if err := s.Revoke(ctx, limes.RevokeRequest{ID: child}); err != nil {
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
	s := New[struct{}]()
	_, err := s.Get(context.Background(), uuid.New())
	if !errors.Is(err, limes.ErrNotFound) {
		t.Fatalf("want limes.ErrNotFound, got %v", err)
	}
}

// The ceiling must reject WITHOUT mutating, so a rejected call is safe to
// retry and the counter cannot drift past the limit.
func TestBumpRequestRejectsWithoutMutating(t *testing.T) {
	ctx := context.Background()
	u := NewUsage[struct{}](nil)
	id := uuid.New()

	for i := int64(1); i <= 2; i++ {
		n, err := u.Bump(ctx, limes.BumpRequest{CapabilityID: id, MaxRequests: 2})
		if err != nil || n != i {
			t.Fatalf("bump %d: n=%d err=%v", i, n, err)
		}
	}
	n, err := u.Bump(ctx, limes.BumpRequest{CapabilityID: id, MaxRequests: 2})
	if !errors.Is(err, limes.ErrRequestLimitExceeded) {
		t.Fatalf("want ErrRequestLimitExceeded, got %v", err)
	}
	if n != 2 {
		t.Errorf("returned count = %d, want the unchanged 2", n)
	}
	got, _ := u.GetUsage(ctx, id)
	if got.RequestCount != 2 {
		t.Errorf("stored count = %d — a rejected bump mutated state", got.RequestCount)
	}
}

// maxRequests <= 0 means "no ceiling"; it must not be read as "zero allowed".
func TestBumpRequestUnlimited(t *testing.T) {
	ctx := context.Background()
	u := NewUsage[struct{}](nil)
	id := uuid.New()
	for i := range 50 {
		if _, err := u.Bump(ctx, limes.BumpRequest{CapabilityID: id, MaxRequests: 0}); err != nil {
			t.Fatalf("unlimited bump %d: %v", i, err)
		}
	}
}

// A pre-unit-tracking row must resolve to the default rather than persisting
// an empty unit — clients would otherwise render a bare number.
func TestSetTenantBudgetDefaultsUnitCode(t *testing.T) {
	ctx := context.Background()
	u := NewUsage[struct{}](nil)
	b, err := u.SetTenantBudget(ctx, limes.SetTenantBudgetRequest{
		TenantID: uuid.New(), MaxBudgetAmount: limes.MustParseAmount("10"),
	})
	if err != nil {
		t.Fatalf("SetTenantBudget: %v", err)
	}
	if b.UnitCode != limes.DefaultUnitCode {
		t.Errorf("UnitCode = %q, want %q", b.UnitCode, limes.DefaultUnitCode)
	}
}

func TestRefundReturnsSpendToEveryCounterOnce(t *testing.T) {
	ctx := context.Background()
	u := NewUsage[struct{}](nil)
	id, tenant := uuid.New(), uuid.New()
	_, _ = u.SetTenantBudget(ctx, limes.SetTenantBudgetRequest{TenantID: tenant, MaxBudgetAmount: limes.MustParseAmount("100")})
	receipt, err := u.Charge(ctx, limes.ChargeRequest{CapabilityID: id, TenantID: tenant, Amount: limes.MustParseAmount("5"), MaxBudget: limes.MustParseAmount("100"), UnitCode: "USD"}, nil)
	if err != nil {
		t.Fatalf("Charge: %v", err)
	}

	// A partial refund, then a refund of "the rest": the second must return
	// exactly what the first left, and a third must return nothing — a
	// retried full refund is a no-op, not a second credit.
	if got, err := u.Refund(ctx, limes.RefundRequest{ChargeID: receipt.ChargeID, Amount: limes.MustParseAmount("2")}); err != nil || got != 2*unit {
		t.Fatalf("partial refund = %v, %v; want 2, nil", got, err)
	}
	if got, err := u.Refund(ctx, limes.RefundRequest{ChargeID: receipt.ChargeID}); err != nil || got != 3*unit {
		t.Fatalf("remainder refund = %v, %v; want 3, nil", got, err)
	}
	if got, err := u.Refund(ctx, limes.RefundRequest{ChargeID: receipt.ChargeID}); err != nil || got != 0 {
		t.Fatalf("repeated full refund = %v, %v; want 0, nil", got, err)
	}
	if _, err := u.Refund(ctx, limes.RefundRequest{ChargeID: receipt.ChargeID, Amount: limes.MustParseAmount("1")}); !errors.Is(err, limes.ErrRefundExceedsCharge) {
		t.Fatalf("over-refund err = %v, want ErrRefundExceedsCharge", err)
	}
	if _, err := u.Refund(ctx, limes.RefundRequest{ChargeID: uuid.New()}); !errors.Is(err, limes.ErrChargeNotFound) {
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
	u := NewUsage[struct{}](nil)
	for _, amt := range []limes.Nanos{-1, limes.MaxNanos + 1} {
		_, err := u.Charge(ctx, limes.ChargeRequest{CapabilityID: uuid.New(), TenantID: uuid.New(), Amount: amt}, nil)
		if !errors.Is(err, limes.ErrInvalidAmount) {
			t.Errorf("Charge(%v) err = %v, want ErrInvalidAmount", amt, err)
		}
	}
}

// A revoked Biscuit copy is found by any of a token's ids, belongs to a
// capability on record, and goes when that capability is purged.
func TestRevokeBiscuit(t *testing.T) {
	ctx := context.Background()
	s := New[struct{}]()
	id := uuid.New()
	if err := s.Insert(ctx, mkCap(id, uuid.Nil, uuid.New(), "a"), limes.Principal{Subject: "op"}); err != nil {
		t.Fatal(err)
	}
	copyID, other := []byte("copy"), []byte("other")

	if err := s.RevokeBiscuit(ctx, limes.RevokeBiscuitRequest{CapabilityID: uuid.New(), RevocationID: copyID}); !errors.Is(err, limes.ErrNotFound) {
		t.Fatalf("unknown capability: err = %v, want ErrNotFound", err)
	}
	if err := s.RevokeBiscuit(ctx, limes.RevokeBiscuitRequest{CapabilityID: id}); err == nil {
		t.Fatal("empty revocation id accepted")
	}
	for range 2 { // idempotent
		if err := s.RevokeBiscuit(ctx, limes.RevokeBiscuitRequest{CapabilityID: id, RevocationID: copyID}); err != nil {
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
	if _, err := s.PurgeExpired(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.IsBiscuitRevoked(ctx, [][]byte{copyID}); got {
		t.Error("revoked copy outlived its purged capability")
	}
}
