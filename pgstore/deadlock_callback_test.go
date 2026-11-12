package pgstore

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/oleg-tkachuk/limes/meter"
)

// A deadlock onCharged meets outside the charge's transaction — on another
// connection of its own — and returns, is the callback's failure, not the
// charge's: the transaction it runs on was not aborted, so the store does
// not run the charge and its callback again beside effects that stand. A
// deadlock of the charge's own transaction is run again (retryDeadlock).
func TestADeadlockOutsideTheChargeIsNotRetried(t *testing.T) {
	f := newTenantFixture(t)
	ctx := WithTenant(t.Context(), f.mine)
	elsewhere := &pgconn.PgError{Code: pgerrcode.DeadlockDetected}
	calls := 0
	onCharged := func(context.Context, meter.Charged) error {
		calls++
		return elsewhere
	}
	_, err := f.usage.Charge(ctx, meter.ChargeRequest{Subject: meterSubject(f.child.ID, f.mine), Amount: 1}, onCharged)
	if !errors.Is(err, elsewhere) {
		t.Errorf("a charge whose onCharged returned another connection's deadlock: err = %v, want it", err)
	}
	if calls != 1 {
		t.Errorf("onCharged ran %d times, want once", calls)
	}
	if u, err := f.usage.GetUsage(ctx, f.child.ID); err == nil && u.SpentAmount != 0 {
		t.Errorf("a charge whose onCharged failed spent %v", u.SpentAmount)
	}
}
