package pgstore

import (
	"context"
	"fmt"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/meter"
)

// An erase of a tenant's usage and a call that meters the tenant are ordered
// by an advisory lock on the tenant, taken first in each transaction: the
// erase holds it alone, each meter call shares it. A call that comes after
// the erase waits for it, then finds the tenant marked erased; an erase that
// comes after a call waits for the call, then removes what it wrote.
const (
	// eraseLockClass is the first key of those advisory locks, which sets
	// them apart from any a consumer takes on the same database: "lime".
	eraseLockClass int32 = 0x6c696d65

	// revokeLockClass is the first key of the advisory locks that order a
	// revoke and the inserts of children of the same tenant: "limr".
	revokeLockClass int32 = 0x6c696d72

	// sqlTakeTenantLock takes the advisory lock of class $1 on tenant $2 alone,
	// and sqlShareTenantLock shares it, until the transaction ends.
	sqlTakeTenantLock  = `SELECT pg_advisory_xact_lock($1, hashtext($2::text))`
	sqlShareTenantLock = `SELECT pg_advisory_xact_lock_shared($1, hashtext($2::text))`

	sqlIsErased   = `SELECT EXISTS (SELECT 1 FROM limes.erased_tenants WHERE tenant_id = $1)`
	sqlMarkErased = `
INSERT INTO limes.erased_tenants (tenant_id, erased_at) VALUES ($1, $2)
ON CONFLICT (tenant_id) DO NOTHING`
	// sqlForgetErased drops the mark of each erased tenant none of whose
	// records is left: no capability of it can be metered any more.
	sqlForgetErased = `
DELETE FROM limes.erased_tenants e
WHERE NOT EXISTS (SELECT 1 FROM limes.capabilities c WHERE c.tenant_id = e.tenant_id)`
)

// markErased takes the tenant's erase lock and marks it erased, first in an
// erase's transaction.
func markErased(ctx context.Context, tx pgx.Tx, tenant uuid.UUID, now time.Time) error {
	if _, err := tx.Exec(ctx, sqlTakeTenantLock, eraseLockClass, tenant); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, sqlMarkErased, tenant, now)
	return err
}

// notErased shares the tenant's erase lock and refuses a tenant marked
// erased, first in a meter call's transaction.
func notErased(ctx context.Context, tx pgx.Tx, tenant uuid.UUID) error {
	if _, err := tx.Exec(ctx, sqlShareTenantLock, eraseLockClass, tenant); err != nil {
		return err
	}
	var erased bool
	if err := tx.QueryRow(ctx, sqlIsErased, tenant).Scan(&erased); err != nil {
		return err
	}
	if erased {
		return fmt.Errorf("%w: %s", limes.ErrTenantErased, tenant)
	}
	return nil
}

