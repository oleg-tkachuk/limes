package pgstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/meter"
)

// sqlFirstCharge stands in for a first charge in flight: it creates the
// tenant's aggregate with what it counted, as lock and commitCharge do.
const sqlFirstCharge = `
INSERT INTO ` + schema + `.tenant_budgets (tenant_id, ceiling_max, ceiling_unlimited, spent, reserved, unit_code, period_start, updated_at)
VALUES ($1, 0, true, $2, $3, $4, now(), now())`

// sqlWaiting counts the sessions waiting on a lock.
const sqlWaiting = `SELECT count(*) FROM pg_locks WHERE NOT granted`

// lockWaitTimeout bounds how long the test waits for SetTenantBudget to block
// behind the first charge.
const lockWaitTimeout = 10 * time.Second

// lockWaitPoll is how often the test looks for that wait.
const lockWaitPoll = 10 * time.Millisecond

// The first charge's counts and unit, and the ceiling set alongside it.
const (
	raceSpent    = 5
	raceReserved = 3
	raceCeiling  = 10
	raceUnit     = "USD"
	otherUnit    = "EUR"
)

// setBudgetDuringFirstCharge runs SetTenantBudget for a tenant with no
// aggregate yet while a first charge that creates one is in flight, lets the
// charge commit once SetTenantBudget waits behind it, and returns what
// SetTenantBudget answered.
func setBudgetDuringFirstCharge(t *testing.T, usage *UsageStore, tenant uuid.UUID, unit string) error {
	t.Helper()
	ctx := context.Background()
	first, err := owner.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Rollback(ctx) }()
	if _, err := first.Exec(ctx, sqlFirstCharge, tenant, raceSpent, raceReserved, raceUnit); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := usage.SetTenantBudget(WithTenant(ctx, tenant), meter.SetTenantBudgetRequest{
			TenantID: tenant, Ceiling: limes.BudgetCeiling{Max: raceCeiling, UnitCode: unit},
		})
		done <- err
	}()
	deadline := time.Now().Add(lockWaitTimeout)
	for {
		var waiting int
		if err := owner.QueryRow(ctx, sqlWaiting).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("SetTenantBudget never waited for the first charge")
		}
		time.Sleep(lockWaitPoll)
	}
	if err := first.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return <-done
}

// The first ceiling set for a tenant is measured against what a first charge
// committed meanwhile counted: SetTenantBudget waits for that charge and
// keeps its spend, its hold and its unit, rather than writing a fresh
// aggregate over them.
func TestSetTenantBudgetKeepsAConcurrentFirstCharge(t *testing.T) {
	fresh(t)
	usage, err := NewUsageStore(Config{DB: app})
	if err != nil {
		t.Fatal(err)
	}
	tenant := uuid.New()
	if err := setBudgetDuringFirstCharge(t, usage, tenant, raceUnit); err != nil {
		t.Fatalf("SetTenantBudget: %v", err)
	}
	b, err := usage.GetTenantBudget(WithTenant(context.Background(), tenant), tenant)
	if err != nil {
		t.Fatal(err)
	}
	if b.SpentAmount != raceSpent || b.ReservedAmount != raceReserved || b.Ceiling.Max != raceCeiling {
		t.Errorf("tenant after the race = spent %v, reserved %v, ceiling %v; want %d, %d, %d",
			b.SpentAmount, b.ReservedAmount, b.Ceiling.Max, raceSpent, raceReserved, raceCeiling)
	}

	// Counted in USD with 3 still held, the tenant cannot be moved to EUR.
	other := uuid.New()
	if err := setBudgetDuringFirstCharge(t, usage, other, otherUnit); !errors.Is(err, limes.ErrUnitCodeMismatch) {
		t.Fatalf("SetTenantBudget in another unit: err = %v, want ErrUnitCodeMismatch", err)
	}
}
