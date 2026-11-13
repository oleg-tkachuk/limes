package pgstore

import (
	"context"
	"database/sql/driver"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/oleg-tkachuk/limes/meter"
)

// reentryWait bounds how long a call that re-enters the transaction may
// take: one that waits on itself would take for ever.
const reentryWait = 10 * time.Second

// sqlCopyTable is a table onCharged copies rows into, its own for the
// transaction.
const (
	sqlCreateCopyTable = `CREATE TEMP TABLE reentry_copy (n int) ON COMMIT DROP`
	copyTable          = "reentry_copy"
	copyColumn         = "n"
)

// sqlEcho reads its argument back.
const sqlEcho = `SELECT $1::int`

// reentering calls the transaction from inside a call on it, recording
// what that inner call answered.
type reentering struct {
	ctx context.Context
	tx  pgx.Tx
	err error
}

func (r *reentering) call() error {
	_, r.err = r.tx.Exec(r.ctx, sqlOne)
	return r.err
}

// reenteringScanner is a sql.Scanner that calls the transaction.
type reenteringScanner struct{ *reentering }

func (s reenteringScanner) Scan(any) error { return s.call() }

// reenteringValuer is a driver.Valuer that calls the transaction.
type reenteringValuer struct{ *reentering }

func (v reenteringValuer) Value() (driver.Value, error) {
	if err := v.call(); err != nil {
		return nil, err
	}
	return int64(1), nil
}

// reenteringSource is a pgx.CopyFromSource of one row that calls the
// transaction for it.
type reenteringSource struct {
	*reentering
	sent bool
}

func (s *reenteringSource) Next() bool {
	if s.sent {
		return false
	}
	s.sent = true
	return s.call() == nil
}

func (s *reenteringSource) Values() ([]any, error) { return []any{1}, nil }
func (s *reenteringSource) Err() error             { return s.err }

// A sql.Scanner, driver.Valuer or pgx.CopyFromSource that calls the
// transaction while the call that runs it holds it is refused with
// ErrChargeTransaction, never left waiting on itself: the outer call fails,
// and the charge with it, its locks let go.
func TestTxFromRefusesACallFromInsideACall(t *testing.T) {
	cases := map[string]func(ctx context.Context, tx pgx.Tx, r *reentering) error{
		"a Scanner": func(ctx context.Context, tx pgx.Tx, r *reentering) error {
			return tx.QueryRow(ctx, sqlOne).Scan(reenteringScanner{r})
		},
		"a Valuer": func(ctx context.Context, tx pgx.Tx, r *reentering) error {
			_, err := tx.Exec(ctx, sqlEcho, reenteringValuer{r})
			return err
		},
		"a CopyFromSource": func(ctx context.Context, tx pgx.Tx, r *reentering) error {
			if _, err := tx.Exec(ctx, sqlCreateCopyTable); err != nil {
				return err
			}
			_, err := tx.CopyFrom(ctx, pgx.Identifier{copyTable}, []string{copyColumn}, &reenteringSource{reentering: r})
			return err
		},
	}
	for name, outer := range cases {
		t.Run(name, func(t *testing.T) {
			f := newTenantFixture(t)
			ctx := WithTenant(t.Context(), f.mine)
			var inner reentering
			onCharged := func(ctx context.Context, _ meter.Charged) error {
				tx, _ := TxFrom(ctx)
				inner = reentering{ctx: ctx, tx: tx}
				return outer(ctx, tx, &inner)
			}
			done := make(chan error, 1)
			go func() {
				_, err := f.usage.Charge(ctx, meter.ChargeRequest{Subject: meterSubject(f.child.ID, f.mine), Amount: 1}, onCharged)
				done <- err
			}()
			var err error
			select {
			case err = <-done:
			case <-time.After(reentryWait):
				t.Fatalf("a charge whose onCharged called the transaction from %s did not return", name)
			}
			if !errors.Is(inner.err, ErrChargeTransaction) {
				t.Errorf("the call from %s: err = %v, want ErrChargeTransaction", name, inner.err)
			}
			if !errors.Is(err, ErrChargeTransaction) {
				t.Errorf("the charge: err = %v, want the refusal onCharged returned", err)
			}
		})
	}
}

