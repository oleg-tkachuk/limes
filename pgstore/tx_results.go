package pgstore

import (
	"sync/atomic"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

// guardedRows are rows of a callback's transaction: each read takes the
// guard's lock, as every call on the transaction does, so rows read from
// another goroutine are never read beside the store's own statements, and
// once the callback has returned they read no further. The guard closes
// them if the callback left them open.
type guardedRows struct {
	rows  pgx.Rows
	guard *callbackGuard
	// refused is why a read was refused, which Err reports: Next reads as
	// the rows' end, and they were not read to it.
	refused atomic.Pointer[error]
}

// rows hands r to the callback, to be closed with the transaction. g.mu
// must be held.
func (g *callbackGuard) rows(r pgx.Rows) pgx.Rows {
	g.open = append(g.open, r.Close)
	return &guardedRows{rows: r, guard: g}
}

// batch hands b to the callback, to be closed with the transaction. g.mu
// must be held.
func (g *callbackGuard) batch(b pgx.BatchResults) pgx.BatchResults {
	g.open = append(g.open, func() { _ = b.Close() })
	return &guardedBatch{results: b, guard: g}
}

// read runs call on rows the guard still holds open, unless another call
// holds the transaction or the callback has returned: then it returns why,
// which Err reports from then on.
func (r *guardedRows) read(call func()) error {
	err := r.guard.enter()
	if err == nil {
		defer r.guard.mu.Unlock()
		if r.guard.closed {
			err = pgx.ErrTxClosed
		}
	}
	if err != nil {
		r.refused.CompareAndSwap(nil, &err)
		return err
	}
	call()
	return nil
}

// Close closes the rows, unless another call holds the transaction — a
// batch's Close running its callback, say: the guard closes them then.
func (r *guardedRows) Close() {
	if r.guard.enter() != nil {
		return
	}
	defer r.guard.mu.Unlock()
	r.rows.Close()
}

// Err is why a read was refused, if one was: pgx.ErrTxClosed once the
// callback has returned, rows it closed then not read to their end.
func (r *guardedRows) Err() error {
	if refused := r.refused.Load(); refused != nil {
		return *refused
	}
	var err error
	if refusal := r.read(func() { err = r.rows.Err() }); refusal != nil {
		return refusal
	}
	return err
}

func (r *guardedRows) CommandTag() pgconn.CommandTag {
	var tag pgconn.CommandTag
	_ = r.read(func() { tag = r.rows.CommandTag() })
	return tag
}

func (r *guardedRows) FieldDescriptions() []pgconn.FieldDescription {
	var fields []pgconn.FieldDescription
	_ = r.read(func() { fields = r.rows.FieldDescriptions() })
	return fields
}

func (r *guardedRows) Next() bool {
	var next bool
	_ = r.read(func() { next = r.rows.Next() })
	return next
}

func (r *guardedRows) Scan(dest ...any) error {
	var err error
	if refusal := r.read(func() { err = r.rows.Scan(dest...) }); refusal != nil {
		return refusal
	}
	return err
}

func (r *guardedRows) Values() ([]any, error) {
	var values []any
	var err error
	if refusal := r.read(func() { values, err = r.rows.Values() }); refusal != nil {
		return nil, refusal
	}
	return values, err
}

func (r *guardedRows) RawValues() [][]byte {
	var raw [][]byte
	_ = r.read(func() { raw = r.rows.RawValues() })
	return raw
}

// Conn is nil, as the transaction's is: the connection is the store's.
func (r *guardedRows) Conn() *pgx.Conn { return nil }

func (r *guardedRows) TypeMap() *pgtype.Map {
	var m *pgtype.Map
	_ = r.read(func() { m = r.rows.TypeMap() })
	return m
}

// heldResults is a batch's results as pgx hands them to a QueuedQuery
// callback, from the read of the callback's call that holds the
// transaction already: the rows they hand out keep the connection
// (connlessRows), and the read needs no other hold.
type heldResults struct{ pgx.BatchResults }

func (r heldResults) Query() (pgx.Rows, error) {
	rows, err := r.BatchResults.Query()
	return connlessRows{rows}, err
}

// connlessRows are rows whose Conn is nil, as the transaction's is: the
// connection is the store's.
type connlessRows struct{ pgx.Rows }

func (connlessRows) Conn() *pgx.Conn { return nil }

// firstRow is the first of rows, as pgx's QueryRow reads it: no row is
// pgx.ErrNoRows, and the rows are closed once scanned.
type firstRow struct{ rows pgx.Rows }

func (r firstRow) Scan(dest ...any) error {
	defer r.rows.Close()
	if !r.rows.Next() {
		if err := r.rows.Err(); err != nil {
			return err
		}
		return pgx.ErrNoRows
	}
	if err := r.rows.Scan(dest...); err != nil {
		return err
	}
	r.rows.Close()
	return r.rows.Err()
}

// guardedBatch is the results of a batch sent on a callback's transaction,
// read under the guard's lock as guardedRows are, and refused once the
// callback has returned.
type guardedBatch struct {
	results pgx.BatchResults
	guard   *callbackGuard
}

func (b *guardedBatch) Exec() (pgconn.CommandTag, error) {
	var tag pgconn.CommandTag
	err := b.guard.run(func() (err error) {
		tag, err = b.results.Exec()
		return err
	})
	return tag, err
}

func (b *guardedBatch) Query() (pgx.Rows, error) {
	var rows pgx.Rows
	err := b.guard.run(func() error {
		r, err := b.results.Query()
		if err != nil {
			return err
		}
		rows = b.guard.rows(r)
		return nil
	})
	return rows, err
}

func (b *guardedBatch) QueryRow() pgx.Row {
	rows, err := b.Query()
	if err != nil {
		return closedRow{err}
	}
	return firstRow{rows}
}

// Tx is nil, as Conn is: a transaction a batch began would be on the
// store's connection, the callback's to end only as SQL the store refuses.
func (b *guardedBatch) Tx() pgx.Tx { return nil }

func (b *guardedBatch) Close() error {
	if err := b.guard.enter(); err != nil {
		return err
	}
	defer b.guard.mu.Unlock()
	return b.results.Close()
}
