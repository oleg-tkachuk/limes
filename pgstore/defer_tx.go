package pgstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// deferTx is a transaction that holds back the statements a scope executes
// and sends them with the first statement the call sends after it, in one
// batch: a scope costs no round trip of its own
// (docs/adr/0018-fewer-round-trips-per-charge.md). Only Exec is held back,
// while the scope runs; a scope that queries runs as before, what it held
// back going first. A held-back statement's error is the scope's, returned
// by the statement it went with, before that one runs.
//
// On a connection lent to the call (lendingDB), BEGIN is held back too: the
// transaction is begun by the call's first batch (pgx.Batch.BeginTx), and a
// call that sends nothing begins none. A meter call's last batch can carry
// COMMIT as well (queueCommit).
type deferTx struct {
	// Tx is the transaction once begun; nil while BEGIN is held back.
	pgx.Tx
	deferring bool
	held      []heldStmt
	// lent is the connection the call has to itself, when BEGIN goes with
	// its first statement; nil for a transaction DB.BeginTx began.
	lent *pgx.Conn
	// release gives lent back, once the transaction has ended; nil for a
	// connection that is not the store's to give back.
	release func()
	// opts are those BEGIN begins the transaction with.
	opts pgx.TxOptions
	// commitQueued is COMMIT queued at the end of the call's last batch,
	// and committed its tag read back as COMMIT.
	commitQueued, committed bool
	// ended is the transaction over for the call's statements: its last
	// batch sent, or the transaction committed or rolled back.
	ended bool
	// finished is Commit or Rollback done, and lent given back.
	finished bool
	// callbackRole is the session's role as a callback found it, once one
	// ran on the transaction (markCallback): lent goes back to its pool
	// only under it.
	callbackRole *string
	// discarded is lent closed rather than given back as it was: a
	// callback left the session under another role than it found.
	discarded bool
}

// sqlSessionRole reads the session's role setting.
const sqlSessionRole = `SELECT current_setting('role')`

// heldStmt is one statement a scope executed, not sent yet.
type heldStmt struct {
	sql  string
	args []any
	// into, when set, reads the statement's result from the batch in
	// place of discarding it; done, when set, runs once the batch is sent
	// without error.
	into func(pgx.BatchResults) error
	done func()
}

// sqlCommit ends a meter call's transaction in its last batch.
const sqlCommit = "COMMIT"

// tagCommit is the command tag of a COMMIT that committed. PostgreSQL
// answers COMMIT in an aborted transaction with the tag ROLLBACK and no
// error.
const tagCommit = "COMMIT"

// txStatusIdle is the transaction status PostgreSQL reports of a connection
// outside a transaction block (ReadyForQuery 'I').
const txStatusIdle = 'I'

// errNotCommitted is a COMMIT that went with a call's last batch and did
// not commit.
var errNotCommitted = fmt.Errorf("pgstore: the call's transaction did not commit: %w", pgx.ErrTxCommitRollback)

// errScope wraps a held-back statement's error as inReadTx wraps the
// scope's.
func errScope(err error) error {
	return fmt.Errorf("pgstore: scope: %w", err)
}

// errBegin wraps the error of a BEGIN sent with the call's first batch.
func errBegin(err error) error {
	return fmt.Errorf("pgstore: begin: %w", err)
}

// begun reports whether the transaction has been begun on the database.
func (t *deferTx) begun() bool { return t.Tx != nil }

// pending reports whether a statement sent now must carry something before
// it: BEGIN, or what the scope held back.
func (t *deferTx) pending() bool { return !t.begun() || len(t.held) > 0 }

// take is what is held back, and holds nothing more.
func (t *deferTx) take() []heldStmt {
	h := t.held
	t.held = nil
	return h
}

// send sends b, if any, after BEGIN when the transaction is not begun yet
// and after what is held back: results that read theirs first.
func (t *deferTx) send(ctx context.Context, b *pgx.Batch) pgx.BatchResults {
	held := t.take()
	begin := !t.begun()
	out := &pgx.Batch{}
	if begin {
		out.BeginTx(t.opts)
	}
	for _, h := range held {
		out.Queue(h.sql, h.args...)
	}
	if b != nil {
		out.QueuedQueries = append(out.QueuedQueries, b.QueuedQueries...)
	}
	var br pgx.BatchResults
	if begin {
		br = t.lent.SendBatch(ctx, out)
		t.Tx = br.Tx()
	} else {
		br = t.Tx.SendBatch(ctx, out)
	}
	if t.commitQueued {
		t.ended = true
	}
	return &drainedResults{BatchResults: br, begin: begin, held: len(held)}
}

// drainHeld reads the results of BEGIN, when begin, and of n held-back
// statements from br: BEGIN's error, or the scope's if one failed.
func drainHeld(br pgx.BatchResults, begin bool, n int) error {
	if begin {
		if _, err := br.Exec(); err != nil {
			return errBegin(err)
		}
	}
	for range n {
		if _, err := br.Exec(); err != nil {
			return errScope(err)
		}
	}
	return nil
}

