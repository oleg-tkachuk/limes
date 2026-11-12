package pgstore

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/internal/errs"
	"github.com/oleg-tkachuk/limes/meter"
)

// txKey keys the transaction a charge or a refund commits on in the context
// its callback receives.
type txKey struct{}

// ErrChargeTransaction is a callback — onCharged, onRefunded — committing or
// rolling back the transaction it runs on, which is the store's to end: the
// charge or the refund is committed after the callback returns, on the same
// transaction.
var ErrChargeTransaction = fmt.Errorf("%w: pgstore: the transaction a callback runs on is the store's to commit or roll back", limes.ErrInvalidRequest)

// TxFrom returns the transaction a charge or a refund commits on, from the
// context UsageStore.Charge and UsageStore.Settle hand onCharged, and
// UsageStore.Refund onRefunded: a write through it commits with the charge
// or the refund, or with it not at all. Any other context holds none, and
// TxFrom reports false; no other method of this package reads a transaction
// from its context.
//
// The call's writes are in it already — a charge's ledger row, a refund's
// refunded amount on the charge and, under a RefundID, its limes.refunds
// row — and its locks held: every counter on the chain and the tenant's
// aggregate, which every other charge of the tenant waits for until the
// callback returns and the store commits. The callback keeps to statements
// on this database, and short ones; a network call there holds the
// tenant's charges for as long.
//
// The transaction is the callback's while it runs, and no longer: once it
// returns, every call on it, from any goroutine, fails with
// pgx.ErrTxClosed, and a call still running then is waited for before the
// store commits. Calls are taken one at a time, as a pgx.Tx needs, and
// so are the reads of the rows and batch results it hands out: those still
// open when the callback returns are closed then, and read no further. A
// batch's QueuedQuery callbacks run while its Close holds the transaction:
// a call they make on it, or one from another goroutine meanwhile, is
// refused with ErrChargeTransaction.
// Commit and Rollback return ErrChargeTransaction; a savepoint (Begin) is
// the callback's to end. Conn is nil, as the connection is the store's, and
// a call whose transaction the callback ended with SQL of its own — COMMIT,
// ROLLBACK, or either followed by BEGIN — is refused with
// ErrChargeTransaction and meter.ErrOutcomeUnknown: the call's writes went
// before the callback, so that COMMIT committed them, and that ROLLBACK
// undid them.
func TxFrom(ctx context.Context) (pgx.Tx, bool) {
	tx, ok := ctx.Value(txKey{}).(*callbackTx)
	if !ok {
		return nil, false // not a nil *callbackTx, which would read as a Tx
	}
	return tx, true
}

// callbackGuard is what every callbackTx of one callback shares: whether
// the callback has returned, the lock that takes calls one at a time —
// reading rows and batch results among them — and the results still open.
type callbackGuard struct {
	mu     sync.Mutex
	closed bool
	// open closes each result handed out, rows and batches, latest first.
	open []func()
	// inBatchCallback is set while a batch's QueuedQuery callback runs,
	// inside the batch's Close, which holds mu: a call it made would wait
	// on mu for ever.
	inBatchCallback atomic.Bool
}

// errBatchCallbackCall is a call on the transaction from a batch's
// QueuedQuery callback, refused rather than waited for.
var errBatchCallbackCall = fmt.Errorf("%w: pgstore: a batch's QueuedQuery callback may not call the transaction its batch is read on", ErrChargeTransaction)

// enter takes the lock for a call, unless a batch's callback, which runs
// holding it, is what makes the call.
func (g *callbackGuard) enter() error {
	if g.inBatchCallback.Load() {
		return errBatchCallbackCall
	}
	g.mu.Lock()
	return nil
}

// inBatch marks each of b's QueuedQuery callbacks as running inside its
// batch's Close (inBatchCallback).
func (g *callbackGuard) inBatch(b *pgx.Batch) {
	for _, q := range b.QueuedQueries {
		if fn := q.Fn; fn != nil {
			q.Fn = func(br pgx.BatchResults) error {
				g.inBatchCallback.Store(true)
				defer g.inBatchCallback.Store(false)
				return fn(br)
			}
		}
	}
}

