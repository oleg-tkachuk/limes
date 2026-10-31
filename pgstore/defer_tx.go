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
type deferTx struct {
	pgx.Tx
	deferring bool
	held      []heldStmt
}

// heldStmt is one statement a scope executed, not sent yet.
type heldStmt struct {
	sql  string
	args []any
}

// errScope wraps a held-back statement's error as inReadTx wraps the
// scope's.
func errScope(err error) error {
	return fmt.Errorf("pgstore: scope: %w", err)
}

// take is what is held back, and holds nothing more.
func (t *deferTx) take() []heldStmt {
	h := t.held
	t.held = nil
	return h
}

// with is a batch of held, then the queries of b, if any.
func with(held []heldStmt, b *pgx.Batch) *pgx.Batch {
	out := &pgx.Batch{}
	for _, h := range held {
		out.Queue(h.sql, h.args...)
	}
	if b != nil {
		out.QueuedQueries = append(out.QueuedQueries, b.QueuedQueries...)
	}
	return out
}

// drainHeld reads the results of n held-back statements from br, the scope's
// error if one failed.
func drainHeld(br pgx.BatchResults, n int) error {
	for range n {
		if _, err := br.Exec(); err != nil {
			return errScope(err)
		}
	}
	return nil
}

// flush sends what is held back on its own.
func (t *deferTx) flush(ctx context.Context) error {
	held := t.take()
	if len(held) == 0 {
		return nil
	}
	br := t.Tx.SendBatch(ctx, with(held, nil))
	err := drainHeld(br, len(held))
	return errors.Join(err, br.Close())
}

// Exec holds sql back while the scope runs, and otherwise sends it with
// what is held back.
func (t *deferTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if t.deferring {
		t.held = append(t.held, heldStmt{sql: sql, args: args})
		return pgconn.CommandTag{}, nil
	}
	held := t.take()
	if len(held) == 0 {
		return t.Tx.Exec(ctx, sql, args...)
	}
	b := with(held, nil)
	b.Queue(sql, args...)
	br := t.Tx.SendBatch(ctx, b)
	if err := drainHeld(br, len(held)); err != nil {
		return pgconn.CommandTag{}, errors.Join(err, br.Close())
	}
	tag, err := br.Exec()
	return tag, errors.Join(err, br.Close())
}

// Query sends sql with what is held back.
func (t *deferTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	held := t.take()
	if len(held) == 0 {
		return t.Tx.Query(ctx, sql, args...)
	}
	b := with(held, nil)
	b.Queue(sql, args...)
	br := t.Tx.SendBatch(ctx, b)
	if err := drainHeld(br, len(held)); err != nil {
		return nil, errors.Join(err, br.Close())
	}
	rows, err := br.Query()
	if err != nil {
		return nil, errors.Join(err, br.Close())
	}
	return &batchRows{Rows: rows, br: br}, nil
}

// QueryRow sends sql with what is held back.
func (t *deferTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	held := t.take()
	if len(held) == 0 {
		return t.Tx.QueryRow(ctx, sql, args...)
	}
	b := with(held, nil)
	b.Queue(sql, args...)
	br := t.Tx.SendBatch(ctx, b)
	if err := drainHeld(br, len(held)); err != nil {
		return errRow{errors.Join(err, br.Close())}
	}
	return &batchRow{row: br.QueryRow(), br: br}
}

// SendBatch sends b with what is held back, first.
func (t *deferTx) SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults {
	held := t.take()
	if len(held) == 0 {
		return t.Tx.SendBatch(ctx, b)
	}
	return &drainedResults{BatchResults: t.Tx.SendBatch(ctx, with(held, b)), held: len(held)}
}

// CopyFrom sends what is held back, then copies.
func (t *deferTx) CopyFrom(ctx context.Context, table pgx.Identifier, cols []string, src pgx.CopyFromSource) (int64, error) {
	if err := t.flush(ctx); err != nil {
		return 0, err
	}
	return t.Tx.CopyFrom(ctx, table, cols, src)
}

// Prepare sends what is held back, then prepares.
func (t *deferTx) Prepare(ctx context.Context, name, sql string) (*pgconn.StatementDescription, error) {
	if err := t.flush(ctx); err != nil {
		return nil, err
	}
	return t.Tx.Prepare(ctx, name, sql)
}

// Begin sends what is held back, then begins a savepoint.
func (t *deferTx) Begin(ctx context.Context) (pgx.Tx, error) {
	if err := t.flush(ctx); err != nil {
		return nil, err
	}
	return t.Tx.Begin(ctx)
}

// Commit commits: what is held back scopes nothing the transaction did, so
// it is not sent.
func (t *deferTx) Commit(ctx context.Context) error {
	t.held = nil
	return t.Tx.Commit(ctx)
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

// drainedResults is a batch's results whose first held statements are the
// scope's, read before any of the caller's.
type drainedResults struct {
	pgx.BatchResults
	held int
	err  error
}

// ready reads the held statements' results the first time it is called.
func (d *drainedResults) ready() error {
	if d.held > 0 {
		d.err, d.held = drainHeld(d.BatchResults, d.held), 0
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