// flush sends what is held back, BEGIN among it, on its own.
func (t *deferTx) flush(ctx context.Context) error {
	if !t.pending() {
		return nil
	}
	return t.send(ctx, nil).Close()
}

// queueCommit queues COMMIT at the end of b, the call's last batch, on a
// lent connection: once b is sent the transaction is over, and Commit
// reports whether its COMMIT committed. It reports false, queuing nothing,
// on a transaction DB.BeginTx began, whose Commit sends COMMIT itself.
func (t *deferTx) queueCommit(b *pgx.Batch) bool {
	if t.lent == nil || t.ended {
		return false
	}
	b.Queue(sqlCommit).Exec(func(tag pgconn.CommandTag) error {
		if tag.String() != tagCommit {
			return fmt.Errorf("%w (tag %q)", errNotCommitted, tag.String())
		}
		t.committed = true
		return nil
	})
	t.commitQueued = true
	return true
}

// queueCommit queues COMMIT at the end of b when tx can send it there
// (deferTx.queueCommit).
func queueCommit(tx pgx.Tx, b *pgx.Batch) bool {
	d, ok := tx.(*deferTx)
	return ok && d.queueCommit(b)
}

// Exec holds sql back while the scope runs, and otherwise sends it with
// what is held back.
func (t *deferTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if t.ended {
		return pgconn.CommandTag{}, pgx.ErrTxClosed
	}
	if t.deferring {
		t.held = append(t.held, heldStmt{sql: sql, args: args})
		return pgconn.CommandTag{}, nil
	}
	if !t.pending() {
		return t.Tx.Exec(ctx, sql, args...)
	}
	b := &pgx.Batch{}
	b.Queue(sql, args...)
	br := t.send(ctx, b)
	tag, err := br.Exec()
	return tag, errors.Join(err, br.Close())
}

// Query sends sql with what is held back.
func (t *deferTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if t.ended {
		return nil, pgx.ErrTxClosed
	}
	if !t.pending() {
		return t.Tx.Query(ctx, sql, args...)
	}
	b := &pgx.Batch{}
	b.Queue(sql, args...)
	br := t.send(ctx, b)
	rows, err := br.Query()
	if err != nil {
		return nil, errors.Join(err, br.Close())
	}
	return &batchRows{Rows: rows, br: br}, nil
}

// QueryRow sends sql with what is held back.
func (t *deferTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if t.ended {
		return errRow{pgx.ErrTxClosed}
	}
	if !t.pending() {
		return t.Tx.QueryRow(ctx, sql, args...)
	}
	b := &pgx.Batch{}
	b.Queue(sql, args...)
	br := t.send(ctx, b)
	return &batchRow{row: br.QueryRow(), br: br}
}

// SendBatch sends b with what is held back, first.
func (t *deferTx) SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults {
	if t.ended {
		return closedBatch{pgx.ErrTxClosed}
	}
	if !t.pending() {
		results := t.Tx.SendBatch(ctx, b)
		if t.commitQueued {
			t.ended = true
		}
		return results
	}
	return t.send(ctx, b)
}

// CopyFrom sends what is held back, then copies.
func (t *deferTx) CopyFrom(ctx context.Context, table pgx.Identifier, cols []string, src pgx.CopyFromSource) (int64, error) {
	if t.ended {
		return 0, pgx.ErrTxClosed
	}
	if err := t.flush(ctx); err != nil {
		return 0, err
	}
	return t.Tx.CopyFrom(ctx, table, cols, src)
}

// Prepare sends what is held back, then prepares.
func (t *deferTx) Prepare(ctx context.Context, name, sql string) (*pgconn.StatementDescription, error) {
	if t.ended {
		return nil, pgx.ErrTxClosed
	}
	if err := t.flush(ctx); err != nil {
		return nil, err
	}
	return t.Tx.Prepare(ctx, name, sql)
}

// Begin sends what is held back, then begins a savepoint.
func (t *deferTx) Begin(ctx context.Context) (pgx.Tx, error) {
	if t.ended {
		return nil, pgx.ErrTxClosed
	}
	if err := t.flush(ctx); err != nil {
		return nil, err
	}
	return t.Tx.Begin(ctx)
}

// LargeObjects is the transaction's large objects, BEGIN sent first when
// it is held back. LargeObjects takes no context, so it is sent with none.
func (t *deferTx) LargeObjects() pgx.LargeObjects {
	if !t.begun() {
		_ = t.flush(context.Background()) // a failed BEGIN leaves t.Tx closed: its calls fail
	}
	return t.Tx.LargeObjects()
}

// Conn is the transaction's connection: the lent one while BEGIN is held
// back.
func (t *deferTx) Conn() *pgx.Conn {
	if !t.begun() {
		return t.lent
	}
	return t.Tx.Conn()
}

