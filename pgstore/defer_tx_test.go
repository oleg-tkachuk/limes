package pgstore

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// deferSetting is a setting a test scope sets, local to its transaction.
const (
	deferSetting = "limes_test.deferred"
	deferValue   = "set"
	sqlReadDefer = `SELECT current_setting('` + deferSetting + `', true)`
	sqlSetDefer  = `SELECT set_config('` + deferSetting + `', '` + deferValue + `', true)`
	// sqlFailDefer is a scope's statement PostgreSQL refuses.
	sqlFailDefer = `SELECT set_config('', 'x', true)`
)

// scopeRunning sqls, as a Scope of a consumer's runs its own.
func scopeRunning(sqls ...string) func(context.Context, pgx.Tx) error {
	return func(ctx context.Context, tx pgx.Tx) error {
		for _, sql := range sqls {
			if _, err := tx.Exec(ctx, sql); err != nil {
				return err
			}
		}
		return nil
	}
}

// A scope's statements reach the database before the call's first one,
// whichever way the call sends it.
func TestDeferTxSendsTheScopeFirst(t *testing.T) {
	c, err := newConn(Config{DB: app, Scope: scopeRunning(sqlSetDefer)})
	if err != nil {
		t.Fatal(err)
	}
	for name, read := range map[string]func(context.Context, pgx.Tx) (string, error){
		"QueryRow": func(ctx context.Context, tx pgx.Tx) (string, error) {
			var v string
			return v, tx.QueryRow(ctx, sqlReadDefer).Scan(&v)
		},
		"Query": func(ctx context.Context, tx pgx.Tx) (string, error) {
			rows, err := tx.Query(ctx, sqlReadDefer)
			if err != nil {
				return "", err
			}
			v, err := pgx.CollectExactlyOneRow(rows, pgx.RowTo[string])
			return v, err
		},
		"SendBatch": func(ctx context.Context, tx pgx.Tx) (string, error) {
			b := &pgx.Batch{}
			b.Queue(sqlReadDefer)
			br := tx.SendBatch(ctx, b)
			var v string
			err := br.QueryRow().Scan(&v)
			return v, errors.Join(err, br.Close())
		},
		"Exec, then QueryRow": func(ctx context.Context, tx pgx.Tx) (string, error) {
			if _, err := tx.Exec(ctx, `SELECT 1`); err != nil {
				return "", err
			}
			var v string
			return v, tx.QueryRow(ctx, sqlReadDefer).Scan(&v)
		},
	} {
		var got string
		err := c.inReadTx(t.Context(), func(tx pgx.Tx) error {
			var err error
			got, err = read(t.Context(), tx)
			return err
		})
		if err != nil || got != deferValue {
			t.Errorf("%s: the setting read %q, %v; want %q, the scope's", name, got, err, deferValue)
		}
	}
}

// A scope's statement that fails fails the call's first statement as the
// scope's error, and the transaction with it.
func TestDeferTxFailsAsTheScope(t *testing.T) {
	c, err := newConn(Config{DB: app, Scope: scopeRunning(sqlSetDefer, sqlFailDefer)})
	if err != nil {
		t.Fatal(err)
	}
	err = c.inReadTx(t.Context(), func(tx pgx.Tx) error {
		var v string
		return tx.QueryRow(t.Context(), sqlReadDefer).Scan(&v)
	})
	if err == nil || !strings.Contains(err.Error(), "pgstore: scope:") {
		t.Errorf("a call under a failing scope: err = %v, want the scope's", err)
	}
}

// A transaction that sends nothing commits, its scope never sent.
func TestDeferTxCommitsWithNothingSent(t *testing.T) {
	c, err := newConn(Config{DB: app, Scope: scopeRunning(sqlFailDefer)})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.inReadTx(t.Context(), func(pgx.Tx) error { return nil }); err != nil {
		t.Errorf("a transaction that sent nothing: %v", err)
	}
}
