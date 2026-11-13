package pgstore

import (
	"context"
	"errors"
	"testing"
	"time"
	"uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/meter"
)

// heldLockWait is how long a call is given to finish while another
// transaction holds its tenant's lock alone: long enough that a call that
// does not wait for the lock has returned by then.
const heldLockWait = 300 * time.Millisecond

// resetWait is how long a callback waits for a reset it started: a reset
// that waits on the call's own rows past it would wait for ever.
const resetWait = 5 * time.Second

// resetCeiling is the ceiling a period reset sets on a tenant that had none.
const resetCeiling limes.Nanos = 1000

// errResetStuck is a reset that did not return while the call that started
// it waited.
var errResetStuck = errors.New("the reset did not return within resetWait")

// endingCalls are the calls that end what a Reserve, a Lease or a Charge of
// subject made, each given what its setup returns: a hold for Settle and
// Release, a lease for SettleLease, a charge for Refund.
var endingCalls = map[string]struct {
	setup func(ctx context.Context, usage *UsageStore, subject limes.MeterSubject) (any, error)
	call  func(ctx context.Context, usage *UsageStore, subject limes.MeterSubject, made any, onDone func(context.Context) error) error
}{
	"Settle": {
		setup: func(ctx context.Context, usage *UsageStore, subject limes.MeterSubject) (any, error) {
			return usage.Reserve(ctx, meter.ReserveRequest{Subject: subject, Amount: stripedAmount})
		},
		call: func(ctx context.Context, usage *UsageStore, subject limes.MeterSubject, made any, onDone func(context.Context) error) error {
			var onCharged func(context.Context, meter.Charged) error
			if onDone != nil {
				onCharged = func(ctx context.Context, _ meter.Charged) error { return onDone(ctx) }
			}
			_, err := usage.Settle(ctx, meter.SettleRequest{Subject: subject, ReservationID: made.(meter.Reservation).ID, Amount: stripedAmount}, onCharged)
			return err
		},
	},
	"Release": {
		setup: func(ctx context.Context, usage *UsageStore, subject limes.MeterSubject) (any, error) {
			return usage.Reserve(ctx, meter.ReserveRequest{Subject: subject, Amount: stripedAmount})
		},
		call: func(ctx context.Context, usage *UsageStore, subject limes.MeterSubject, made any, _ func(context.Context) error) error {
			return usage.Release(ctx, meter.ReleaseRequest{Subject: subject, ReservationID: made.(meter.Reservation).ID})
		},
	},
	"SettleLease": {
		setup: func(ctx context.Context, usage *UsageStore, subject limes.MeterSubject) (any, error) {
			return usage.Lease(ctx, meter.LeaseRequest{Subject: subject, Amount: stripedAmount})
		},
		call: func(ctx context.Context, usage *UsageStore, subject limes.MeterSubject, _ any, _ func(context.Context) error) error {
			_, err := usage.SettleLease(ctx, meter.SettleLeaseRequest{Subject: subject})
			return err
		},
	},
	"Refund": {
		setup: func(ctx context.Context, usage *UsageStore, subject limes.MeterSubject) (any, error) {
			return usage.Charge(ctx, meter.ChargeRequest{Subject: subject, Amount: stripedAmount}, nil)
		},
		call: func(ctx context.Context, usage *UsageStore, subject limes.MeterSubject, made any, onDone func(context.Context) error) error {
			var onRefunded func(context.Context, meter.Refunded) error
			if onDone != nil {
				onRefunded = func(ctx context.Context, _ meter.Refunded) error { return onDone(ctx) }
			}
			_, err := usage.Refund(ctx, meter.RefundRequest{ChargeID: made.(meter.ChargeReceipt).ChargeID, Amount: stripedAmount}, onRefunded)
			return err
		},
	},
}

// Settle, Release, SettleLease and Refund share their tenant's lock, as a Charge does,
// so a call that holds it alone — SetTenantBudget, an erase — is waited
// for, and waits for them.
func TestEndingCallsShareTheTenantLock(t *testing.T) {
	for name, ending := range endingCalls {
		t.Run(name, func(t *testing.T) {
			fresh(t)
			usage := stripedStore(t, Config{})
			subject := leaseholderOf(t)
			tenant := subject.TenantID()
			ctx := WithTenant(t.Context(), tenant)
			made, err := ending.setup(ctx, usage, subject)
			if err != nil {
				t.Fatal(err)
			}

			alone, err := owner.Begin(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = alone.Rollback(context.Background()) }()
			if _, err := alone.Exec(context.Background(), sqlTakeTenantLock, eraseLockClass, tenant); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- ending.call(ctx, usage, subject, made, nil) }()
			select {
			case err := <-done:
				t.Fatalf("%s returned while its tenant's lock was held alone: %v", name, err)
			case <-time.After(heldLockWait):
			}
			if err := alone.Rollback(context.Background()); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err != nil {
				t.Fatalf("%s once the lock was let go: %v", name, err)
			}
		})
	}
}

