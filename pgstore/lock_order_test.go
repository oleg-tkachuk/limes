package pgstore

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5"
	"github.com/oleg-tkachuk/limes/meter"
)

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
	charged, err := f.usage.Charge(mine, meter.ChargeRequest{Subject: meterSubject(f.child.ID, f.mine), Amount: amount}, nil)
	if err != nil {
		t.Fatal(err)
	}
	erase := otherSide(t)
	if _, err := erase.Exec(context.Background(), `SELECT 1 FROM `+schema+`.tenant_budgets WHERE tenant_id = $1 FOR UPDATE`, f.mine); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := f.usage.Refund(mine, meter.RefundRequest{ChargeID: charged.ChargeID, Remaining: true})
		done <- err
	}()
	awaitLockWait(t)
	if _, err := erase.Exec(context.Background(), `DELETE FROM `+schema+`.charges WHERE tenant_id = $1`, f.mine); err != nil {
		t.Fatalf("the erase deleting the charges: %v", err)
	}
	if err := erase.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := <-done; isDeadlock(err) || !errors.Is(err, meter.ErrChargeNotFound) {
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
	held, err := f.usage.Reserve(mine, meter.ReserveRequest{Subject: meterSubject(f.child.ID, f.mine), Amount: 1})
	if err != nil {
		t.Fatal(err)
	}
	release := otherSide(t)
	if _, err := release.Exec(context.Background(), `SELECT 1 FROM `+schema+`.reservations WHERE id = $1 FOR UPDATE`, held.ID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- f.usage.DeleteUsage(mine, meter.DeleteUsageRequest{CapabilityID: f.parent.ID})
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
		r, err := f.usage.Reserve(mine, meter.ReserveRequest{Subject: meterSubject(f.child.ID, f.mine), Amount: 1})
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
		_, err := f.usage.ReleaseExpired(WithAllTenants(context.Background()), meter.ReleaseExpiredRequest{})
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

// A call locks a tenant's aggregate after the counters and copies it moves
// (docs/adr/0018-fewer-round-trips-per-charge.md): one that holds a counter
// and then locks the aggregate does not meet a Charge, a Reserve or a
// Release holding the aggregate and waiting for the counter.
func TestTheTenantsAggregateIsLockedLast(t *testing.T) {
	for name, call := range map[string]func(f tenantFixture, ctx context.Context, hold uuid.UUID) error{
		"Charge": func(f tenantFixture, ctx context.Context, hold uuid.UUID) error {
			_, err := f.usage.Charge(ctx, meter.ChargeRequest{Subject: meterSubject(f.child.ID, f.mine), Amount: 1}, nil)
			return err
		},
		"Reserve": func(f tenantFixture, ctx context.Context, hold uuid.UUID) error {
			_, err := f.usage.Reserve(ctx, meter.ReserveRequest{Subject: meterSubject(f.child.ID, f.mine), Amount: 1})
			return err
		},
		"Release": func(f tenantFixture, ctx context.Context, hold uuid.UUID) error {
			return f.usage.Release(ctx, meter.ReleaseRequest{Subject: meterSubject(f.child.ID, f.mine), ReservationID: hold})
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newTenantFixture(t)
			mine := WithTenant(context.Background(), f.mine)
			// The counters and the aggregate exist before the race: the
			// call locks rows, not their absence.
			if _, err := f.usage.Charge(mine, meter.ChargeRequest{Subject: meterSubject(f.child.ID, f.mine), Amount: 1}, nil); err != nil {
				t.Fatal(err)
			}
			held, err := f.usage.Reserve(mine, meter.ReserveRequest{Subject: meterSubject(f.child.ID, f.mine), Amount: 1})
			if err != nil {
				t.Fatal(err)
			}
			other := otherSide(t)
			if _, err := other.Exec(context.Background(), `SELECT 1 FROM `+schema+`.usage WHERE capability_id = $1 FOR UPDATE`, f.child.ID); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- call(f, mine, held.ID) }()
			awaitLockWait(t)
			if _, err := other.Exec(context.Background(), `SELECT 1 FROM `+schema+`.tenant_budgets WHERE tenant_id = $1 FOR UPDATE`, f.mine); err != nil {
				t.Fatalf("the other call locking the aggregate after the counter: %v", err)
			}
			if err := other.Commit(context.Background()); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err != nil {
				t.Errorf("%s once the other call committed: %v", name, err)
			}
		})
	}
}

// logRecords is a slog handler that keeps what it is handed.
type logRecords struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *logRecords) Enabled(context.Context, slog.Level) bool { return true }
func (h *logRecords) WithAttrs([]slog.Attr) slog.Handler       { return h }
func (h *logRecords) WithGroup(string) slog.Handler            { return h }
func (h *logRecords) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r)
	return nil
}

// A Charge that PostgreSQL aborts to break a deadlock with a call of the
// order before — the tenant's aggregate first, then a counter — is run
// again once that call is done, and says so: what a rolling upgrade from
// that order meets.
func TestAChargeDeadlockedByTheOldOrderIsRetried(t *testing.T) {
	f := newTenantFixture(t)
	mine := WithTenant(context.Background(), f.mine)
	subject := meterSubject(f.child.ID, f.mine)
	if _, err := f.usage.Charge(mine, meter.ChargeRequest{Subject: subject, Amount: 1}, nil); err != nil {
		t.Fatal(err)
	}
	logged := &logRecords{}
	usage, err := NewUsageStore(Config{DB: app, Now: f.clock.Now, Logger: slog.New(logged)})
	if err != nil {
		t.Fatal(err)
	}
	old := otherSide(t)
	if _, err := old.Exec(context.Background(), `SELECT 1 FROM `+schema+`.tenant_budgets WHERE tenant_id = $1 FOR UPDATE`, f.mine); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := usage.Charge(mine, meter.ChargeRequest{Subject: subject, Amount: 1}, nil)
		done <- err
	}()
	awaitLockWait(t)
	if _, err := old.Exec(context.Background(), `SELECT 1 FROM `+schema+`.usage WHERE capability_id = $1 FOR UPDATE`, f.child.ID); err != nil {
		t.Fatalf("the old order's call locking the counter: %v", err)
	}
	if err := old.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("Charge deadlocked by the old order: %v, want it retried", err)
	}
	logged.mu.Lock()
	defer logged.mu.Unlock()
	if len(logged.records) != 1 || logged.records[0].Level != slog.LevelWarn {
		t.Errorf("logged %d records, want one warning of the retry", len(logged.records))
	}
}
