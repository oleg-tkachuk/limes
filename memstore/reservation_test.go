package memstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes"
)

func reservationFixture(t *testing.T) (*UsageStore[struct{}], uuid.UUID, uuid.UUID, *time.Time) {
	t.Helper()
	now := time.Unix(1_800_000_000, 0)
	u := NewUsage[struct{}](nil).WithClock(func() time.Time { return now })
	tenant := uuid.New()
	if _, err := u.SetTenantBudget(context.Background(), limes.SetTenantBudgetRequest{TenantID: tenant, MaxBudgetAmount: limes.MustParseAmount("100")}); err != nil {
		t.Fatal(err)
	}
	return u, uuid.New(), tenant, &now
}

// A hold counts against the ceiling like spend does, so two callers cannot
// each reserve the same last 6.00.
func TestReservationsCountAgainstTheCeiling(t *testing.T) {
	ctx := context.Background()
	u, capID, tenant, _ := reservationFixture(t)
	reserve := func(amount limes.Nanos) (limes.Reservation, error) {
		return u.Reserve(ctx, limes.ReserveRequest{CapabilityID: capID, TenantID: tenant, Amount: amount, MaxBudget: limes.MustParseAmount("10")})
	}
	if _, err := reserve(6 * unit); err != nil {
		t.Fatalf("first hold: %v", err)
	}
	if _, err := reserve(6 * unit); !errors.Is(err, limes.ErrBudgetExceeded) {
		t.Fatalf("second hold past the ceiling: err = %v, want ErrBudgetExceeded", err)
	}
	if _, err := u.Charge(ctx, limes.ChargeRequest{CapabilityID: capID, TenantID: tenant, Amount: limes.MustParseAmount("5"), MaxBudget: limes.MustParseAmount("10")}, nil); !errors.Is(err, limes.ErrBudgetExceeded) {
		t.Fatalf("charge past spend+holds: err = %v, want ErrBudgetExceeded", err)
	}
	got, _ := u.GetUsage(ctx, capID)
	if got.ReservedAmount != 6*unit || got.SpentAmount != 0 {
		t.Errorf("usage = %+v, want 6 held and nothing spent", got)
	}
}

func TestSettleChargesTheActualCostAndReleasesTheHold(t *testing.T) {
	ctx := context.Background()
	u, capID, tenant, _ := reservationFixture(t)
	r, err := u.Reserve(ctx, limes.ReserveRequest{CapabilityID: capID, TenantID: tenant, Amount: limes.MustParseAmount("8"), MaxBudget: limes.MustParseAmount("10"), Op: "llm", Actor: "agent"})
	if err != nil {
		t.Fatal(err)
	}

	// Above the hold, the excess must fit: 8 held, 11 actual, ceiling 10.
	if _, err := u.Settle(ctx, limes.SettleRequest{ReservationID: r.ID, Amount: limes.MustParseAmount("11"), MaxBudget: limes.MustParseAmount("10")}, nil); !errors.Is(err, limes.ErrBudgetExceeded) {
		t.Fatalf("settle past the ceiling: err = %v, want ErrBudgetExceeded", err)
	}
	if got, _ := u.GetUsage(ctx, capID); got.ReservedAmount != 8*unit {
		t.Fatalf("a refused settle released the hold: %+v", got)
	}

	receipt, err := u.Settle(ctx, limes.SettleRequest{ReservationID: r.ID, Amount: limes.MustParseAmount("3"), MaxBudget: limes.MustParseAmount("10")}, nil)
	if err != nil {
		t.Fatalf("settle: %v", err)
	}
	got, _ := u.GetUsage(ctx, capID)
	if got.SpentAmount != 3*unit || got.ReservedAmount != 0 || receipt.ChargeID == uuid.Nil {
		t.Errorf("after settle usage = %+v receipt = %+v, want 3 spent, 0 held, a charge", got, receipt)
	}
	if b, _ := u.GetTenantBudget(ctx, tenant); b.SpentAmount != 3*unit || b.ReservedAmount != 0 {
		t.Errorf("tenant after settle = %+v", b)
	}
	if l := u.Ledger(); len(l) != 1 || l[0].Op != "llm" || l[0].Actor != "agent" {
		t.Errorf("ledger = %+v, want one charge carrying the reservation's op and actor", l)
	}
	again, err := u.Settle(ctx, limes.SettleRequest{ReservationID: r.ID, Amount: limes.MustParseAmount("1"), MaxBudget: limes.MustParseAmount("10")}, nil)
	if err != nil || !again.Replayed || again.ChargeID != receipt.ChargeID {
		t.Errorf("second settle = %+v, %v; want a replay of charge %s", again, err, receipt.ChargeID)
	}
	if got, _ := u.GetUsage(ctx, capID); got.SpentAmount != 3*unit || len(u.Ledger()) != 1 {
		t.Errorf("a replayed settle moved spend: %+v, ledger %d rows", got, len(u.Ledger()))
	}
}