// close ends the transaction for the callback, once any call running on it
// has returned: it closes every result the callback left open, so the
// connection is free for the store's COMMIT, and refuses every call after.
func (g *callbackGuard) close() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.closed = true
	for _, closeOpen := range slices.Backward(g.open) {
		closeOpen()
	}
	g.open = nil
}

// run runs call unless the callback has returned, holding the lock while it
// runs.
func (g *callbackGuard) run(call func() error) error {
	if err := g.enter(); err != nil {
		return err
	}
	defer g.mu.Unlock()
	if g.closed {
		return pgx.ErrTxClosed
	}
	return call()
}

// callbackTx is the call's transaction, or a savepoint in it, as the
// callback sees it.
type callbackTx struct {
	tx    pgx.Tx
	guard *callbackGuard
	// savepoint is whether tx is a savepoint the callback began, which it
	// may end.
	savepoint bool
}

// withTx is ctx carrying tx for one run of a callback, and the func that
// ends it when the callback returns.
func withTx(ctx context.Context, tx pgx.Tx) (context.Context, func()) {
	guard := &callbackGuard{}
	return context.WithValue(ctx, txKey{}, &callbackTx{tx: tx, guard: guard}), guard.close
}

// Begin starts a savepoint, which the callback may commit or roll back.
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

// Commit releases a savepoint; the call's own transaction is the store's.
func (t *callbackTx) Commit(ctx context.Context) error {
	if !t.savepoint {
		return ErrChargeTransaction
	}
	return t.guard.run(func() error { return t.tx.Commit(ctx) })
}

// Rollback rolls back to a savepoint; the call's own transaction is the
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
	if err := t.guard.run(func() error {
		t.guard.inBatch(b)
		results = t.guard.batch(t.tx.SendBatch(ctx, b))
		return nil
	}); err != nil {
		return closedBatch{err}
	}
	return results
}

// LargeObjects is the transaction's large objects: the zero LargeObjects,
// which no call can use, once the callback has returned.
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
	err := t.guard.run(func() error {
		r, err := t.tx.Query(ctx, sql, args...)
		if err != nil {
			return err
		}
		rows = t.guard.rows(r)
		return nil
	})
	return rows, err
}

// QueryRow is Query's first row, read when the callback scans it: the rows
// stay the guard's until then, as pgx's own QueryRow defers its error.
func (t *callbackTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	rows, err := t.Query(ctx, sql, args...)
	if err != nil {
		return closedRow{err}
	}
	return firstRow{rows}
}

// Conn is nil: the connection is the store's, and through it the callback
// could end the call's transaction or run beside it.
func (t *callbackTx) Conn() *pgx.Conn { return nil }

// txStatusInTransaction is the transaction status PostgreSQL reports of a
// connection in an open transaction block (ReadyForQuery 'T').
const txStatusInTransaction = 'T'

// settingCharging marks the transaction a charge's or a refund's writes
// went in, for the rest of it: a transaction the callback ended and began
// again with SQL of its own does not carry it.
const settingCharging = "limes.charging"

const (
	// sqlMarkCharge sets settingCharging to $1, local to the transaction,
	// with the call's writes.
	sqlMarkCharge = `SELECT set_config('` + settingCharging + `', $1, true)`

	// sqlChargeStillOpen fails, dividing by zero, unless the transaction is
	// still the one sqlMarkCharge marked $1: one that does not fail lets the
	// COMMIT queued after it commit the call, and one that does aborts it.
	// The divisor reads a setting, never folded into a constant, so the
	// statement fails only when it runs.
	sqlChargeStillOpen = `SELECT 1 / (coalesce(current_setting('` + settingCharging + `', true), '') = $1)::int`

	// sqlStateDivisionByZero is the SQLSTATE sqlChargeStillOpen fails with.
	sqlStateDivisionByZero = "22012"
)

