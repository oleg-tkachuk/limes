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

// txKey keys the transaction a charge, a bump or a refund commits on in the
// context its callback receives.
type txKey struct{}

// ErrChargeTransaction is a callback — onCharged, onBumped, onRefunded —
// committing or rolling back the transaction it runs on, which is the
// store's to end: the charge, the bump or the refund is committed after the
// callback returns, on the same transaction.
var ErrChargeTransaction = fmt.Errorf("%w: pgstore: the transaction a callback runs on is the store's to commit or roll back", limes.ErrInvalidRequest)

// TxFrom returns the transaction a charge, a bump or a refund commits on,
// from the context UsageStore.Charge and UsageStore.Settle hand onCharged,
// UsageStore.Bump onBumped, and UsageStore.Refund onRefunded: a write
// through it commits with the charge, the bump or the refund, or with it not
// at all. Any other context holds none, and
// TxFrom reports false; no other method of this package reads a transaction
// from its context.
//
// The call's writes are in it already — a charge's ledger row, a bump's
// counters and, under an ExternalRef, its limes.bumps row, a refund's
// refunded amount on the charge and, under a RefundID, its limes.refunds
// row — and its locks held: every counter on the chain and, but for a bump,
// the tenant's aggregate, which every other charge of the tenant waits for
// until the callback returns and the store commits. The callback keeps to statements
// on this database, and short ones; a network call there holds the
// tenant's charges for as long.
//
// The transaction is the callback's while it runs, and no longer: once it
// returns, every call on it, from any goroutine, fails with
// pgx.ErrTxClosed, and a call still running then is waited for before the
// store commits. It takes one call at a time, as a pgx.Tx does, and so it
// takes the reads of the rows and batch results it hands out: a call made
// while another runs is refused with ErrChargeTransaction, never waited
// for, as pgx refuses a busy connection. That holds for a call from another
// goroutine, and for one from code the running call runs — a batch's
// QueuedQuery callback, a sql.Scanner, a driver.Valuer, a
// pgx.CopyFromSource — which would otherwise wait on itself with the
// tenant's locks held. Rows and batch results still open when the callback
// returns are closed then, and read no further. A callback that returns
// under another role than it found has the call refused with
// ErrChargeTransaction and rolled back, the role with it: the store's
// statements after it would run under that role, and a SET ROLE,
// committed, would outlast the call on the pooled connection. That holds
// for SET LOCAL ROLE as for SET ROLE; a callback that sets a role for its
// own statements sets it back before it returns. One that sets it
// where no ROLLBACK reaches — around a COMMIT or ROLLBACK of its own — has
// the connection closed rather than given back under that role; on a
// Config.DB that is a *pgx.Conn the store is then store_unavailable until
// the caller connects again. A DB of your own that begins transactions
// itself (DB) gives its connections back as it does. A deadlock the
// callback meets on a connection of its own and returns fails the call;
// only one of the call's own transaction runs it again.
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
	// closing is set once the callback has returned, before close waits for
	// a call still running: a call refused meanwhile is refused as closed.
	closing atomic.Bool
	// open closes each result handed out, rows and batches, latest first.
	open []func()
}

// errCallInFlight is a call on the transaction while another runs on it,
// refused rather than waited for: the one running may be what makes it.
var errCallInFlight = fmt.Errorf("%w: pgstore: a call on the transaction while another runs on it", ErrChargeTransaction)

// enter takes the lock for a call, refusing one made while another holds
// it: pgx.ErrTxClosed once the callback has returned, errCallInFlight
// before.
func (g *callbackGuard) enter() error {
	if g.mu.TryLock() {
		return nil
	}
	if g.closing.Load() {
		return pgx.ErrTxClosed
	}
	return errCallInFlight
}

