package pgstore

import (
	"context"
	"errors"
	"testing"

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
