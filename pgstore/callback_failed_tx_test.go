package pgstore

import (
	"context"
	"errors"
	"testing"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/meter"
)

// The SQL a callback ends the call's transaction with, begins another with,
// and then fails in: the new transaction is left failed ('E'), and the
// call's writes committed with the COMMIT before it.
var committingThenAborting = map[string]string{
	"COMMIT, BEGIN, a failing statement": `COMMIT; BEGIN; SELECT 1/0`,
	"COMMIT, BEGIN, a deadlock":          `COMMIT; BEGIN; DO $$ BEGIN RAISE EXCEPTION USING ERRCODE = '40P01'; END $$`,
}

// failingInTheCall is SQL a callback fails in on the call's own
// transaction, which leaves it failed and commits nothing.
const failingInTheCall = `SELECT 1/0`

// What a callback returns after its SQL: the error that SQL met, or nil.
const (
	returnsItsError = "its error"
	returnsNil      = "nil"
)

// callbackCall is one store call that runs a callback over its writes,
// with the callback it is handed.
type callbackCall struct {
	name string
	run  func(t *testing.T, f tenantFixture, ctx context.Context, cb func(context.Context) error) error
}

// callbackCalls are the calls a callback runs in: every route into
// callbackFailure and endCallback.
var callbackCalls = []callbackCall{
	{"a charge", func(_ *testing.T, f tenantFixture, ctx context.Context, cb func(context.Context) error) error {
		_, err := f.usage.Charge(ctx, meter.ChargeRequest{Subject: meterSubject(f.child.ID, f.mine), Amount: 1}, func(ctx context.Context, _ meter.Charged) error { return cb(ctx) })
		return err
	}},
	{"a bump", func(_ *testing.T, f tenantFixture, ctx context.Context, cb func(context.Context) error) error {
		_, err := f.usage.Bump(ctx, meter.BumpRequest{Subject: meterSubject(f.child.ID, f.mine)}, func(ctx context.Context, _ meter.Bumped) error { return cb(ctx) })
		return err
	}},
	{"a settle", func(t *testing.T, f tenantFixture, ctx context.Context, cb func(context.Context) error) error {
		subject := meterSubject(f.child.ID, f.mine)
		r, err := f.usage.Reserve(ctx, meter.ReserveRequest{Subject: subject, Amount: 1})
		if err != nil {
			t.Fatalf("Reserve: %v", err)
		}
		_, err = f.usage.Settle(ctx, meter.SettleRequest{Subject: subject, ReservationID: r.ID, Amount: 1}, func(ctx context.Context, _ meter.Charged) error { return cb(ctx) })
		return err
	}},
	{"a refund", func(t *testing.T, f tenantFixture, ctx context.Context, cb func(context.Context) error) error {
		c, err := f.usage.Charge(ctx, meter.ChargeRequest{Subject: meterSubject(f.child.ID, f.mine), Amount: 1}, nil)
		if err != nil {
			t.Fatalf("Charge: %v", err)
		}
		_, err = f.usage.Refund(ctx, meter.RefundRequest{ChargeID: c.ChargeID, Remaining: true}, func(ctx context.Context, _ meter.Refunded) error { return cb(ctx) })
		return err
	}},
	{"an erase", func(_ *testing.T, f tenantFixture, ctx context.Context, cb func(context.Context) error) error {
		_, err := f.records.EraseTenant(ctx, limes.EraseTenantRequest{TenantID: f.mine}, func(ctx context.Context, _ Erased) error { return cb(ctx) })
		return err
	}},
}

// checkOutcomeUnknown fails unless err is ErrChargeTransaction beside
// meter.ErrOutcomeUnknown, and the callback ran once: the call's writes
// committed, so neither the caller nor the store may run it as not counted.
func checkOutcomeUnknown(t *testing.T, call string, err error, runs int) {
	t.Helper()
	if !errors.Is(err, ErrChargeTransaction) || !errors.Is(err, meter.ErrOutcomeUnknown) {
		t.Errorf("%s whose callback committed it: err = %v, want ErrChargeTransaction and ErrOutcomeUnknown", call, err)
	}
	if runs != 1 {
		t.Errorf("%s ran its callback %d times, want once: a committed call is not run again", call, runs)
	}
}

// A callback that commits the call's transaction, begins another and leaves
// it failed has every call fail as outcome unknown, run once, whether it
// returns the failure or nil: the failed transaction is not the call's.
func TestACallbackLeavingAFailedTransactionAfterItsCommitIsOutcomeUnknown(t *testing.T) {
	for name, sql := range committingThenAborting {
		for _, returned := range []string{returnsItsError, returnsNil} {
			for _, c := range callbackCalls {
				t.Run(name+"/"+returned+"/"+c.name, func(t *testing.T) {
					f := newTenantFixture(t)
					ctx := WithTenant(t.Context(), f.mine)
					runs := 0
					err := c.run(t, f, ctx, func(ctx context.Context) error {
						runs++
						tx, _ := TxFrom(ctx)
						_, err := tx.Exec(ctx, sql)
						if returned == returnsNil {
							return nil
						}
						return err
					})
					checkOutcomeUnknown(t, c.name, err, runs)
				})
			}
		}
	}
}

// A callback that commits the call's transaction and begins another, then
// fails after its caller gave up, has the call fail as outcome unknown: the
// check of whose transaction it is runs past the caller's cancellation.
func TestACallbackCommittingThenCancelledIsOutcomeUnknown(t *testing.T) {
	for _, c := range callbackCalls {
		t.Run(c.name, func(t *testing.T) {
			f := newTenantFixture(t)
			ctx, cancel := context.WithCancel(WithTenant(t.Context(), f.mine))
			defer cancel()
			runs := 0
			err := c.run(t, f, ctx, func(ctx context.Context) error {
				runs++
				tx, _ := TxFrom(ctx)
				if _, err := tx.Exec(ctx, `COMMIT; BEGIN`); err != nil {
					return err
				}
				cancel()
				return errConsumer
			})
			checkOutcomeUnknown(t, c.name, err, runs)
		})
	}
}

// A callback whose own statement fails in the call's transaction leaves it
// failed with nothing committed: the call fails with that error alone, not
// as outcome unknown, and counts nothing.
func TestACallbackFailingInTheCallsOwnTransactionCountsNothing(t *testing.T) {
	failInTheCall := func(ctx context.Context) error {
		tx, _ := TxFrom(ctx)
		_, err := tx.Exec(ctx, failingInTheCall)
		return err
	}
	for _, c := range callbackCalls {
		t.Run(c.name, func(t *testing.T) {
			f := newTenantFixture(t)
			ctx := WithTenant(t.Context(), f.mine)
			err := c.run(t, f, ctx, failInTheCall)
			if err == nil || errors.Is(err, meter.ErrOutcomeUnknown) {
				t.Errorf("%s whose callback failed in its transaction: err = %v, want the failure alone", c.name, err)
			}
			if c.name != "a charge" {
				return
			}
			if u, err := f.usage.GetUsage(ctx, f.child.ID); err == nil && u.SpentAmount != 0 {
				t.Errorf("a charge whose callback failed in its transaction spent %v", u.SpentAmount)
			}
		})
	}
}