func TestReleaseAndExpiryFreeTheHold(t *testing.T) {
	ctx := context.Background()
	u, capID, tenant, now := reservationFixture(t)
	r, _ := u.Reserve(ctx, limes.ReserveRequest{CapabilityID: capID, TenantID: tenant, Amount: limes.MustParseAmount("4"), MaxBudget: limes.MustParseAmount("10")})
	if err := u.Release(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if err := u.Release(ctx, r.ID); err != nil {
		t.Fatalf("repeated release: %v", err)
	}
	if got, _ := u.GetUsage(ctx, capID); got.ReservedAmount != 0 {
		t.Fatalf("held after release = %v", got.ReservedAmount)
	}

	r, _ = u.Reserve(ctx, limes.ReserveRequest{CapabilityID: capID, TenantID: tenant, Amount: limes.MustParseAmount("4"), MaxBudget: limes.MustParseAmount("10"), TTL: time.Minute})
	*now = now.Add(2 * time.Minute)
	if _, err := u.Settle(ctx, limes.SettleRequest{ReservationID: r.ID, Amount: limes.MustParseAmount("1"), MaxBudget: limes.MustParseAmount("10")}, nil); !errors.Is(err, limes.ErrReservationNotFound) {
		t.Fatalf("settle after expiry: err = %v, want ErrReservationNotFound", err)
	}
	if n, _ := u.ReleaseExpired(ctx); n != 1 {
		t.Fatalf("ReleaseExpired released %d, want 1", n)
	}
	if got, _ := u.GetUsage(ctx, capID); got.ReservedAmount != 0 {
		t.Fatalf("held after expiry sweep = %v", got.ReservedAmount)
	}
}

// A child's hold counts against its parent, like its spend does.
func TestReservationsCountAgainstAncestors(t *testing.T) {
	ctx := context.Background()
	s := New[struct{}]()
	u := NewUsage(s)
	rootID, kids, tenant := tree(t, s,
		limes.Caveats{Ops: []limes.Op{limes.OpGet}, MaxBudgetAmount: limes.MustParseAmount("25"), UnitCode: "USD"},
		2,
		limes.Caveats{Ops: []limes.Op{limes.OpGet}, MaxBudgetAmount: limes.MustParseAmount("20"), UnitCode: "USD"},
	)
	if _, err := u.Reserve(ctx, limes.ReserveRequest{CapabilityID: kids[0], TenantID: tenant, Amount: limes.MustParseAmount("20"), MaxBudget: limes.MustParseAmount("20")}); err != nil {
		t.Fatal(err)
	}
	if _, err := u.Reserve(ctx, limes.ReserveRequest{CapabilityID: kids[1], TenantID: tenant, Amount: limes.MustParseAmount("20"), MaxBudget: limes.MustParseAmount("20")}); !errors.Is(err, limes.ErrBudgetExceeded) {
		t.Fatalf("sibling hold past the parent: err = %v, want ErrBudgetExceeded", err)
	}
	if got, _ := u.GetUsage(ctx, rootID); got.ReservedAmount != 20*unit {
		t.Errorf("parent held = %v, want 20", got.ReservedAmount)
	}
}

// A hold that would cross the tenant aggregate is refused, and holds nothing.
func TestReservationsCountAgainstTheTenantBudget(t *testing.T) {
	ctx := context.Background()
	u, capID, tenant, _ := reservationFixture(t)
	if _, err := u.Charge(ctx, limes.ChargeRequest{CapabilityID: uuid.New(), TenantID: tenant, Amount: limes.MustParseAmount("90")}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := u.Reserve(ctx, limes.ReserveRequest{CapabilityID: capID, TenantID: tenant, Amount: limes.MustParseAmount("11")}); !errors.Is(err, limes.ErrTenantBudgetExceeded) {
		t.Fatalf("hold past the tenant budget: err = %v, want ErrTenantBudgetExceeded", err)
	}
	if b, _ := u.GetTenantBudget(ctx, tenant); b.ReservedAmount != 0 {
		t.Errorf("tenant held after a refused hold = %v, want 0", b.ReservedAmount)
	}
}