// The statements of an erase, in the order the meter locks: a tenant's
// holds, its records in id order, its aggregate, its counters in id order,
// its copies' counters, then the ledger, which a refund locks last too. Each
// lock is taken before a row is written, so an erase and a meter call never
// wait on each other in a cycle.
const (
	sqlLockTenantHolds = `
SELECT id FROM limes.reservations WHERE tenant_id = $1 ORDER BY id FOR UPDATE`

	sqlLockTenantRecords = `
SELECT id FROM limes.capabilities WHERE tenant_id = $1 ORDER BY id FOR UPDATE`

	// sqlRevokeTenant writes a revocation entry for every capability of the
	// tenant that has none; the entries already there stand.
	sqlRevokeTenant = `
INSERT INTO limes.revocations (capability_id, tenant_id, revoked_at, note, actor, cascaded)
SELECT id, tenant_id, $2, $3, $4, false FROM limes.capabilities WHERE tenant_id = $1
ON CONFLICT (capability_id) DO NOTHING`

	// sqlRemoveDueRecords removes the tenant's records Purge would: expired
	// before the cutoff. A child expires no later than its parent, so the
	// rows due hold each one's descendants, and the one statement removes
	// them together; their revocation entries go with them (ON DELETE
	// CASCADE).
	sqlRemoveDueRecords = `
DELETE FROM limes.capabilities WHERE tenant_id = $1 AND expires_at < $2`

	sqlCountTenantRecords = `SELECT count(*) FROM limes.capabilities WHERE tenant_id = $1`

	sqlLockTenantUsage = `
SELECT capability_id FROM limes.usage WHERE tenant_id = $1 ORDER BY capability_id FOR UPDATE`

	sqlLockTenantCopies = `
SELECT revocation_id FROM limes.copy_usage WHERE tenant_id = $1 ORDER BY capability_id, revocation_id FOR UPDATE`

	sqlDeleteTenantHolds   = `DELETE FROM limes.reservations WHERE tenant_id = $1`
	sqlDeleteTenantBudget  = `DELETE FROM limes.tenant_budgets WHERE tenant_id = $1`
	sqlDeleteTenantUsage   = `DELETE FROM limes.usage WHERE tenant_id = $1`
	sqlDeleteTenantCopies  = `DELETE FROM limes.copy_usage WHERE tenant_id = $1`
	sqlDeleteTenantCharges = `DELETE FROM limes.charges WHERE tenant_id = $1`
)

// EraseTenantRecords implements limes.TenantRecordEraser: it revokes every
// capability of the tenant not revoked yet and removes the records Purge
// would remove at limes.MinPurgeRetention, in one transaction scoped to the
// tenant (Config.CheckScope).
func (s *Store) EraseTenantRecords(ctx context.Context, req limes.EraseTenantRequest) (limes.EraseTenantResult, error) {
	if err := req.Validate(); err != nil {
		return limes.EraseTenantResult{}, err
	}
	var out limes.EraseTenantResult
	err := s.inTenantTx(ctx, req.TenantID, func(tx pgx.Tx) error {
		var err error
		out, err = s.eraseRecords(ctx, tx, req)
		return err
	})
	return out, err
}

// Erased is what EraseTenant hands its callback once the store's rows of the
// tenant are gone, in the transaction that removes them. Like the result it
// carries, it does not compare, so a field added in 1.x breaks no caller.
type Erased struct {
	// Tx is the erase's transaction, scoped to the tenant.
	Tx pgx.Tx
	// Result is what the call returns once the transaction commits.
	Result limes.EraseTenantResult
}

// EraseTenant erases the tenant's records, as EraseTenantRecords does, and
// its usage, as UsageStore.EraseTenantUsage does, in one transaction, and
// runs onErased in it last, so a consumer removes its own rows of the tenant
// with them or not at all. An error from onErased rolls everything back.
func (s *Store) EraseTenant(ctx context.Context, req limes.EraseTenantRequest, onErased func(ctx context.Context, e Erased) error) (limes.EraseTenantResult, error) {
	if err := req.Validate(); err != nil {
		return limes.EraseTenantResult{}, err
	}
	var out limes.EraseTenantResult
	err := s.inTenantTx(ctx, req.TenantID, func(tx pgx.Tx) error {
		if err := markErased(ctx, tx, req.TenantID, s.clock()); err != nil {
			return err
		}
		// The holds are locked first, as a Settle locks its hold before
		// the records.
		if err := lockAll(ctx, tx, sqlLockTenantHolds, req); err != nil {
			return err
		}
		var err error
		if out, err = s.eraseRecords(ctx, tx, req); err != nil {
			return err
		}
		if err := eraseUsage(ctx, tx, meter.EraseTenantUsageRequest{TenantID: req.TenantID}); err != nil {
			return err
		}
		if onErased != nil {
			return onErased(ctx, Erased{Tx: tx, Result: out})
		}
		return nil
	})
	return out, err
}

