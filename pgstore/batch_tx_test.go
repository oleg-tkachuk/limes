package pgstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/oleg-tkachuk/limes/meter"
)

// sqlDivideByZero is a statement PostgreSQL refuses once it runs.
const sqlDivideByZero = `SELECT 1/0`

// sqlBackendPID is the server process of the connection it runs on.
const sqlBackendPID = `SELECT pg_backend_pid()`

// sqlReadIsolation reads the isolation level of the transaction it runs in.
const sqlReadIsolation = `SELECT current_setting('transaction_isolation')`

// isolationReadCommitted is how PostgreSQL names READ COMMITTED.
const isolationReadCommitted = "read committed"

// pgDivisionByZero is PostgreSQL's code for a division by zero.
const pgDivisionByZero = "22012"

// soloPool is a pool of the one connection as appUser, so a call that left
// it mid-transaction, and pgxpool destroyed it, shows as a new one.
func soloPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	const conns = 1
	cfg, err := pgxpool.ParseConfig(ownerDSN)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.User, cfg.ConnConfig.Password = appUser, appPassword
	cfg.MaxConns = conns
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// backendPID is the server process of pool's connection, which reads back
// unchanged once a call has given the connection back clean.
func backendPID(t *testing.T, pool *pgxpool.Pool) uint32 {
	t.Helper()
	var pid uint32
	if err := pool.QueryRow(t.Context(), sqlBackendPID).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	return pid
}

// cleanAfter checks pool's one connection came back from a call outside a
// transaction, rolled back by the store rather than destroyed by the pool.
func cleanAfter(t *testing.T, pool *pgxpool.Pool, pid uint32) {
	t.Helper()
	if got := backendPID(t, pool); got != pid {
		t.Errorf("the connection is server process %d after the call, want %d: the pool destroyed it", got, pid)
	}
	pc, err := pool.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Release()
	if status := pc.Conn().PgConn().TxStatus(); status != txStatusIdle {
		t.Errorf("the connection's transaction status is %q, want %q", status, txStatusIdle)
	}
}

// isDivisionByZero reports whether err is PostgreSQL refusing sqlDivideByZero.
func isDivisionByZero(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == pgDivisionByZero
}

// A statement that fails in the batch COMMIT goes with fails the call with
// its own error, commits nothing, and the connection goes back rolled back.
func TestAFailedLastBatchRollsBack(t *testing.T) {
	pool := soloPool(t)
	c, err := newConn(Config{DB: pool, Scope: scopeRunning(sqlSetDefer)})
	if err != nil {
		t.Fatal(err)
	}
	pid := backendPID(t, pool)
	err = c.inReadTx(t.Context(), func(tx pgx.Tx) error {
		b := &pgx.Batch{}
		b.Queue(sqlDivideByZero)
		if !queueCommit(tx, b) {
			t.Fatal("a pool's transaction queued no COMMIT")
		}
		br := tx.SendBatch(t.Context(), b)
		_, err := br.Exec()
		return errors.Join(err, br.Close())
	})
	if !isDivisionByZero(err) {
		t.Errorf("the call returned %v, want the statement's division by zero", err)
	}
	if errors.Is(err, errNotCommitted) {
		t.Errorf("the call returned %v, want the statement's error alone", err)
	}
	cleanAfter(t, pool, pid)
}

// A COMMIT PostgreSQL answers with ROLLBACK, the transaction aborted before
// it, is not reported as committed: not by the batch, and not by Commit
// when the call drops the batch's error.
func TestACommitAnsweredWithRollbackIsNotSuccess(t *testing.T) {
	for name, dropBatchError := range map[string]bool{"returned": false, "dropped": true} {
		t.Run(name, func(t *testing.T) {
			pool := soloPool(t)
			c, err := newConn(Config{DB: pool})
			if err != nil {
				t.Fatal(err)
			}
			pid := backendPID(t, pool)
			err = c.inReadTx(t.Context(), func(tx pgx.Tx) error {
				// Its error is dropped, as a careless caller would: the
				// transaction stays aborted.
				_, _ = tx.Exec(t.Context(), sqlDivideByZero)
				b := &pgx.Batch{}
				queueCommit(tx, b)
				err := tx.SendBatch(t.Context(), b).Close()
				if dropBatchError {
					return nil
				}
				return err
			})
			if !errors.Is(err, pgx.ErrTxCommitRollback) {
				t.Errorf("the call returned %v, want %v", err, pgx.ErrTxCommitRollback)
			}
			cleanAfter(t, pool, pid)
		})
	}
}

