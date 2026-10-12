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
	u := mustUsage(t, UsageStoreConfig[struct{}]{Records: newTestStore(), Now: func() time.Time { return now }})
	tenant := uuid.New()
	if _, err := u.SetTenantBudget(context.Background(), limes.SetTenantBudgetRequest{TenantID: tenant, Ceiling: limes.BudgetCeiling{Max: limes.MustParseAmount("100")}}); err != nil {
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
		return u.Reserve(ctx, limes.ReserveRequest{Subject: recordedSubject(ctx, t, u, subjectSpec{capID: capID, tenant: tenant, budget: budgetOf(limes.MustParseAmount("10"))}), Amount: amount})
	}
	if _, err := reserve(6 * unit); err != nil {
		t.Fatalf("first hold: %v", err)
	}
	if _, err := reserve(6 * unit); !errors.Is(err, limes.ErrBudgetExceeded) {
		t.Fatalf("second hold past the ceiling: err = %v, want ErrBudgetExceeded", err)
	}
	if _, err := u.Charge(ctx, limes.ChargeRequest{Subject: recordedSubject(ctx, t, u, subjectSpec{capID: capID, tenant: tenant, budget: budgetOf(limes.MustParseAmount("10"))}), Amount: limes.MustParseAmount("5")}, nil); !errors.Is(err, limes.ErrBudgetExceeded) {
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
	r, err := u.Reserve(ctx, limes.ReserveRequest{Subject: recordedSubject(ctx, t, u, subjectSpec{capID: capID, tenant: tenant, budget: budgetOf(limes.MustParseAmount("10"))}), Amount: limes.MustParseAmount("8"), Op: "llm", Actor: "agent"})
	if err != nil {
		t.Fatal(err)
	}

	// Above the hold, the excess must fit: 8 held, 11 actual, ceiling 10.
	if _, err := u.Settle(ctx, limes.SettleRequest{Subject: recordedSubject(ctx, t, u, subjectSpec{capID: capID, tenant: tenant, budget: budgetOf(limes.MustParseAmount("10"))}), ReservationID: r.ID, Amount: limes.MustParseAmount("11")}, nil); !errors.Is(err, limes.ErrBudgetExceeded) {
		t.Fatalf("settle past the ceiling: err = %v, want ErrBudgetExceeded", err)
	}
	if got, _ := u.GetUsage(ctx, capID); got.ReservedAmount != 8*unit {
		t.Fatalf("a refused settle released the hold: %+v", got)
	}

	receipt, err := u.Settle(ctx, limes.SettleRequest{Subject: recordedSubject(ctx, t, u, subjectSpec{capID: capID, tenant: tenant, budget: budgetOf(limes.MustParseAmount("10"))}), ReservationID: r.ID, Amount: limes.MustParseAmount("3")}, nil)
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
	if l := u.committedLedger(); len(l) != 1 || l[0].Op != "llm" || l[0].Actor != "agent" {
		t.Errorf("ledger = %+v, want one charge carrying the reservation's op and actor", l)
	}
	again, err := u.Settle(ctx, limes.SettleRequest{Subject: recordedSubject(ctx, t, u, subjectSpec{capID: capID, tenant: tenant, budget: budgetOf(limes.MustParseAmount("10"))}), ReservationID: r.ID, Amount: limes.MustParseAmount("1")}, nil)
	if err != nil || !again.Replayed || again.ChargeID != receipt.ChargeID {
		t.Errorf("second settle = %+v, %v; want a replay of charge %s", again, err, receipt.ChargeID)
	}
	if got, _ := u.GetUsage(ctx, capID); got.SpentAmount != 3*unit || len(u.committedLedger()) != 1 {
		t.Errorf("a replayed settle moved spend: %+v, ledger %d rows", got, len(u.committedLedger()))
	}
}