// A period reset of a tenant with no ceiling that comes while a Settle or a
// Refund of it runs its callback waits for it, and gives up as busy: it
// does not close the period under a stripe the call has yet to commit, which
// would leave the tenant's stripes of another period than its aggregate and
// fail every later call of the tenant.
func TestResetDuringAnEndingCallLeavesNoStaleStripe(t *testing.T) {
	for _, name := range []string{"Settle", "Refund"} {
		ending := endingCalls[name]
		t.Run(name, func(t *testing.T) {
			fresh(t)
			usage := stripedStore(t, Config{TenantBudgetLockTimeout: busyLockTimeout})
			subject := leaseholderOf(t)
			tenant := subject.TenantID()
			ctx := WithTenant(t.Context(), tenant)
			made, err := ending.setup(ctx, usage, subject)
			if err != nil {
				t.Fatal(err)
			}
			// The stripes the setup wrote are folded, so the call's stripe is
			// one it inserts, which a fold does not see until it commits.
			folded, err := usage.SetTenantBudget(ctx, meter.SetTenantBudgetRequest{TenantID: tenant, Ceiling: limes.BudgetCeiling{Unlimited: true, UnitCode: testUnit}})
			if err != nil {
				t.Fatal(err)
			}
			reset := meter.SetTenantBudgetRequest{
				TenantID: tenant, ResetSpend: true, ExpectedVersion: folded.ResourceVersion,
				Ceiling: limes.BudgetCeiling{Max: resetCeiling, UnitCode: testUnit},
			}
			var resetErr error
			resetDone := make(chan error, 1)
			// The store runs the callback again after a deadlock: the
			// reset is sent from its first run alone.
			var sent bool
			onDone := func(context.Context) error {
				if sent {
					return nil
				}
				sent = true
				// From a goroutine of its own, as an operator's reset comes:
				// the call's transaction is left open meanwhile.
				go func() {
					_, err := usage.SetTenantBudget(ctx, reset)
					resetDone <- err
				}()
				select {
				case resetErr = <-resetDone:
					resetDone = nil
				case <-time.After(resetWait):
					resetErr = errResetStuck
				}
				return nil
			}
			err = ending.call(ctx, usage, subject, made, onDone)
			if resetDone != nil {
				if late := <-resetDone; late != nil {
					t.Logf("the reset, once %s ended: %v", name, late)
				}
			}
			if err != nil {
				t.Fatalf("%s whose callback ran a reset: %v", name, err)
			}
			if !errors.Is(resetErr, meter.ErrTenantBudgetBusy) {
				t.Errorf("a reset during %s's callback = %v, want ErrTenantBudgetBusy", name, resetErr)
			}
			if _, err := usage.Charge(ctx, meter.ChargeRequest{Subject: subject, Amount: 1}, nil); err != nil {
				t.Errorf("a Charge after %s: %v", name, err)
			}
			if _, err := usage.SetTenantBudget(ctx, reset); err != nil {
				t.Errorf("a reset after %s: %v", name, err)
			}
		})
	}
}

// advisoryWaitPoll and advisoryWaitLimit pace waitAdvisoryWaiters.
const (
	advisoryWaitPoll  = 20 * time.Millisecond
	advisoryWaitLimit = 10 * time.Second
)

// sqlAdvisoryWaiters counts the advisory lock requests that wait.
const sqlAdvisoryWaiters = `SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND NOT granted`

// waitAdvisoryWaiters waits until n advisory lock requests wait.
func waitAdvisoryWaiters(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(advisoryWaitLimit)
	for time.Now().Before(deadline) {
		var waiting int
		if err := owner.QueryRow(context.Background(), sqlAdvisoryWaiters).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting >= n {
			return
		}
		time.Sleep(advisoryWaitPoll)
	}
	t.Fatalf("fewer than %d advisory lock requests waited", n)
}

// tenantAloneCalls are the calls that hold a tenant's erase lock alone and
// then lock its rows.
var tenantAloneCalls = map[string]func(ctx context.Context, usage *UsageStore, tenant uuid.UUID) error{
	"EraseTenantUsage": func(ctx context.Context, usage *UsageStore, tenant uuid.UUID) error {
		_, err := usage.EraseTenantUsage(ctx, meter.EraseTenantUsageRequest{TenantID: tenant})
		return err
	},
	"SetTenantBudget": func(ctx context.Context, usage *UsageStore, tenant uuid.UUID) error {
		_, err := usage.SetTenantBudget(ctx, meter.SetTenantBudgetRequest{TenantID: tenant, Ceiling: limes.BudgetCeiling{Max: resetCeiling, UnitCode: testUnit}})
		return err
	},
}

// DeleteUsage of a leaseholder shares its tenant's lock before it locks a
// row, as every call that settles a lease does: queued behind a call that
// holds the lock alone and then locks the same rows, both finish, and
// neither is chosen as a deadlock's victim.
func TestDeleteUsageOfALeaseholderBesideACallHoldingItsTenantAlone(t *testing.T) {
	for name, alone := range tenantAloneCalls {
		t.Run(name, func(t *testing.T) {
			fresh(t)
			usage := stripedStore(t, Config{})
			subject := leaseholderOf(t)
			tenant := subject.TenantID()
			ctx := WithTenant(t.Context(), tenant)
			if _, err := usage.Lease(ctx, meter.LeaseRequest{Subject: subject, Amount: stripedAmount}); err != nil {
				t.Fatal(err)
			}
			// A call of the tenant in flight shares its lock, as a Charge
			// does, so the call holding it alone and the drop queue behind.
			inFlight, err := owner.Begin(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = inFlight.Rollback(context.Background()) }()
			if _, err := inFlight.Exec(context.Background(), sqlShareTenantLock, eraseLockClass, tenant); err != nil {
				t.Fatal(err)
			}
			aloneDone := make(chan error, 1)
			go func() { aloneDone <- alone(ctx, usage, tenant) }()
			waitAdvisoryWaiters(t, 1)
			dropDone := make(chan error, 1)
			go func() {
				dropDone <- usage.DeleteUsage(ctx, meter.DeleteUsageRequest{CapabilityID: subject.CapabilityID()})
			}()
			waitAdvisoryWaiters(t, 2)
			if err := inFlight.Rollback(context.Background()); err != nil {
				t.Fatal(err)
			}
			if err := <-aloneDone; err != nil {
				t.Errorf("%s: %v", name, err)
			}
			if err := <-dropDone; err != nil {
				t.Errorf("DeleteUsage queued behind %s: %v", name, err)
			}
		})
	}
}
