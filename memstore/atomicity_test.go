package memstore

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes"
)

// The charge-atomicity guard.
//
// This property is why UsageStore is generic in TX at all: onCharged runs
// inside the charge so a consumer's outbox write commits with the spend,
// closing the dual-write window (ADR-0003). Before this file, it had NO test
// anywhere in the repository — verified by grepping every *_test.go for
// onCharged and finding nothing. The guard therefore closes a pre-existing
// gap rather than merely preserving coverage across the move.

func TestChargeRollsBackWhenSideEffectFails(t *testing.T) {
	ctx := context.Background()
	capID, tenantID := uuid.New(), uuid.New()

	usage := NewUsage[struct{}](nil)
	if _, err := usage.SetTenantBudget(ctx, limes.SetTenantBudgetRequest{
		TenantID: tenantID, MaxBudgetAmount: limes.MustParseAmount("100"), UnitCode: "USD",
	}); err != nil {
		t.Fatalf("SetTenantBudget: %v", err)
	}

	// A first charge that succeeds, so there is real state to preserve.
	if _, err := usage.Charge(ctx, limes.ChargeRequest{CapabilityID: capID, TenantID: tenantID, Amount: limes.MustParseAmount("10"), MaxBudget: limes.MustParseAmount("100"), UnitCode: "USD", Op: "read", Actor: "alice"}, nil); err != nil {
		t.Fatalf("first charge: %v", err)
	}

	before, err := usage.GetUsage(ctx, capID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	budgetBefore, err := usage.GetTenantBudget(ctx, tenantID)
	if err != nil {
		t.Fatalf("GetTenantBudget: %v", err)
	}
	ledgerBefore := len(usage.Ledger())

	// Now a charge whose side effect fails.
	boom := errors.New("outbox insert failed")
	spent, err := usage.Charge(ctx, limes.ChargeRequest{CapabilityID: capID, TenantID: tenantID, Amount: limes.MustParseAmount("25"), MaxBudget: limes.MustParseAmount("100"), UnitCode: "USD", Op: "write", Actor: "alice"}, func(context.Context, struct{}) error { return boom })

	if !errors.Is(err, boom) {
		t.Fatalf("want the side-effect error, got %v", err)
	}

	// The whole point: nothing moved.
	after, err := usage.GetUsage(ctx, capID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if after.SpentAmount != before.SpentAmount {
		t.Errorf("capability spend moved: %v → %v", before.SpentAmount, after.SpentAmount)
	}
	if spent.Spent != before.SpentAmount {
		t.Errorf("returned spend = %v, want the unchanged %v", spent.Spent, before.SpentAmount)
	}

	budgetAfter, err := usage.GetTenantBudget(ctx, tenantID)
	if err != nil {
		t.Fatalf("GetTenantBudget: %v", err)
	}
	if budgetAfter.SpentAmount != budgetBefore.SpentAmount {
		t.Errorf("tenant spend moved: %v → %v", budgetBefore.SpentAmount, budgetAfter.SpentAmount)
	}

	// The ledger is the third thing that must not have grown — a committed
	// ledger row for a rolled-back charge is exactly the dual-write divergence
	// this design exists to prevent.
	if got := len(usage.Ledger()); got != ledgerBefore {
		t.Errorf("ledger grew from %d to %d despite the rollback", ledgerBefore, got)
	}
}

// A retry after a failed side effect must land cleanly — if the first attempt
// had partially applied, the retry would double-charge.
func TestChargeRetryAfterRollbackIsClean(t *testing.T) {
	ctx := context.Background()
	capID, tenantID := uuid.New(), uuid.New()
	usage := NewUsage[struct{}](nil)

	boom := errors.New("transient")
	if _, err := usage.Charge(ctx, limes.ChargeRequest{CapabilityID: capID, TenantID: tenantID, Amount: limes.MustParseAmount("7"), MaxBudget: limes.MustParseAmount("100"), UnitCode: "USD", Op: "op", Actor: "actor"}, func(context.Context, struct{}) error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("want the failure, got %v", err)
	}

	spent, err := usage.Charge(ctx, limes.ChargeRequest{CapabilityID: capID, TenantID: tenantID, Amount: limes.MustParseAmount("7"), MaxBudget: limes.MustParseAmount("100"), UnitCode: "USD", Op: "op", Actor: "actor"}, nil)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if spent.Spent != 7*unit {
		t.Errorf("spend after one failed + one successful charge = %v, want 7", spent.Spent)
	}
	if got := len(usage.Ledger()); got != 1 {
		t.Errorf("ledger has %d entries, want exactly 1", got)
	}
}

// Rejection by the capability ceiling must leave both counters untouched, so a
// caller can retry with a smaller amount without compensating anything.
func TestChargeCapabilityCeilingLeavesBothCountersUnmutated(t *testing.T) {
	ctx := context.Background()
	capID, tenantID := uuid.New(), uuid.New()
	usage := NewUsage[struct{}](nil)
	if _, err := usage.SetTenantBudget(ctx, limes.SetTenantBudgetRequest{
		TenantID: tenantID, MaxBudgetAmount: limes.MustParseAmount("1000"), UnitCode: "USD",
	}); err != nil {
		t.Fatalf("SetTenantBudget: %v", err)
	}

	_, err := usage.Charge(ctx, limes.ChargeRequest{CapabilityID: capID, TenantID: tenantID, Amount: limes.MustParseAmount("50"), MaxBudget: limes.MustParseAmount("10"), UnitCode: "USD", Op: "op", Actor: "actor"}, nil)
	if !errors.Is(err, limes.ErrBudgetExceeded) {
		t.Fatalf("want ErrBudgetExceeded, got %v", err)
	}

	if _, err := usage.GetUsage(ctx, capID); !errors.Is(err, limes.ErrUsageNotFound) {
		t.Errorf("a rejected first charge must create no usage row, got %v", err)
	}
	b, err := usage.GetTenantBudget(ctx, tenantID)
	if err != nil {
		t.Fatalf("GetTenantBudget: %v", err)
	}
	if b.SpentAmount != 0 {
		t.Errorf("tenant spend = %v after a capability-ceiling rejection, want 0", b.SpentAmount)
	}
}

// The tenant ceiling is checked AFTER the capability one, so this is the case
// where a naive implementation would have already applied the capability side
// and would need a compensating refund. Staging removes that need entirely.
func TestChargeTenantCeilingLeavesBothCountersUnmutated(t *testing.T) {
	ctx := context.Background()
	capID, tenantID := uuid.New(), uuid.New()
	usage := NewUsage[struct{}](nil)
	if _, err := usage.SetTenantBudget(ctx, limes.SetTenantBudgetRequest{
		TenantID: tenantID, MaxBudgetAmount: limes.MustParseAmount("20"), UnitCode: "USD",
	}); err != nil {
		t.Fatalf("SetTenantBudget: %v", err)
	}

	// Comfortably inside the capability ceiling, over the tenant one.
	_, err := usage.Charge(ctx, limes.ChargeRequest{CapabilityID: capID, TenantID: tenantID, Amount: limes.MustParseAmount("50"), MaxBudget: limes.MustParseAmount("1000"), UnitCode: "USD", Op: "op", Actor: "actor"}, nil)
	if !errors.Is(err, limes.ErrTenantBudgetExceeded) {
		t.Fatalf("want ErrTenantBudgetExceeded, got %v", err)
	}

	if _, err := usage.GetUsage(ctx, capID); !errors.Is(err, limes.ErrUsageNotFound) {
		t.Errorf("capability counter was mutated before the tenant check rejected: %v", err)
	}
	b, _ := usage.GetTenantBudget(ctx, tenantID)
	if b.SpentAmount != 0 {
		t.Errorf("tenant spend = %v, want 0", b.SpentAmount)
	}
	if got := len(usage.Ledger()); got != 0 {
		t.Errorf("ledger has %d entries after a rejection, want 0", got)
	}
}

// The no-transaction mode (contracts §1.2): a consumer with no transactional
// storage instantiates UsageStore[struct{}] and always passes nil. Everything
// works; only the atomic-side-effect guarantee is forgone.
func TestNoTransactionModeWorksEndToEnd(t *testing.T) {
	ctx := context.Background()
	capID := uuid.New()
	usage := NewUsage[struct{}](nil)

	if _, err := usage.Bump(ctx, limes.BumpRequest{CapabilityID: capID, MaxRequests: 5}); err != nil {
		t.Fatalf("Bump: %v", err)
	}
	spent, err := usage.Charge(ctx, limes.ChargeRequest{CapabilityID: capID, TenantID: uuid.New(), Amount: limes.MustParseAmount("3"), MaxBudget: limes.MustParseAmount("10"), UnitCode: "", Op: "op", Actor: "actor"}, nil)
	if err != nil {
		t.Fatalf("Charge: %v", err)
	}
	if spent.Spent != 3*unit {
		t.Errorf("spent = %v, want 3", spent.Spent)
	}
	u, err := usage.GetUsage(ctx, capID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if u.RequestCount != 1 {
		t.Errorf("RequestCount = %d, want 1", u.RequestCount)
	}
	// An empty unit code must resolve to the default rather than persisting
	// blank — a bare number with no unit is unrenderable.
	if u.UnitCode != limes.DefaultUnitCode {
		t.Errorf("UnitCode = %q, want the %q default", u.UnitCode, limes.DefaultUnitCode)
	}
}
