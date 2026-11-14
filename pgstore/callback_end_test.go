package pgstore

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/meter"
)

// The SQL a callback ends the call's transaction with before it fails: the
// call's writes committed with it.
var committingTheCall = map[string]string{
	"COMMIT":        `COMMIT`,
	"COMMIT, BEGIN": `COMMIT; BEGIN`,
}

// checkEndedThenFailed fails unless err is the callback's own error beside
// ErrChargeTransaction and meter.ErrOutcomeUnknown: the call's writes may
// have committed, so the caller must not read it as nothing counted.
func checkEndedThenFailed(t *testing.T, call string, err error) {
	t.Helper()
	if !errors.Is(err, errConsumer) || !errors.Is(err, ErrChargeTransaction) || !errors.Is(err, meter.ErrOutcomeUnknown) {
		t.Errorf("%s whose callback committed and then failed: err = %v, want its error, ErrChargeTransaction and ErrOutcomeUnknown", call, err)
	}
}

// A callback that commits the call's transaction with SQL of its own and
// then fails has the call fail as outcome unknown, beside its error, for a
// charge, a bump and an erase alike.
func TestACallbackCommittingThenFailingIsOutcomeUnknown(t *testing.T) {
	for name, sql := range committingTheCall {
		t.Run(name, func(t *testing.T) {
			f := newTenantFixture(t)
			ctx := WithTenant(t.Context(), f.mine)
			commitThenFail := func(ctx context.Context) error {
				tx, _ := TxFrom(ctx)
				if _, err := tx.Exec(ctx, sql); err != nil {
					return err
				}
				return errConsumer
			}
			subject := meterSubject(f.child.ID, f.mine)
			_, err := f.usage.Charge(ctx, meter.ChargeRequest{Subject: subject, Amount: 1}, func(ctx context.Context, _ meter.Charged) error { return commitThenFail(ctx) })
			checkEndedThenFailed(t, "a charge", err)
			_, err = f.usage.Bump(ctx, meter.BumpRequest{Subject: subject}, func(ctx context.Context, _ meter.Bumped) error { return commitThenFail(ctx) })
			checkEndedThenFailed(t, "a bump", err)
			_, err = f.records.EraseTenant(ctx, limes.EraseTenantRequest{TenantID: f.mine}, func(ctx context.Context, _ Erased) error { return commitThenFail(ctx) })
			checkEndedThenFailed(t, "an erase", err)
		})
	}
}

// A callback that fails with the call's transaction still its own fails the
// call with its error alone: nothing counted, which the caller may send again.
func TestACallbackFailingInTheCallsTransactionCountsNothing(t *testing.T) {
	f := newTenantFixture(t)
	ctx := WithTenant(t.Context(), f.mine)
	_, err := f.usage.Bump(ctx, meter.BumpRequest{Subject: meterSubject(f.child.ID, f.mine)}, func(context.Context, meter.Bumped) error { return errConsumer })
	if !errors.Is(err, errConsumer) || errors.Is(err, meter.ErrOutcomeUnknown) {
		t.Fatalf("a bump whose callback failed: err = %v, want its error alone", err)
	}
	if u, err := f.usage.GetUsage(ctx, f.child.ID); err == nil && u.RequestCount != 0 {
		t.Errorf("a failed callback counted %d requests", u.RequestCount)
	}
}

// Erased.Tx is the transaction TxFrom lends: an onErased cannot end the
// erase through it, nor reach the connection, and a role it set goes with
// the refused erase rather than staying on a pooled connection.
func TestErasedTxIsTheStoresToEnd(t *testing.T) {
	f := newTenantFixture(t)
	ctx := WithTenant(t.Context(), f.mine)
	var commitErr, rollbackErr error
	var conn *pgx.Conn
	var lent bool
	_, err := f.records.EraseTenant(ctx, limes.EraseTenantRequest{TenantID: f.mine}, func(ctx context.Context, e Erased) error {
		_, lent = TxFrom(ctx)
		conn = e.Tx.Conn()
		if _, err := e.Tx.Exec(ctx, sqlSetRole); err != nil {
			return err
		}
		commitErr, rollbackErr = e.Tx.Commit(ctx), e.Tx.Rollback(ctx)
		return nil
	})
	if !errors.Is(commitErr, ErrChargeTransaction) || !errors.Is(rollbackErr, ErrChargeTransaction) {
		t.Errorf("Erased.Tx Commit = %v, Rollback = %v; want ErrChargeTransaction", commitErr, rollbackErr)
	}
	if conn != nil || !lent {
		t.Errorf("Erased.Tx.Conn() = %v, TxFrom in onErased = %v; want nil and a transaction", conn, lent)
	}
	if !errors.Is(err, ErrChargeTransaction) || errors.Is(err, meter.ErrOutcomeUnknown) {
		t.Errorf("an erase whose onErased set the role: err = %v, want ErrChargeTransaction, the outcome known", err)
	}
	checkPoolSetsNoRole(t, "onErased SET ROLE and Erased.Tx.Commit")
}

// A lent connection committed with the session under another role than a
// callback found is closed, as one rolled back is, not given back to the
// pool under it.
func TestACommitUnderACallbacksRoleClosesTheConnection(t *testing.T) {
	f := newTenantFixture(t)
	ctx := WithTenant(t.Context(), f.mine)
	_ = f.records.inTenantTx(ctx, f.mine, func(tx pgx.Tx) error {
		if err := sendWrites(ctx, tx, []heldStmt{markCallback(tx, f.mine.String())}, false); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, sqlSetRole); err != nil {
			return err
		}
		return tx.Commit(ctx)
	})
	checkPoolSetsNoRole(t, "a commit under a callback's role")
}
