package pgstore

import (
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

// read runs call on rows the guard still holds open, and reports false once
// the callback has returned.
func (r *guardedRows) read(call func()) bool {
	if r.guard.enter() != nil {
		return false
	}
	defer r.guard.mu.Unlock()
	if r.guard.closed {
		return false
	}
	call()
	return true
}

// Close closes the rows, unless a batch's callback calls it, while its
// batch's Close holds the transaction: the guard closes them then.
func (r *guardedRows) Close() {
	if r.guard.enter() != nil {
		return
	}
	defer r.guard.mu.Unlock()
	r.rows.Close()
}

// Err is pgx.ErrTxClosed once the callback has returned: rows it closed
// then were not read to their end.
func (r *guardedRows) Err() error {
	var err error
	if !r.read(func() { err = r.rows.Err() }) {
		return pgx.ErrTxClosed
	}
	return err
}

func (r *guardedRows) CommandTag() pgconn.CommandTag {
	var tag pgconn.CommandTag
	r.read(func() { tag = r.rows.CommandTag() })
	return tag
}

func (r *guardedRows) FieldDescriptions() []pgconn.FieldDescription {
	var fields []pgconn.FieldDescription
	r.read(func() { fields = r.rows.FieldDescriptions() })
	return fields
}

func (r *guardedRows) Next() bool {
	var next bool
	r.read(func() { next = r.rows.Next() })
	return next
}

func (r *guardedRows) Scan(dest ...any) error {
	err := pgx.ErrTxClosed
	r.read(func() { err = r.rows.Scan(dest...) })
	return err
}

func (r *guardedRows) Values() ([]any, error) {
	var values []any
	err := pgx.ErrTxClosed
	r.read(func() { values, err = r.rows.Values() })
	return values, err
}

func (r *guardedRows) RawValues() [][]byte {
	var raw [][]byte
	r.read(func() { raw = r.rows.RawValues() })
	return raw
}

// Conn is nil, as the transaction's is: the connection is the store's.
func (r *guardedRows) Conn() *pgx.Conn { return nil }

func (r *guardedRows) TypeMap() *pgtype.Map {
	var m *pgtype.Map
	r.read(func() { m = r.rows.TypeMap() })
	return m
}

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
