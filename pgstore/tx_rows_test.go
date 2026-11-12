package pgstore

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/oleg-tkachuk/limes/meter"
)

// sqlSeries is a query of several rows, so rows left unread hold the
// connection.
const sqlSeries = `SELECT generate_series(1, 3)`

// The rows and batch results the transaction hands onCharged are its while
// it runs and no longer: one left open is closed when it returns, so the
// charge commits; read past it, it is a closed transaction; and none hands
// out the store's connection.
func TestTxFromClosesWhatTheCallbackLeftOpen(t *testing.T) {
	type opened struct {
		rows  pgx.Rows
		row   pgx.Row
		batch pgx.BatchResults
	}
	cases := map[string]func(ctx context.Context, tx pgx.Tx, o *opened) error{
		"rows": func(ctx context.Context, tx pgx.Tx, o *opened) (err error) {
			if o.rows, err = tx.Query(ctx, sqlSeries); err != nil {
				return err
			}
			if !o.rows.Next() {
				t.Error("the first row of a query in onCharged was not read")
			}
			if o.rows.Conn() != nil {
				t.Error("Conn of rows in onCharged is not nil")
			}
			return nil
		},
		"a row not scanned": func(ctx context.Context, tx pgx.Tx, o *opened) error {
			o.row = tx.QueryRow(ctx, sqlSeries)
			return nil
		},
		"a batch and its rows": func(ctx context.Context, tx pgx.Tx, o *opened) (err error) {
			b := &pgx.Batch{}
			b.Queue(sqlSeries)
			b.Queue(sqlSeries)
			o.batch = tx.SendBatch(ctx, b)
			o.rows, err = o.batch.Query()
			return err
		},
	}
	for name, open := range cases {
		t.Run(name, func(t *testing.T) {
			f := newTenantFixture(t)
			ctx := WithTenant(t.Context(), f.mine)
			var o opened
			onCharged := func(ctx context.Context, _ meter.Charged) error {
				tx, _ := TxFrom(ctx)
				return open(ctx, tx, &o)
			}
			if _, err := f.usage.Charge(ctx, meter.ChargeRequest{Subject: meterSubject(f.child.ID, f.mine), Amount: 1}, onCharged); err != nil {
				t.Fatalf("a charge whose onCharged left %s open: %v", name, err)
			}
			if u, err := f.usage.GetUsage(ctx, f.child.ID); err != nil || u.SpentAmount != 1 {
				t.Errorf("usage after the charge = %+v, %v; want it written", u, err)
			}
			if o.rows != nil {
				if o.rows.Next() || !errors.Is(o.rows.Err(), pgx.ErrTxClosed) {
					t.Errorf("rows after onCharged: Next read on, or Err = %v; want pgx.ErrTxClosed", o.rows.Err())
				}
				if o.rows.Conn() != nil {
					t.Error("Conn of rows after onCharged is not nil")
				}
			}
			if o.row != nil {
				var n int
				if err := o.row.Scan(&n); !errors.Is(err, pgx.ErrTxClosed) {
					t.Errorf("Scan of a row after onCharged: err = %v, want pgx.ErrTxClosed", err)
				}
			}
			if o.batch != nil {
				if _, err := o.batch.Exec(); !errors.Is(err, pgx.ErrTxClosed) {
					t.Errorf("Exec of a batch after onCharged: err = %v, want pgx.ErrTxClosed", err)
				}
				if o.batch.Tx() != nil {
					t.Error("Tx of a batch in onCharged is not nil")
				}
			}
		})
	}
}

// A row scanned in onCharged reads as pgx's own does: its first value, and
// pgx.ErrNoRows for none.
func TestTxFromQueryRowReadsTheFirstRow(t *testing.T) {
	f := newTenantFixture(t)
	ctx := WithTenant(t.Context(), f.mine)
	onCharged := func(ctx context.Context, _ meter.Charged) error {
		tx, _ := TxFrom(ctx)
		var n int
		if err := tx.QueryRow(ctx, sqlSeries).Scan(&n); err != nil || n != 1 {
			t.Errorf("QueryRow of a series = %d, %v; want 1", n, err)
		}
		if err := tx.QueryRow(ctx, sqlNoRows).Scan(&n); !errors.Is(err, pgx.ErrNoRows) {
			t.Errorf("QueryRow of no rows: err = %v, want pgx.ErrNoRows", err)
		}
		return nil
	}
	if _, err := f.usage.Charge(ctx, meter.ChargeRequest{Subject: meterSubject(f.child.ID, f.mine), Amount: 1}, onCharged); err != nil {
		t.Fatal(err)
	}
}

// sqlNoRows is a query of no rows.
const sqlNoRows = `SELECT 1 WHERE false`

// Rows read from another goroutine while onCharged returns are read one call
// at a time with the store's own, never beside them: read whole, or refused
// as closed.
func TestTxFromWaitsForRowsReadInFlight(t *testing.T) {
	f := newTenantFixture(t)
	ctx := WithTenant(t.Context(), f.mine)
	started, done := make(chan struct{}), make(chan error, 1)
	onCharged := func(ctx context.Context, _ meter.Charged) error {
		tx, _ := TxFrom(ctx)
		rows, err := tx.Query(ctx, slowStatement)
		if err != nil {
			return err
		}
		go func() {
			defer rows.Close()
			close(started)
			for rows.Next() {
			}
			done <- rows.Err()
		}()
		<-started
		return nil
	}
	if _, err := f.usage.Charge(ctx, meter.ChargeRequest{Subject: meterSubject(f.child.ID, f.mine), Amount: 1}, onCharged); err != nil {
		t.Fatalf("a charge beside rows read in flight: %v", err)
	}
	if err := <-done; err != nil && !errors.Is(err, pgx.ErrTxClosed) {
		t.Errorf("the rows in flight: err = %v, want them read whole or refused as closed", err)
	}
}

// A batch's QueuedQuery callback runs while its Close holds the transaction:
// a call it makes on the transaction is refused, never waited for, so the
// charge is not stuck holding the tenant's locks.
func TestTxFromRefusesACallFromABatchCallback(t *testing.T) {
	f := newTenantFixture(t)
	ctx := WithTenant(t.Context(), f.mine)
	var called error
	onCharged := func(ctx context.Context, _ meter.Charged) error {
		tx, _ := TxFrom(ctx)
		b := &pgx.Batch{}
		b.Queue(sqlOne).Exec(func(pgconn.CommandTag) error {
			_, called = tx.Exec(ctx, sqlOne)
			return nil
		})
		return tx.SendBatch(ctx, b).Close()
	}
	if _, err := f.usage.Charge(ctx, meter.ChargeRequest{Subject: meterSubject(f.child.ID, f.mine), Amount: 1}, onCharged); err != nil {
		t.Fatalf("a charge whose batch callback called the transaction: %v", err)
	}
	if !errors.Is(called, ErrChargeTransaction) {
		t.Errorf("a call from a batch callback: err = %v, want ErrChargeTransaction", called)
	}
}
