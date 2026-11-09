package pgstore

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/oleg-tkachuk/limes/meter"
)

// defaultTenantBudgetLockTimeout bounds how long SetTenantBudget waits for
// the tenant's calls in flight before it gives up with
// meter.ErrTenantBudgetBusy (docs/adr/0022-unlimited-tenant-aggregate.md,
// Decision 9). While it waits, every call of the tenant that starts queues
// behind it, so the bound is the longest the tenant is stalled.
const defaultTenantBudgetLockTimeout = 5 * time.Second

// sqlLockNotAvailable is PostgreSQL's code for a lock not taken within
// lock_timeout.
const sqlLockNotAvailable = "55P03"

// lockTimeoutUnit is the unit lock_timeout is set in, and
// lockTimeoutResolution the shortest it holds: a shorter one is set as
// zero, which PostgreSQL reads as no bound at all.
const (
	lockTimeoutUnit       = "ms"
	lockTimeoutResolution = time.Millisecond
)

// The savepoint the wait for the tenant runs under: a lock_timeout aborts
// the transaction, and rolled back to it the transaction can still read
// who held the tenant.
const (
	sqlTenantAloneSavepoint = `SAVEPOINT pgstore_tenant_alone`
	sqlTenantAloneRelease   = `RELEASE SAVEPOINT pgstore_tenant_alone`
	sqlTenantAloneRollback  = `ROLLBACK TO SAVEPOINT pgstore_tenant_alone`
)

// sqlTenantHolders reads the transactions that hold the advisory lock of
// class $1 on tenant $2 granted, as sqlShareTenantLock takes it: how many,
// and the oldest whose session the role may see — how long its
// transaction had run, in microseconds, and whether it was idle in it. A
// session of another role shows no xact_start, so it is counted and no
// more.
const sqlTenantHolders = `
WITH holder AS (
    SELECT a.xact_start, a.state
    FROM pg_catalog.pg_locks l
    LEFT JOIN pg_catalog.pg_stat_activity a ON a.pid = l.pid
    CROSS JOIN (SELECT ` + tenantLockKey + ` AS key) k
    WHERE l.locktype = 'advisory' AND l.granted AND l.objsubid = 1
      AND l.database = (SELECT oid FROM pg_catalog.pg_database WHERE datname = pg_catalog.current_database())
      AND l.classid = ((k.key >> 32) & ` + lockKeyHalfSQL + `)::oid
      AND l.objid = (k.key & ` + lockKeyHalfSQL + `)::oid
)
SELECT (SELECT count(*) FROM holder)::int,
       (EXTRACT(epoch FROM pg_catalog.clock_timestamp() - o.xact_start) * 1e6)::bigint,
       o.state IN ('idle in transaction', 'idle in transaction (aborted)')
FROM (SELECT 1) one
LEFT JOIN LATERAL (
    SELECT xact_start, state FROM holder WHERE xact_start IS NOT NULL ORDER BY xact_start LIMIT 1
) o ON true`

// lockKeyHalfSQL masks one 32-bit half of a 64-bit advisory lock key, as
// pg_locks shows it: the high half in classid, the low in objid.
const lockKeyHalfSQL = "4294967295"

const (
	// sqlSetLockTimeout bounds the transaction's lock waits to $1, until
	// sqlDefaultLockTimeout or its end.
	sqlSetLockTimeout = `SELECT set_config('lock_timeout', $1, true)`
	// sqlDefaultLockTimeout puts the transaction's lock waits back on the
	// role's or the database's default for the rest of it.
	sqlDefaultLockTimeout = `SET LOCAL lock_timeout TO DEFAULT`
)

// takeTenantAlone takes tenant's advisory lock alone in tx, as an erase
// does, waiting at most tenantBudgetLockTimeout for the calls that share
// it: none of the tenant's calls is then in flight, and none starts until
// tx ends. Past the bound it returns a meter.TenantBudgetBusyError saying
// who held the tenant. A wait past the bound or past tenantWaitWarnAfter
// is logged at WARN.
func (s *UsageStore) takeTenantAlone(ctx context.Context, tx pgx.Tx, tenant uuid.UUID) error {
	b := &pgx.Batch{}
	b.Queue(sqlTenantAloneSavepoint)
	b.Queue(sqlSetLockTimeout, strconv.FormatInt(s.tenantBudgetLockTimeout.Milliseconds(), 10)+lockTimeoutUnit)
	b.Queue(sqlTakeTenantLock, eraseLockClass, tenant)
	b.Queue(sqlDefaultLockTimeout)
	b.Queue(sqlTenantAloneRelease)
	start := time.Now()
	br := tx.SendBatch(ctx, b)
	var err error
	for range b.Len() {
		if _, err = br.Exec(); err != nil {
			break
		}
	}
	err = errors.Join(err, br.Close())
	waited := time.Since(start)
	var pgErr *pgconn.PgError
	switch {
	case errors.As(err, &pgErr) && pgErr.Code == sqlLockNotAvailable:
		return s.tenantBusy(ctx, tx, tenant, waited)
	case err != nil:
		return err
	case waited > s.tenantWaitWarnAfter:
		s.log.WarnContext(ctx, "pgstore: SetTenantBudget waited long for a tenant's calls in flight",
			slog.String(logKeyOp, string(meter.StoreOperationSetTenantBudget)), slog.String(logKeyTenant, tenant.String()), slog.Duration(logKeyWait, waited))
	}
	return nil
}

// Log keys of what a busy SetTenantBudget saw of the tenant's holders.
const (
	logKeyHolders           = "holders"
	logKeyOldestRunning     = "oldest_running"
	logKeyIdleInTransaction = "idle_in_transaction"
)

// tenantBusy rolls tx back to before the wait that timed out, reads who
// held tenant, logs it at WARN, and returns it as a
// meter.TenantBudgetBusyError. A failure to read them is logged and
// returned beside the busy error, which still says how long it waited.
func (s *UsageStore) tenantBusy(ctx context.Context, tx pgx.Tx, tenant uuid.UUID, waited time.Duration) error {
	busy := &meter.TenantBudgetBusyError{TenantID: tenant, Waited: waited}
	attrs := []any{slog.String(logKeyOp, string(meter.StoreOperationSetTenantBudget)), slog.String(logKeyTenant, tenant.String()), slog.Duration(logKeyWait, waited)}
	var running *int64
	var idle *bool
	_, err := tx.Exec(ctx, sqlTenantAloneRollback)
	if err == nil {
		err = tx.QueryRow(ctx, sqlTenantHolders, eraseLockClass, tenant).Scan(&busy.Holders, &running, &idle)
	}
	if err != nil {
		s.log.WarnContext(ctx, "pgstore: gave up on a tenant's calls in flight and could not read who held it: SetTenantBudget changed nothing",
			append(attrs, slog.String(logKeyError, err.Error()))...)
		return errors.Join(busy, err)
	}
	attrs = append(attrs, slog.Int(logKeyHolders, busy.Holders))
	if running != nil {
		busy.Oldest = &meter.TenantBudgetHolder{Running: time.Duration(*running) * time.Microsecond, IdleInTransaction: idle != nil && *idle}
		attrs = append(attrs, slog.Duration(logKeyOldestRunning, busy.Oldest.Running), slog.Bool(logKeyIdleInTransaction, busy.Oldest.IdleInTransaction))
	}
	s.log.WarnContext(ctx, "pgstore: gave up on a tenant's calls in flight: SetTenantBudget changed nothing", attrs...)
	return busy
}