// callback is the caller's callback over a call's writes — onCharged or
// onRefunded, bound to the receipt it is handed.
type callback func(ctx context.Context) error

// writeThenCall writes c and then, and runs call, if any, once they are
// sent, so that what call writes sees them. Without call the writes carry
// COMMIT when last (writeLast). With it they go without, marked mark as the
// transaction's (sqlMarkCharge); COMMIT goes after call, in one batch with
// the check that call left the transaction open (endCallback): one round
// trip more. The order shortens no lock: the counters and the tenant's
// aggregate are locked before the writes and held to COMMIT either way.
func (c *counters) writeThenCall(ctx context.Context, tx pgx.Tx, now time.Time, writeTenant, last bool, mark string, call callback, then []heldStmt) error {
	if call == nil {
		return c.writeBatch(ctx, tx, now, writeTenant, last, then)
	}
	then = append(then, heldStmt{sql: sqlMarkCharge, args: []any{mark}})
	if err := c.writeBatch(ctx, tx, now, writeTenant, false, then); err != nil {
		return err
	}
	callbackCtx, end := withTx(ctx, tx)
	err := func() error {
		defer end()
		return call(callbackCtx)
	}()
	if err != nil {
		return err
	}
	return endCallback(ctx, tx, mark, last)
}

// endCallback ends a call whose writes were sent, marked mark, before its
// callback ran: it refuses one whose transaction the callback ended with SQL
// of its own — COMMIT, ROLLBACK, or either followed by BEGIN in the same
// statement — rather than through the Tx, which refuses, and otherwise
// sends COMMIT when last, in one batch with the check (sqlChargeStillOpen).
//
// The call's writes went before the callback, so its COMMIT committed them
// and its ROLLBACK undid them, under locks it no longer held: which one ran
// is not known to the store, and the call fails as meter.ErrOutcomeUnknown
// too. One that left the transaction failed, without ending it, committed
// nothing.
func endCallback(ctx context.Context, tx pgx.Tx, mark string, last bool) error {
	switch status := tx.Conn().PgConn().TxStatus(); status {
	case txStatusInTransaction:
	case txStatusIdle:
		return errs.Errorf("%w: %w: the callback ended it", meter.ErrOutcomeUnknown, ErrChargeTransaction)
	default:
		return fmt.Errorf("%w: the callback left it failed (status %q)", ErrChargeTransaction, status)
	}
	b := &pgx.Batch{}
	b.Queue(sqlChargeStillOpen, mark)
	committing := last && queueCommit(tx, b)
	br := tx.SendBatch(ctx, b)
	_, err := br.Exec()
	if pgErr := (*pgconn.PgError)(nil); errors.As(err, &pgErr) && pgErr.Code == sqlStateDivisionByZero {
		_ = br.Close() // its error is the check's, answered: COMMIT did not run
		return errs.Errorf("%w: %w: the callback ended it and began another", meter.ErrOutcomeUnknown, ErrChargeTransaction)
	}
	err = errors.Join(err, br.Close())
	if committing {
		return commitOutcome(tx, err)
	}
	return err
}

// closedRow is a QueryRow on a transaction the callback no longer holds.
type closedRow struct{ err error }

func (r closedRow) Scan(...any) error { return r.err }

// closedBatch is a SendBatch on a transaction the callback no longer holds.
type closedBatch struct{ err error }

func (b closedBatch) Exec() (pgconn.CommandTag, error) { return pgconn.CommandTag{}, b.err }
func (b closedBatch) Query() (pgx.Rows, error)         { return nil, b.err }
func (b closedBatch) QueryRow() pgx.Row                { return closedRow(b) }
func (b closedBatch) Close() error                     { return b.err }

// Tx is nil: the batch never ran, so it began no transaction.
func (b closedBatch) Tx() pgx.Tx { return nil }

var _ pgx.Tx = (*callbackTx)(nil)