// eraseRecords revokes and removes in tx, its records locked first, after
// the tenant's revoke lock, as a revoke takes it (lockForRevoke).
func (s *Store) eraseRecords(ctx context.Context, tx pgx.Tx, req limes.EraseTenantRequest) (limes.EraseTenantResult, error) {
	var out limes.EraseTenantResult
	if err := lockForRevoke(ctx, tx, req.TenantID); err != nil {
		return out, err
	}
	if err := lockAll(ctx, tx, sqlLockTenantRecords, req); err != nil {
		return out, err
	}
	now := s.clock()
	tag, err := tx.Exec(ctx, sqlRevokeTenant, req.TenantID, now, req.Note, req.Actor)
	if err != nil {
		return out, err
	}
	out.Revoked = tag.RowsAffected()
	if tag, err = tx.Exec(ctx, sqlRemoveDueRecords, req.TenantID, now.Add(-limes.MinPurgeRetention)); err != nil {
		return out, err
	}
	out.Removed = tag.RowsAffected()
	err = tx.QueryRow(ctx, sqlCountTenantRecords, req.TenantID).Scan(&out.Kept)
	return out, err
}

// EraseTenantUsage implements meter.TenantUsageEraser: it marks the tenant
// erased and removes its holds, aggregate, counters, copies' counters and
// charges in one transaction scoped to the tenant (Config.CheckScope).
func (s *UsageStore) EraseTenantUsage(ctx context.Context, req meter.EraseTenantUsageRequest) error {
	if err := req.Validate(); err != nil {
		return err
	}
	return s.inTenantTx(ctx, req.TenantID, func(tx pgx.Tx) error {
		if err := markErased(ctx, tx, req.TenantID, s.clock()); err != nil {
			return err
		}
		return eraseUsage(ctx, tx, req)
	})
}

// eraseUsage locks the tenant's meter rows in the meter's order, then
// removes them and the ledger.
func eraseUsage(ctx context.Context, tx pgx.Tx, req meter.EraseTenantUsageRequest) error {
	steps := []string{
		sqlLockTenantHolds, sqlLockTenant, sqlLockTenantUsage, sqlLockTenantCopies,
		sqlDeleteTenantHolds, sqlDeleteTenantBudget, sqlDeleteTenantUsage, sqlDeleteTenantCopies, sqlDeleteTenantCharges,
	}
	for _, sql := range steps {
		if _, err := tx.Exec(ctx, sql, req.TenantID); err != nil {
			return err
		}
	}
	return nil
}

// lockAll runs a locking query for the tenant req names and drops its rows.
func lockAll(ctx context.Context, tx pgx.Tx, sql string, req limes.EraseTenantRequest) error {
	_, err := tx.Exec(ctx, sql, req.TenantID)
	return err
}

var (
	_ limes.TenantRecordEraser = (*Store)(nil)
	_ meter.TenantUsageEraser  = (*UsageStore)(nil)
)

// A revoke and an insert of a child in the same tenant are ordered by an
// advisory lock on the tenant, taken before any record's row: a revoke —
// and an erase of the tenant's records, which revokes them — holds it
// alone, each insert of a child shares it. An insert holds its parent's row
// for share, which the revoke's mark must update, and a row lock grants a
// new share holder past an updater already waiting: inserts overlapping one
// another under a capability would hold it without a gap, and its revoke
// would wait for as long as they kept coming. An advisory lock queues them
// instead, so an insert that comes after a waiting revoke waits behind it.

// lockForRevoke takes tenant's revoke lock alone, first in a revoke's
// transaction.
func lockForRevoke(ctx context.Context, tx pgx.Tx, tenant uuid.UUID) error {
	_, err := tx.Exec(ctx, sqlTakeTenantLock, revokeLockClass, tenant)
	return err
}

// shareRevokeLock shares tenant's revoke lock, in an insert of a child
// before its parent's row is locked.
func shareRevokeLock(ctx context.Context, tx pgx.Tx, tenant uuid.UUID) error {
	_, err := tx.Exec(ctx, sqlShareTenantLock, revokeLockClass, tenant)
	return err
}
