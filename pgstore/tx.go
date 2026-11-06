package pgstore

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/internal/errs"
	"github.com/oleg-tkachuk/limes/meter"
)

// txKey keys the transaction a charge commits on in the context onCharged
// receives.
type txKey struct{}

// ErrChargeTransaction is onCharged committing or rolling back the charge's
// transaction, which is the store's to end: the charge is committed after
// onCharged returns, on the same transaction.
var ErrChargeTransaction = fmt.Errorf("%w: pgstore: the charge's transaction is the store's to commit or roll back", limes.ErrInvalidRequest)

// TxFrom returns the transaction a charge commits on, from the context
// UsageStore.Charge and UsageStore.Settle hand onCharged: a write through it
// commits with the charge, or with it not at all. Any other context holds
// none, and TxFrom reports false; no other method of this package reads a
// transaction from its context.
//
// The charge's writes are in it already, the ledger row among them, and its
// locks held: every counter on the chain and the tenant's aggregate, which
// every other charge of the tenant waits for until onCharged returns and
// the store commits. onCharged keeps to statements on this database, and
// short ones; a network call there holds the tenant's charges for as long.
//
// The transaction is onCharged's while onCharged runs, and no longer: once
// it returns, every call on it, from any goroutine, fails with
// pgx.ErrTxClosed, and a call still running then is waited for before the
// store commits the charge. Calls are taken one at a time, as a pgx.Tx needs.
// Commit and Rollback return ErrChargeTransaction; a savepoint (Begin) is
// onCharged's to end. Conn is nil, as the connection is the store's, and a
// charge whose transaction onCharged ended with SQL of its own — COMMIT,
// ROLLBACK, or either followed by BEGIN — is refused with
// ErrChargeTransaction and meter.ErrOutcomeUnknown: the charge's writes
// went before onCharged, so that COMMIT committed them, and that ROLLBACK
// undid them.
func TxFrom(ctx context.Context) (pgx.Tx, bool) {
	tx, ok := ctx.Value(txKey{}).(*callbackTx)
	if !ok {
		return nil, false // not a nil *callbackTx, which would read as a Tx
	}
	return tx, true
}

// callbackGuard is what every callbackTx of one onCharged shares: whether
// onCharged has returned, and the lock that takes calls one at a time.
type callbackGuard struct {
	mu     sync.Mutex
	closed bool
}

// close ends the transaction for onCharged, once any call running on it
// has returned.
func (g *callbackGuard) close() {
	g.mu.Lock()
	g.closed = true
	g.mu.Unlock()
}

// run runs call unless onCharged has returned, holding the lock while it
// runs.
func (g *callbackGuard) run(call func() error) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return pgx.ErrTxClosed
	}
	return call()
}

// callbackTx is the charge's transaction, or a savepoint in it, as
// onCharged sees it.
type callbackTx struct {
	tx    pgx.Tx
	guard *callbackGuard
	// savepoint is whether tx is a savepoint onCharged began, which it may
	// end.
	savepoint bool
}

// withTx is ctx carrying tx for one call of onCharged, and the func that
// ends it when onCharged returns.
func withTx(ctx context.Context, tx pgx.Tx) (context.Context, func()) {
	guard := &callbackGuard{}
	return context.WithValue(ctx, txKey{}, &callbackTx{tx: tx, guard: guard}), guard.close
}

