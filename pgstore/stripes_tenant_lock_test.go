package pgstore

import (
	"context"
	"errors"
	"testing"
	"time"

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

// endingCalls are the calls that end what a Reserve or a Charge of subject
// made, each given what its setup returns: a hold for Settle and Release, a
// charge for Refund.
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

// Settle, Release and Refund share their tenant's lock, as a Charge does,
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
			onDone := func(context.Context) error {
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
