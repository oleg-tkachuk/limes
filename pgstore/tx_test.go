package pgstore

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/oleg-tkachuk/limes/meter"
)

// lockUsageNoWait locks the counter row of a capability, failing at once
// rather than waiting when another transaction holds it.
const (
	lockUsageNoWait = `SELECT capability_id FROM ` + schema + `.usage WHERE capability_id = $1 FOR UPDATE NOWAIT`
	// lockNotAvailable is the SQLSTATE of a NOWAIT lock another transaction holds.
	lockNotAvailable = "55P03"
)

// The transaction onCharged reads with TxFrom is the one the charge commits
// on: it already holds the counter row the charge locked, which another
// connection cannot take.
func TestTxFromIsTheChargesTransaction(t *testing.T) {
	f := newTenantFixture(t)
	ctx := WithTenant(t.Context(), f.mine)
	subject := meterSubject(f.child.ID, f.mine)
	// a committed counter row, so another connection sees the charge's lock
	// on it rather than no row at all
	if _, err := f.usage.Bump(ctx, meter.BumpRequest{Subject: subject}); err != nil {
		t.Fatal(err)
	}
	ran := false
	onCharged := func(ctx context.Context, _ meter.Charged) error {
		ran = true
		tx, ok := TxFrom(ctx)
		if !ok {
			t.Fatal("onCharged's context holds no transaction")
		}
		if tag, err := tx.Exec(ctx, lockUsageNoWait, f.child.ID); err != nil || tag.RowsAffected() != 1 {
			t.Errorf("the counter row through TxFrom: %v, %v; want it held already", tag, err)
		}
		var pgErr *pgconn.PgError
		if _, err := owner.Exec(ctx, lockUsageNoWait, f.child.ID); !errors.As(err, &pgErr) || pgErr.Code != lockNotAvailable {
			t.Errorf("the counter row from another connection: err = %v, want it locked by the charge", err)
		}
		return nil
	}
	if _, err := f.usage.Charge(ctx, meter.ChargeRequest{Subject: subject, Amount: 1}, onCharged); err != nil {
		t.Fatal(err)
	}
	if !ran {
		t.Fatal("onCharged did not run")
	}
}

// A context no charge handed on holds no transaction.
func TestTxFromAnotherContext(t *testing.T) {
	if tx, ok := TxFrom(t.Context()); ok || tx != nil {
		t.Errorf("TxFrom(a plain context) = %v, %v; want none", tx, ok)
	}
}

// sqlOne is a statement any transaction can run.
const sqlOne = `SELECT 1`

// The transaction is onCharged's while it runs and no longer: kept past it,
// every call fails as a closed transaction, and the charge is written all
// the same.
func TestTxFromEndsWithTheCallback(t *testing.T) {
	f := newTenantFixture(t)
	ctx := WithTenant(t.Context(), f.mine)
	var kept pgx.Tx
	onCharged := func(ctx context.Context, _ meter.Charged) error {
		kept, _ = TxFrom(ctx)
		_, err := kept.Exec(ctx, sqlOne)
		return err
	}
	if _, err := f.usage.Charge(ctx, meter.ChargeRequest{Subject: meterSubject(f.child.ID, f.mine), Amount: 1}, onCharged); err != nil {
		t.Fatal(err)
	}
	if _, err := kept.Exec(ctx, sqlOne); !errors.Is(err, pgx.ErrTxClosed) {
		t.Errorf("Exec after onCharged: err = %v, want pgx.ErrTxClosed", err)
	}
	var one int
	if err := kept.QueryRow(ctx, sqlOne).Scan(&one); !errors.Is(err, pgx.ErrTxClosed) {
		t.Errorf("QueryRow after onCharged: err = %v, want pgx.ErrTxClosed", err)
	}
	if _, err := kept.Begin(ctx); !errors.Is(err, pgx.ErrTxClosed) {
		t.Errorf("Begin after onCharged: err = %v, want pgx.ErrTxClosed", err)
	}
	if kept.Conn() != nil {
		t.Error("Conn after onCharged is not nil")
	}
}

// onCharged may not end the charge's transaction, but may end a savepoint
// of its own: a rolled-back savepoint undoes its writes alone, and the
// charge commits.
func TestTxFromKeepsTheTransactionTheStores(t *testing.T) {
	f := newTenantFixture(t)
	ctx := WithTenant(t.Context(), f.mine)
	onCharged := func(ctx context.Context, _ meter.Charged) error {
		tx, _ := TxFrom(ctx)
		if err := tx.Commit(ctx); !errors.Is(err, ErrChargeTransaction) {
			t.Errorf("Commit in onCharged: err = %v, want ErrChargeTransaction", err)
		}
		if err := tx.Rollback(ctx); !errors.Is(err, ErrChargeTransaction) {
			t.Errorf("Rollback in onCharged: err = %v, want ErrChargeTransaction", err)
		}
		sp, err := tx.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err := sp.Exec(ctx, sqlOne); err != nil {
			return err
		}
		return sp.Rollback(ctx)
	}
	subject := meterSubject(f.child.ID, f.mine)
	if _, err := f.usage.Charge(ctx, meter.ChargeRequest{Subject: subject, Amount: 1}, onCharged); err != nil {
		t.Fatalf("a charge whose onCharged tried to end its transaction: %v", err)
	}
	if u, err := f.usage.GetUsage(ctx, f.child.ID); err != nil || u.SpentAmount != 1 {
		t.Errorf("usage after the charge = %+v, %v; want it written", u, err)
	}
}

// slowStatement holds the connection long enough for the charge to go on
// while it runs, were it not waited for.
const slowStatement = `SELECT pg_sleep(0.2)`

// A call from another goroutine still running when onCharged returns is
// waited for before the store writes the charge, not run beside it.
func TestTxFromWaitsForACallInFlight(t *testing.T) {
	f := newTenantFixture(t)
	ctx := WithTenant(t.Context(), f.mine)
	started, done := make(chan struct{}), make(chan error, 1)
	onCharged := func(ctx context.Context, _ meter.Charged) error {
		tx, _ := TxFrom(ctx)
		go func() {
			close(started)
			_, err := tx.Exec(ctx, slowStatement)
			done <- err
		}()
		<-started
		return nil
	}
	if _, err := f.usage.Charge(ctx, meter.ChargeRequest{Subject: meterSubject(f.child.ID, f.mine), Amount: 1}, onCharged); err != nil {
		t.Fatalf("a charge beside a call in flight: %v", err)
	}
	if err := <-done; err != nil && !errors.Is(err, pgx.ErrTxClosed) {
		t.Errorf("the call in flight: err = %v, want it run whole or refused as closed", err)
	}
}