func TestReleaseAndExpiryFreeTheHold(t *testing.T) {
	ctx := context.Background()
	u, capID, tenant, now := reservationFixture(t)
	r, _ := u.Reserve(ctx, limes.ReserveRequest{Subject: recordedSubject(ctx, t, u, subjectSpec{capID: capID, tenant: tenant, budget: budgetOf(limes.MustParseAmount("10"))}), Amount: limes.MustParseAmount("4")})
	if err := u.Release(ctx, limes.ReleaseRequest{ReservationID: r.ID}); err != nil {
		t.Fatal(err)
	}
	if err := u.Release(ctx, limes.ReleaseRequest{ReservationID: r.ID}); err != nil {
		t.Fatalf("repeated release: %v", err)
	}
	if got, _ := u.GetUsage(ctx, capID); got.ReservedAmount != 0 {
		t.Fatalf("held after release = %v", got.ReservedAmount)
	}

	r, _ = u.Reserve(ctx, limes.ReserveRequest{Subject: recordedSubject(ctx, t, u, subjectSpec{capID: capID, tenant: tenant, budget: budgetOf(limes.MustParseAmount("10"))}), Amount: limes.MustParseAmount("4"), TTL: time.Minute})
	*now = now.Add(2 * time.Minute)
	if _, err := u.Settle(ctx, limes.SettleRequest{Subject: recordedSubject(ctx, t, u, subjectSpec{capID: capID, tenant: tenant, budget: budgetOf(limes.MustParseAmount("10"))}), ReservationID: r.ID, Amount: limes.MustParseAmount("1")}, nil); !errors.Is(err, limes.ErrReservationNotFound) {
		t.Fatalf("settle after expiry: err = %v, want ErrReservationNotFound", err)
	}
	if r, _ := u.ReleaseExpired(ctx, limes.ReleaseExpiredRequest{}); r.Released != 1 {
		t.Fatalf("ReleaseExpired released %d, want 1", r.Released)
	}
	if got, _ := u.GetUsage(ctx, capID); got.ReservedAmount != 0 {
		t.Fatalf("held after expiry sweep = %v", got.ReservedAmount)
	}
}

// A child's hold counts against its parent, like its spend does.
func TestReservationsCountAgainstAncestors(t *testing.T) {
	ctx := context.Background()
	s := newTestStore()
	u := newTestUsage(s)
	rootID, kids, tenant := tree(t, s,
		limes.Caveats{Ops: []limes.Op{limes.OpGet}, Budget: limes.BudgetCeiling{Max: limes.MustParseAmount("25")}, UnitCode: "USD"},
		2,
		limes.Caveats{Ops: []limes.Op{limes.OpGet}, Budget: limes.BudgetCeiling{Max: limes.MustParseAmount("20")}, UnitCode: "USD"},
	)
	if _, err := u.Reserve(ctx, limes.ReserveRequest{Subject: recordedSubject(ctx, t, u, subjectSpec{capID: kids[0], tenant: tenant, budget: budgetOf(limes.MustParseAmount("20"))}), Amount: limes.MustParseAmount("20")}); err != nil {
		t.Fatal(err)
	}
	if _, err := u.Reserve(ctx, limes.ReserveRequest{Subject: recordedSubject(ctx, t, u, subjectSpec{capID: kids[1], tenant: tenant, budget: budgetOf(limes.MustParseAmount("20"))}), Amount: limes.MustParseAmount("20")}); !errors.Is(err, limes.ErrBudgetExceeded) {
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
	if _, err := u.Charge(ctx, limes.ChargeRequest{Subject: recordedSubject(ctx, t, u, subjectSpec{capID: uuid.New(), tenant: tenant}), Amount: limes.MustParseAmount("90")}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := u.Reserve(ctx, limes.ReserveRequest{Subject: recordedSubject(ctx, t, u, subjectSpec{capID: capID, tenant: tenant}), Amount: limes.MustParseAmount("11")}); !errors.Is(err, limes.ErrTenantBudgetExceeded) {
		t.Fatalf("hold past the tenant budget: err = %v, want ErrTenantBudgetExceeded", err)
	}
	if b, _ := u.GetTenantBudget(ctx, tenant); b.ReservedAmount != 0 {
		t.Errorf("tenant held after a refused hold = %v, want 0", b.ReservedAmount)
	}
}

// A hold's expiry is a wall time, as the cursor it is paged by: sorted by a
// monotonic reading and sought by a wall one, a page would skip or repeat a
// hold after the wall clock steps. Round(0) drops a monotonic reading, so a
// time equal to its own Round(0), compared with ==, carries none.
func TestHoldExpiryIsAWallTime(t *testing.T) {
	ctx := context.Background()
	u := mustUsage(t, UsageStoreConfig[struct{}]{Records: newTestStore()}) // time.Now, which carries one
	capID, tenant := uuid.New(), uuid.New()
	r, err := u.Reserve(ctx, limes.ReserveRequest{Subject: recordedSubject(ctx, t, u, subjectSpec{capID: capID, tenant: tenant}), Amount: unit})
	if err != nil {
		t.Fatal(err)
	}
	page, err := u.ListReservations(ctx, limes.ListReservationsRequest{CapabilityID: capID})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("ListReservations = %+v, %v; want the one hold", page.Items, err)
	}
	for name, at := range map[string]time.Time{"Reserve": r.ExpiresAt, "ListReservations": page.Items[0].ExpiresAt} {
		if at != at.Round(0) {
			t.Errorf("%s: ExpiresAt %v carries a monotonic reading", name, at)
		}
	}
}