// Begin starts a savepoint, which onCharged may commit or roll back.
func (t *callbackTx) Begin(ctx context.Context) (pgx.Tx, error) {
	var nested pgx.Tx
	err := t.guard.run(func() (err error) {
		nested, err = t.tx.Begin(ctx)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &callbackTx{tx: nested, guard: t.guard, savepoint: true}, nil
}

// Commit releases a savepoint; the charge's own transaction is the store's.
func (t *callbackTx) Commit(ctx context.Context) error {
	if !t.savepoint {
		return ErrChargeTransaction
	}
	return t.guard.run(func() error { return t.tx.Commit(ctx) })
}

// Rollback rolls back to a savepoint; the charge's own transaction is the
// store's.
func (t *callbackTx) Rollback(ctx context.Context) error {
	if !t.savepoint {
		return ErrChargeTransaction
	}
	return t.guard.run(func() error { return t.tx.Rollback(ctx) })
}

func (t *callbackTx) CopyFrom(ctx context.Context, table pgx.Identifier, columns []string, rows pgx.CopyFromSource) (int64, error) {
	var n int64
	err := t.guard.run(func() (err error) {
		n, err = t.tx.CopyFrom(ctx, table, columns, rows)
		return err
	})
	return n, err
}

func (t *callbackTx) SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults {
	var results pgx.BatchResults
	if err := t.guard.run(func() error { results = t.tx.SendBatch(ctx, b); return nil }); err != nil {
		return closedBatch{err}
	}
	return results
}

// LargeObjects is the transaction's large objects: the zero LargeObjects,
// which no call can use, once onCharged has returned.
func (t *callbackTx) LargeObjects() pgx.LargeObjects {
	var lo pgx.LargeObjects
	_ = t.guard.run(func() error { lo = t.tx.LargeObjects(); return nil })
	return lo
}

func (t *callbackTx) Prepare(ctx context.Context, name, sql string) (*pgconn.StatementDescription, error) {
	var d *pgconn.StatementDescription
	err := t.guard.run(func() (err error) {
		d, err = t.tx.Prepare(ctx, name, sql)
		return err
	})
	return d, err
}

func (t *callbackTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	var tag pgconn.CommandTag
	err := t.guard.run(func() (err error) {
		tag, err = t.tx.Exec(ctx, sql, args...)
		return err
	})
	return tag, err
}

func (t *callbackTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	var rows pgx.Rows
	err := t.guard.run(func() (err error) {
		rows, err = t.tx.Query(ctx, sql, args...)
		return err
	})
	return rows, err
}

func (t *callbackTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	var row pgx.Row
	if err := t.guard.run(func() error { row = t.tx.QueryRow(ctx, sql, args...); return nil }); err != nil {
		return closedRow{err}
	}
	return row
}

// Conn is nil: the connection is the store's, and through it onCharged could
// end the charge's transaction or run beside it.
func (t *callbackTx) Conn() *pgx.Conn { return nil }

// txStatusInTransaction is the transaction status PostgreSQL reports of a
// connection in an open transaction block (ReadyForQuery 'T').
const txStatusInTransaction = 'T'

// settingCharging marks the transaction a charge's writes went in, for the
// rest of it: a transaction onCharged ended and began again with SQL of its
// own does not carry it.
const settingCharging = "limes.charging"

const (
	// sqlMarkCharge sets settingCharging to $1, local to the transaction,
	// with the charge's writes.
	sqlMarkCharge = `SELECT set_config('` + settingCharging + `', $1, true)`

	// sqlChargeStillOpen fails, dividing by zero, unless the transaction is
	// still the one sqlMarkCharge marked $1: one that does not fail lets the
	// COMMIT queued after it commit the charge, and one that does aborts it.
	// The divisor reads a setting, never folded into a constant, so the
	// statement fails only when it runs.
	sqlChargeStillOpen = `SELECT 1 / (coalesce(current_setting('` + settingCharging + `', true), '') = $1)::int`

	// sqlStateDivisionByZero is the SQLSTATE sqlChargeStillOpen fails with.
	sqlStateDivisionByZero = "22012"
)

// endCharged ends a charge whose writes were sent, marked mark, before
// onCharged ran: it refuses one whose transaction onCharged ended with SQL
// of its own — COMMIT, ROLLBACK, or either followed by BEGIN in the same
// statement — rather than through the Tx, which refuses, and otherwise
// sends COMMIT when last, in one batch with the check (sqlChargeStillOpen).
//
// The charge's writes went before onCharged, so its COMMIT committed them
// and its ROLLBACK undid them, under locks it no longer held: which one ran
// is not known to the store, and the call fails as meter.ErrOutcomeUnknown
// too. One that left the transaction failed, without ending it, committed
// nothing.
func endCharged(ctx context.Context, tx pgx.Tx, mark string, last bool) error {
	switch status := tx.Conn().PgConn().TxStatus(); status {
	case txStatusInTransaction:
	case txStatusIdle:
		return errs.Errorf("%w: %w: onCharged ended it", meter.ErrOutcomeUnknown, ErrChargeTransaction)
	default:
		return fmt.Errorf("%w: onCharged left it failed (status %q)", ErrChargeTransaction, status)
	}
	b := &pgx.Batch{}
	b.Queue(sqlChargeStillOpen, mark)
	committing := last && queueCommit(tx, b)
	br := tx.SendBatch(ctx, b)
	_, err := br.Exec()
	if pgErr := (*pgconn.PgError)(nil); errors.As(err, &pgErr) && pgErr.Code == sqlStateDivisionByZero {
		_ = br.Close() // its error is the check's, answered: COMMIT did not run
		return errs.Errorf("%w: %w: onCharged ended it and began another", meter.ErrOutcomeUnknown, ErrChargeTransaction)
	}
	err = errors.Join(err, br.Close())
	if committing {
		return commitOutcome(tx, err)
	}
	return err
}

// closedRow is a QueryRow on a transaction onCharged no longer holds.
type closedRow struct{ err error }

func (r closedRow) Scan(...any) error { return r.err }

// closedBatch is a SendBatch on a transaction onCharged no longer holds.
type closedBatch struct{ err error }

func (b closedBatch) Exec() (pgconn.CommandTag, error) { return pgconn.CommandTag{}, b.err }
func (b closedBatch) Query() (pgx.Rows, error)         { return nil, b.err }
func (b closedBatch) QueryRow() pgx.Row                { return closedRow(b) }
func (b closedBatch) Close() error                     { return b.err }

// Tx is nil: the batch never ran, so it began no transaction.
func (b closedBatch) Tx() pgx.Tx { return nil }

var _ pgx.Tx = (*callbackTx)(nil)