// A call on the transaction while another runs on it, from another
// goroutine, is refused with ErrChargeTransaction rather than waited for, as
// pgx refuses a busy connection; the call that runs is not disturbed.
func TestTxFromRefusesACallBesideAnother(t *testing.T) {
	f := newTenantFixture(t)
	ctx := WithTenant(t.Context(), f.mine)
	var beside, slow error
	onCharged := func(ctx context.Context, _ meter.Charged) error {
		tx, _ := TxFrom(ctx)
		started, done := make(chan struct{}), make(chan struct{})
		go func() {
			defer close(done)
			close(started)
			_, slow = tx.Exec(ctx, slowStatement)
		}()
		<-started
		// Until the slow statement holds the transaction, a call may still
		// run: it is retried until one is refused or the slow one ends.
		for {
			_, beside = tx.Exec(ctx, sqlOne)
			if beside != nil {
				break
			}
			select {
			case <-done:
				t.Fatal("no call was made while the slow statement ran")
			default:
			}
		}
		<-done
		return slow
	}
	if _, err := f.usage.Charge(ctx, meter.ChargeRequest{Subject: meterSubject(f.child.ID, f.mine), Amount: 1}, onCharged); err != nil {
		t.Fatalf("a charge whose onCharged called beside a call, or the call it ran: %v", err)
	}
	if !errors.Is(beside, ErrChargeTransaction) {
		t.Errorf("a call beside another: err = %v, want ErrChargeTransaction", beside)
	}
}

// The transaction lends no large objects: pgx.LargeObjects is bound to the
// transaction it was made on, so one kept past the callback would run beside
// the store's COMMIT. It is the zero LargeObjects.
func TestTxFromLendsNoLargeObjects(t *testing.T) {
	f := newTenantFixture(t)
	ctx := WithTenant(t.Context(), f.mine)
	onCharged := func(ctx context.Context, _ meter.Charged) error {
		tx, _ := TxFrom(ctx)
		lo := tx.LargeObjects()
		if lo != (pgx.LargeObjects{}) {
			t.Error("LargeObjects in onCharged is not the zero LargeObjects")
		}
		if !panics(func() { _, _ = lo.Create(ctx, 0) }) {
			t.Error("a call on the LargeObjects onCharged gets did not panic")
		}
		return nil
	}
	if _, err := f.usage.Charge(ctx, meter.ChargeRequest{Subject: meterSubject(f.child.ID, f.mine), Amount: 1}, onCharged); err != nil {
		t.Fatal(err)
	}
}

// A batch's QueuedQuery callback reads rows whose Conn is nil, as the
// transaction's are: the connection is the store's, and through it the
// callback could run beside the store's own statements.
func TestTxFromBatchCallbackRowsLendNoConn(t *testing.T) {
	f := newTenantFixture(t)
	ctx := WithTenant(t.Context(), f.mine)
	var called bool
	var conn *pgx.Conn
	onCharged := func(ctx context.Context, _ meter.Charged) error {
		tx, _ := TxFrom(ctx)
		b := &pgx.Batch{}
		b.Queue(sqlOne).Query(func(rows pgx.Rows) error {
			called, conn = true, rows.Conn()
			return nil
		})
		return tx.SendBatch(ctx, b).Close()
	}
	if _, err := f.usage.Charge(ctx, meter.ChargeRequest{Subject: meterSubject(f.child.ID, f.mine), Amount: 1}, onCharged); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("the QueuedQuery callback did not run")
	}
	if conn != nil {
		t.Error("a QueuedQuery callback's rows lent the store's connection")
	}
}

// panics reports whether f panics.
func panics(f func()) (panicked bool) {
	defer func() { panicked = recover() != nil }()
	f()
	return false
}