// Commit commits: what is held back scopes nothing the transaction did, so
// it is not sent. On a lent connection, a transaction never begun commits
// nothing and sends nothing, and one whose COMMIT went with its last batch
// sends nothing more and reports whether it committed.
func (t *deferTx) Commit(ctx context.Context) error {
	t.held = nil
	if t.lent == nil {
		return t.Tx.Commit(ctx)
	}
	if t.finished {
		return pgx.ErrTxClosed
	}
	switch {
	case t.commitQueued && t.committed:
		t.finish()
		return nil
	case t.commitQueued:
		return errors.Join(errNotCommitted, t.rollback(ctx))
	case !t.begun():
		t.finish()
		return nil
	}
	defer t.finish()
	return t.Tx.Commit(ctx)
}

// Rollback rolls back. On a lent connection it sends nothing for a
// transaction never begun, or one the database has ended already: its
// COMMIT went with the last batch, or its BEGIN failed.
func (t *deferTx) Rollback(ctx context.Context) error {
	t.held = nil
	if t.lent == nil {
		return t.Tx.Rollback(ctx)
	}
	if t.finished {
		return pgx.ErrTxClosed
	}
	return t.rollback(ctx)
}

// rollback sends ROLLBACK when the lent connection is still in a
// transaction — open, or aborted by a statement that failed — so it goes
// back clean, then gives the connection back: closed, when a callback ran
// on the transaction and left the session under another role than it
// found (keepsCallbackRole).
func (t *deferTx) rollback(ctx context.Context) error {
	defer t.finish()
	if !t.begun() {
		return nil
	}
	var err error
	if t.lent.PgConn().TxStatus() != txStatusIdle {
		err = t.Tx.Rollback(ctx)
	}
	if !t.keepsCallbackRole(ctx) {
		t.discarded = true
		err = errors.Join(err, t.lent.Close(ctx))
	}
	return err
}

// keepsCallbackRole reports whether the session's role, read once the
// transaction has ended, is the one the callback that ran on it found,
// true when none ran. A SET ROLE in the transaction ends with it; one a
// callback sent after ending the transaction with SQL of its own, or
// before a COMMIT of its own, does not, and would serve the next call on
// the connection. A role that cannot be read is not the one.
func (t *deferTx) keepsCallbackRole(ctx context.Context) bool {
	if t.callbackRole == nil {
		return true
	}
	var role string
	if err := t.lent.QueryRow(ctx, sqlSessionRole).Scan(&role); err != nil {
		return false
	}
	return role == *t.callbackRole
}

// finish ends the transaction for the call and gives the lent connection
// back, once.
func (t *deferTx) finish() {
	t.ended, t.finished = true, true
	if release := t.release; release != nil {
		t.release = nil
		release()
	}
}

// batchRows is the rows of a query sent in a batch, which closes the batch
// when they are closed.
type batchRows struct {
	pgx.Rows
	br     pgx.BatchResults
	closed bool
	err    error
}

// Close closes the rows, then the batch.
func (r *batchRows) Close() {
	if r.closed {
		return
	}
	r.closed = true
	r.Rows.Close()
	r.err = r.br.Close()
}

// Err is the rows' error, or the batch's once closed.
func (r *batchRows) Err() error {
	return errors.Join(r.Rows.Err(), r.err)
}

// batchRow is the row of a query sent in a batch, which closes the batch
// once scanned.
type batchRow struct {
	row pgx.Row
	br  pgx.BatchResults
}

// Scan scans the row, then closes the batch.
func (r *batchRow) Scan(dest ...any) error {
	err := r.row.Scan(dest...)
	if cerr := r.br.Close(); err == nil {
		err = cerr
	}
	return err
}

// errRow is a row that is only an error.
type errRow struct{ err error }

func (r errRow) Scan(...any) error { return r.err }

// drainedResults is a batch's results whose first statements are BEGIN,
// when begin, and the scope's held ones, read before any of the caller's.
type drainedResults struct {
	pgx.BatchResults
	begin bool
	held  int
	err   error
}

// ready reads BEGIN's and the held statements' results the first time it
// is called.
func (d *drainedResults) ready() error {
	if d.begin || d.held > 0 {
		d.err = drainHeld(d.BatchResults, d.begin, d.held)
		d.begin, d.held = false, 0
	}
	return d.err
}

func (d *drainedResults) Exec() (pgconn.CommandTag, error) {
	if err := d.ready(); err != nil {
		return pgconn.CommandTag{}, err
	}
	return d.BatchResults.Exec()
}

func (d *drainedResults) Query() (pgx.Rows, error) {
	if err := d.ready(); err != nil {
		return nil, err
	}
	return d.BatchResults.Query()
}

func (d *drainedResults) QueryRow() pgx.Row {
	if err := d.ready(); err != nil {
		return errRow{err}
	}
	return d.BatchResults.QueryRow()
}

func (d *drainedResults) Close() error {
	return errors.Join(d.ready(), d.BatchResults.Close())
}