// Once the batch COMMIT went with is sent, the transaction takes no
// statement: one would run outside it.
func TestNoStatementAfterTheLastBatch(t *testing.T) {
	c, err := newConn(Config{DB: app})
	if err != nil {
		t.Fatal(err)
	}
	var after error
	err = c.inReadTx(t.Context(), func(tx pgx.Tx) error {
		b := &pgx.Batch{}
		b.Queue(sqlBackendPID)
		queueCommit(tx, b)
		if err := tx.SendBatch(t.Context(), b).Close(); err != nil {
			return err
		}
		_, after = tx.Exec(t.Context(), sqlBackendPID)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(after, pgx.ErrTxClosed) {
		t.Errorf("a statement after the last batch returned %v, want %v", after, pgx.ErrTxClosed)
	}
}

// A call that sends nothing — its scope refused before any statement —
// begins no transaction and costs no round trip.
func TestACallThatSendsNothingBeginsNothing(t *testing.T) {
	pool, sent := latencyPoolOf(t, 0, roundTripConns)
	errRefused := errors.New("refused before any statement")
	c, err := newConn(Config{DB: pool, Scope: func(context.Context, pgx.Tx) error { return errRefused }})
	if err != nil {
		t.Fatal(err)
	}
	backendPID(t, pool) // the connection is dialled before counting
	before := sent.Load()
	if err := c.inReadTx(t.Context(), func(pgx.Tx) error { return nil }); !errors.Is(err, errRefused) {
		t.Fatalf("the call returned %v, want %v", err, errRefused)
	}
	if got := sent.Load() - before; got != 0 {
		t.Errorf("the call made %d round trips, want none", got)
	}
}

// BEGIN sent with the call's first batch begins READ COMMITTED, whatever
// the session's default: what onCharged reads through TxFrom.
func TestABatchedBeginIsReadCommitted(t *testing.T) {
	f := newTenantFixture(t)
	pc, err := app.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	conn := pc.Hijack()
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	if _, err := conn.Exec(t.Context(), sqlSetSession, "default_transaction_isolation", "serializable"); err != nil {
		t.Fatal(err)
	}
	usage, err := NewUsageStore(Config{DB: conn, Now: f.clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	var isolation string
	_, err = usage.Charge(WithTenant(t.Context(), f.mine), meter.ChargeRequest{Subject: meterSubject(f.child.ID, f.mine), Amount: 1},
		func(ctx context.Context, _ meter.Charged) error {
			tx, _ := TxFrom(ctx)
			return tx.QueryRow(ctx, sqlReadIsolation).Scan(&isolation)
		})
	if err != nil {
		t.Fatal(err)
	}
	if isolation != isolationReadCommitted {
		t.Errorf("the charge ran at %q, want %q", isolation, isolationReadCommitted)
	}
}

// failingChargeAmount is the amount of a charge the test trigger refuses
// as its ledger row is written, and failChargeMessage what it raises.
const (
	failingChargeAmount = 7
	failChargeMessage   = "limes test: charge refused"
)

// sqlFailCharges installs a trigger that refuses any charge of
// failingChargeAmount when it is written: a statement of the last batch
// that fails.
var sqlFailCharges = fmt.Sprintf(`
CREATE FUNCTION public.limes_test_fail_charge() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
	IF NEW.amount = %d THEN
		RAISE EXCEPTION '%s';
	END IF;
	RETURN NEW;
END $$;
CREATE TRIGGER limes_test_fail_charge BEFORE INSERT ON %s.charges
	FOR EACH ROW EXECUTE FUNCTION public.limes_test_fail_charge();`, failingChargeAmount, failChargeMessage, schema)

// sqlDropFailCharges removes what sqlFailCharges installs.
const sqlDropFailCharges = `
DROP TRIGGER IF EXISTS limes_test_fail_charge ON ` + schema + `.charges;
DROP FUNCTION IF EXISTS public.limes_test_fail_charge();`

// A Charge whose ledger row fails in the batch COMMIT goes with returns
// that error, moves no counter, and leaves the connection clean for the
// next call, which commits.
func TestAChargeFailingInItsLastBatchCommitsNothing(t *testing.T) {
	f := newTenantFixture(t)
	if _, err := owner.Exec(t.Context(), sqlFailCharges); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = owner.Exec(context.Background(), sqlDropFailCharges) })
	pool := soloPool(t)
	usage, err := NewUsageStore(Config{DB: pool, Now: f.clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	mine := WithTenant(t.Context(), f.mine)
	subject := meterSubject(f.child.ID, f.mine)
	pid := backendPID(t, pool)
	if _, err := usage.Charge(mine, meter.ChargeRequest{Subject: subject, Amount: failingChargeAmount}, nil); err == nil || !strings.Contains(err.Error(), failChargeMessage) {
		t.Fatalf("the charge returned %v, want the ledger row's refusal", err)
	}
	cleanAfter(t, pool, pid)
	const amount = 1
	got, err := usage.Charge(mine, meter.ChargeRequest{Subject: subject, Amount: amount}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.SpentAmount != amount {
		t.Errorf("spent %d after the refused charge and one of %d, want %d", got.SpentAmount, amount, amount)
	}
}

// A store logs, when constructed, whether BEGIN and COMMIT go in a call's
// batches, and on what.
func TestTheStoreLogsHowItOpensTransactions(t *testing.T) {
	pc, err := app.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Release()
	for _, tc := range []struct {
		db   DB
		want string
	}{
		{app, txPathAcquired},
		{pc.Conn(), txPathConn},
		{beginOnly{app}, txPathSeparate},
	} {
		var buf bytes.Buffer
		if _, err := NewUsageStore(Config{DB: tc.db, Logger: slog.New(slog.NewTextHandler(&buf, nil))}); err != nil {
			t.Fatal(err)
		}
		if want := logKeyTxPath + "=" + tc.want; !strings.Contains(buf.String(), want) {
			t.Errorf("a store on %T logged %q, want %s", tc.db, buf.String(), want)
		}
	}
}
