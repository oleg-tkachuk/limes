package pgstore

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/oleg-tkachuk/limes"
)

// sqlDeadlockDetected is PostgreSQL's code for a transaction it aborted to
// break a cycle of waits.
const sqlDeadlockDetected = "40P01"

// isDeadlock reports whether err is PostgreSQL breaking a deadlock.
func isDeadlock(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == sqlDeadlockDetected
}

// awaitLockWait returns once some session waits on a lock: the call under
// test has taken what it takes first and is blocked behind the other side.
func awaitLockWait(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(lockWaitTimeout)
	for {
		var waiting int
		if err := owner.QueryRow(context.Background(), sqlWaiting).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the call never waited on the other side's lock")
		}
		time.Sleep(lockWaitPoll)
	}
}

// otherSide is a transaction, as the owner, standing in for another call
// that takes its locks in the documented order.
func otherSide(t *testing.T) pgx.Tx {
	t.Helper()
	tx, err := owner.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	return tx
}

// Refund locks the ledger after the counters, as an erase does: an erase
// that holds the tenant's aggregate and then deletes its charges does not
// meet a Refund holding the charge and waiting for the aggregate.
func TestRefundLocksTheLedgerLast(t *testing.T) {
	const amount = 5
	f := newTenantFixture(t)
	mine := WithTenant(context.Background(), f.mine)
	charged, err := f.usage.Charge(mine, limes.ChargeRequest{Subject: meterSubject(f.child.ID, f.mine), Amount: amount}, nil)
	if err != nil {
		t.Fatal(err)
	}
	erase := otherSide(t)
	if _, err := erase.Exec(context.Background(), `SELECT 1 FROM `+schema+`.tenant_budgets WHERE tenant_id = $1 FOR UPDATE`, f.mine); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := f.usage.Refund(mine, limes.RefundRequest{ChargeID: charged.ChargeID, Remaining: true})
		done <- err
	}()
	awaitLockWait(t)
	if _, err := erase.Exec(context.Background(), `DELETE FROM `+schema+`.charges WHERE tenant_id = $1`, f.mine); err != nil {
		t.Fatalf("the erase deleting the charges: %v", err)
	}
	if err := erase.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := <-done; isDeadlock(err) || !errors.Is(err, limes.ErrChargeNotFound) {
		t.Errorf("Refund of a charge erased meanwhile: err = %v, want ErrChargeNotFound", err)
	}
}

// Dropping a capability's counters locks every hold that names it before a
// counter: a Release that holds a descendant's hold and then locks the
// counters it names does not meet DeleteUsage holding those counters and
// waiting for the hold.
func TestDeleteUsageLocksTheHoldsFirst(t *testing.T) {
	f := newTenantFixture(t)
	mine := WithTenant(context.Background(), f.mine)
	held, err := f.usage.Reserve(mine, limes.ReserveRequest{Subject: meterSubject(f.child.ID, f.mine), Amount: 1})
	if err != nil {
		t.Fatal(err)
	}
	release := otherSide(t)
	if _, err := release.Exec(context.Background(), `SELECT 1 FROM `+schema+`.reservations WHERE id = $1 FOR UPDATE`, held.ID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- f.usage.DeleteUsage(mine, limes.DeleteUsageRequest{CapabilityID: f.parent.ID})
	}()
	awaitLockWait(t)
	if _, err := release.Exec(context.Background(), `SELECT 1 FROM `+schema+`.usage WHERE capability_id = $1 FOR UPDATE`, f.parent.ID); err != nil {
		t.Fatalf("the release locking the parent's counters: %v", err)
	}
	if err := release.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Errorf("DeleteUsage of the parent: %v", err)
	}
}

// ReleaseExpired locks the holds it releases in id order, as every other
// call that locks several does: it does not meet one holding the hold of
// lower id and waiting for the other.
func TestReleaseExpiredLocksHoldsInIDOrder(t *testing.T) {
	f := newTenantFixture(t)
	mine := WithTenant(context.Background(), f.mine)
	var ids [2]uuid.UUID
	for i := range ids {
		r, err := f.usage.Reserve(mine, limes.ReserveRequest{Subject: meterSubject(f.child.ID, f.mine), Amount: 1})
		if err != nil {
			t.Fatal(err)
		}
		ids[i] = r.ID
	}
	if bytes.Compare(ids[0][:], ids[1][:]) > 0 {
		ids[0], ids[1] = ids[1], ids[0]
	}
	// Both are due, the one of lower id last: expiry order is not id order.
	past := f.clock.Now().Add(-time.Minute)
	for i, id := range ids {
		if _, err := owner.Exec(context.Background(), `UPDATE `+schema+`.reservations SET expires_at = $2 WHERE id = $1`, id, past.Add(-time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	other := otherSide(t)
	if _, err := other.Exec(context.Background(), `SELECT 1 FROM `+schema+`.reservations WHERE id = $1 FOR UPDATE`, ids[0]); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := f.usage.ReleaseExpired(WithAllTenants(context.Background()), limes.ReleaseExpiredRequest{})
		done <- err
	}()
	awaitLockWait(t)
	if _, err := other.Exec(context.Background(), `SELECT 1 FROM `+schema+`.reservations WHERE id = $1 FOR UPDATE`, ids[1]); err != nil {
		t.Fatalf("the other call locking the second hold: %v", err)
	}
	if err := other.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Errorf("ReleaseExpired: %v", err)
	}
}
