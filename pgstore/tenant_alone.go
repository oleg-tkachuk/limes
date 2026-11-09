package pgstore

import (
	"context"
	"errors"
	"fmt"
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

// lockTimeoutUnit is the unit lock_timeout is set in.
const lockTimeoutUnit = "ms"

const (
	// sqlSetLockTimeout bounds the transaction's lock waits to $1, until
	// sqlDefaultLockTimeout or its end.
	sqlSetLockTimeout = `SELECT set_config('lock_timeout', $1, true)`
	// sqlDefaultLockTimeout puts the transaction's lock waits back on the
	// role's or the database's default for the rest of it.
	sqlDefaultLockTimeout = `SET LOCAL lock_timeout TO DEFAULT`
)

// errTenantBudgetBusy is ErrTenantBudgetBusy as pgstore returns it.
var errTenantBudgetBusy = fmt.Errorf("%w: pgstore", meter.ErrTenantBudgetBusy)

// takeTenantAlone takes tenant's advisory lock alone in tx, as an erase
// does, waiting at most tenantBudgetLockTimeout for the calls that share
// it: none of the tenant's calls is then in flight, and none starts until
// tx ends. Past the bound it returns errTenantBudgetBusy. A wait past the
// bound or past tenantWaitWarnAfter is logged at WARN.
func (s *UsageStore) takeTenantAlone(ctx context.Context, tx pgx.Tx, tenant uuid.UUID) error {
	b := &pgx.Batch{}
	b.Queue(sqlSetLockTimeout, strconv.FormatInt(s.tenantBudgetLockTimeout.Milliseconds(), 10)+lockTimeoutUnit)
	b.Queue(sqlTakeTenantLock, eraseLockClass, tenant)
	b.Queue(sqlDefaultLockTimeout)
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
		s.log.WarnContext(ctx, "pgstore: gave up on a tenant's calls in flight: SetTenantBudget changed nothing",
			slog.String(logKeyOp, string(meter.StoreOperationSetTenantBudget)), slog.String(logKeyTenant, tenant.String()), slog.Duration(logKeyWait, waited))
		return errTenantBudgetBusy
	case err != nil:
		return err
	case waited > s.tenantWaitWarnAfter:
		s.log.WarnContext(ctx, "pgstore: SetTenantBudget waited long for a tenant's calls in flight",
			slog.String(logKeyOp, string(meter.StoreOperationSetTenantBudget)), slog.String(logKeyTenant, tenant.String()), slog.Duration(logKeyWait, waited))
	}
	return nil
}