// close ends the transaction for the callback, once any call running on it
// has returned: it closes every result the callback left open, so the
// connection is free for the store's COMMIT, and refuses every call after.
func (g *callbackGuard) close() {
	g.closing.Store(true)
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

// CopyFrom copies rows into table. A copy rows failed is answered with
// rows' own error: pgx sends it to the server as the copy's failure, whose
// error back would name a cancelled statement, not why.
func (t *callbackTx) CopyFrom(ctx context.Context, table pgx.Identifier, columns []string, rows pgx.CopyFromSource) (int64, error) {
	var n int64
	err := t.guard.run(func() (err error) {
		n, err = t.tx.CopyFrom(ctx, table, columns, rows)
		if sourceErr := rows.Err(); err != nil && sourceErr != nil {
			return sourceErr
		}
		return err
	})
	return n, err
}

// SendBatch sends b, its QueuedQuery callbacks handed results whose rows
// keep the connection, as the transaction's do (heldResults): pgx calls
// them with its own, from the read that holds the transaction.
func (t *callbackTx) SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults {
	for _, qq := range b.QueuedQueries {
		if fn := qq.Fn; fn != nil {
			qq.Fn = func(br pgx.BatchResults) error { return fn(heldResults{br}) }
		}
	}
	var results pgx.BatchResults
	if err := t.guard.run(func() error {
		results = t.guard.batch(t.tx.SendBatch(ctx, b))
		return nil
	}); err != nil {
		return closedBatch{err}
	}
	return results
}

// LargeObjects is the zero LargeObjects, which no call can use: every call
// on it panics, as on any zero pgx.LargeObjects, since it can return no
// error of its own. The transaction lends none: pgx.LargeObjects is a
// struct over the transaction it was made on, which this one cannot stand
// in for, so its calls would not be taken one at a time, and one kept past
// the callback would run beside the store's COMMIT. A callback keeps its
// data in tables.
func (t *callbackTx) LargeObjects() pgx.LargeObjects { return pgx.LargeObjects{} }

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

// settingCharging marks the transaction a charge's, a bump's or a refund's
// writes went in, for the rest of it: a transaction the callback ended and began
// again with SQL of its own does not carry it.
const settingCharging = "limes.charging"

// settingCallbackRole holds the session's role setting as the callback
// found it, for the rest of the transaction.
const settingCallbackRole = "limes.callback_role"

const (
	// sqlMarkCharge sets settingCharging to $1, and settingCallbackRole to
	// the session's role, local to the transaction, with the call's writes.
	sqlMarkCharge = `SELECT set_config('` + settingCharging + `', $1, true),
       set_config('` + settingCallbackRole + `', current_setting('role'), true)`

	// sqlRoleKept fails, with sqlStateRoleChanged, when the session's role
	// is not the one sqlMarkCharge found: the callback set one, which,
	// committed, would outlast the call on the pooled connection. A SET of
	// the session is undone with the transaction its failure aborts.
	sqlRoleKept = `DO $$ BEGIN
  IF current_setting('role') IS DISTINCT FROM current_setting('` + settingCallbackRole + `', true) THEN
    RAISE EXCEPTION USING ERRCODE = '` + sqlStateRoleChanged + `', MESSAGE = 'the callback set the session''s role';
  END IF;
END $$`

	// sqlStateRoleChanged is the SQLSTATE sqlRoleKept fails with: limes's
	// own class, "LM".
	sqlStateRoleChanged = "LM001"

	// sqlChargeStillOpen fails, dividing by zero, unless the transaction is
	// still the one sqlMarkCharge marked $1: one that does not fail lets the
	// COMMIT queued after it commit the call, and one that does aborts it.
	// The divisor reads a setting, never folded into a constant, so the
	// statement fails only when it runs.
	sqlChargeStillOpen = `SELECT 1 / (coalesce(current_setting('` + settingCharging + `', true), '') = $1)::int`

	// sqlStateDivisionByZero is the SQLSTATE sqlChargeStillOpen fails with.
	sqlStateDivisionByZero = "22012"
)

// callback is the caller's callback over a call's writes — onCharged,
// onBumped or onRefunded, bound to the receipt it is handed.
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
	then = append(then, markCallback(tx, mark))
	if err := c.writeBatch(ctx, tx, now, writeTenant, false, then); err != nil {
		return err
	}
	callbackCtx, end := withTx(ctx, tx)
	err := func() error {
		defer end()
		return call(callbackCtx)
	}()
	if err != nil {
		return callbackFailure(tx, err)
	}
	return endCallback(ctx, tx, mark, last)
}

// markCallback is sqlMarkCharge marking the transaction mark, its result
// read into tx's callbackRole: the role the connection must be under when
// it goes back to its pool.
func markCallback(tx pgx.Tx, mark string) heldStmt {
	return heldStmt{sql: sqlMarkCharge, args: []any{mark}, into: func(br pgx.BatchResults) error {
		var marked, role string
		if err := br.QueryRow().Scan(&marked, &role); err != nil {
			return err
		}
		if d, ok := tx.(*deferTx); ok {
			d.callbackRole = &role
		}
		return nil
	}}
}

// deadlockOutside is a deadlock a callback returned while the call's
// transaction stood: one it met on a connection of its own, which
// retryDeadlock does not run the call again for, since what the callback
// did there stands. It is the callback's error as it is, and adds no code.
type deadlockOutside struct{ err error }

func (d deadlockOutside) Error() string { return d.err.Error() }
func (d deadlockOutside) Unwrap() error { return d.err }

// callbackFailure is err, a callback's, as deadlockOutside when it is a
// deadlock and the call's transaction is still open: PostgreSQL aborted
// another transaction, not this one.
func callbackFailure(tx pgx.Tx, err error) error {
	if isDeadlock(err) && tx.Conn().PgConn().TxStatus() == txStatusInTransaction {
		return deadlockOutside{err}
	}
	return err
}

// endCallback ends a call whose writes were sent, marked mark, before its
// callback ran: it refuses one whose transaction the callback ended with SQL
// of its own — COMMIT, ROLLBACK, or either followed by BEGIN in the same
// statement — rather than through the Tx, which refuses, and one whose
// callback set the session's role (sqlRoleKept), rolled back so the role
// goes with it; otherwise it sends COMMIT when last, in one batch with the
// checks (sqlChargeStillOpen).
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
	b.Queue(sqlRoleKept)
	committing := last && queueCommit(tx, b)
	br := tx.SendBatch(ctx, b)
	_, err := br.Exec()
	if pgErr := (*pgconn.PgError)(nil); errors.As(err, &pgErr) && pgErr.Code == sqlStateDivisionByZero {
		_ = br.Close() // its error is the check's, answered: COMMIT did not run
		return errs.Errorf("%w: %w: the callback ended it and began another", meter.ErrOutcomeUnknown, ErrChargeTransaction)
	}
	if err == nil {
		_, err = br.Exec()
		if pgErr := (*pgconn.PgError)(nil); errors.As(err, &pgErr) && pgErr.Code == sqlStateRoleChanged {
			_ = br.Close() // its error is the check's, answered: COMMIT did not run
			return fmt.Errorf("%w: the callback set the session's role, which would outlast the call", ErrChargeTransaction)
		}
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
