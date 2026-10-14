package pgstore

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/oleg-tkachuk/limes"
)

// The statements of an erase, in the order the meter locks: a tenant's
// holds, its records in id order, its aggregate, its counters in id order,
// its copies' counters, then the ledger, which no call locks. Each lock is
// taken before a row is written, so an erase and a meter call never wait on
// each other in a cycle.
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
SELECT revocation_id FROM limes.copy_usage WHERE tenant_id = $1 ORDER BY revocation_id FOR UPDATE`

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

// EraseTenant erases the tenant's records, as EraseTenantRecords does, and
// its usage, as UsageStore.EraseTenantUsage does, in one transaction, and
// runs onErased in it last, so a consumer removes its own rows of the tenant
// with them or not at all. An error from onErased rolls everything back.
func (s *Store) EraseTenant(ctx context.Context, req limes.EraseTenantRequest, onErased func(ctx context.Context, tx pgx.Tx) error) (limes.EraseTenantResult, error) {
	if err := req.Validate(); err != nil {
		return limes.EraseTenantResult{}, err
	}
	var out limes.EraseTenantResult
	err := s.inTenantTx(ctx, req.TenantID, func(tx pgx.Tx) error {
		// The holds are locked first, as a Settle locks its hold before
		// the records.
		if err := lockAll(ctx, tx, sqlLockTenantHolds, req); err != nil {
			return err
		}
		var err error
		if out, err = s.eraseRecords(ctx, tx, req); err != nil {
			return err
		}
		if err := eraseUsage(ctx, tx, limes.EraseTenantUsageRequest{TenantID: req.TenantID}); err != nil {
			return err
		}
		if onErased != nil {
			return onErased(ctx, tx)
		}
		return nil
	})
	return out, err
}

// eraseRecords revokes and removes in tx, its records locked first.
func (s *Store) eraseRecords(ctx context.Context, tx pgx.Tx, req limes.EraseTenantRequest) (limes.EraseTenantResult, error) {
	var out limes.EraseTenantResult
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

// EraseTenantUsage implements limes.TenantUsageEraser: it removes the
// tenant's holds, aggregate, counters, copies' counters and charges in one
// transaction scoped to the tenant (Config.CheckScope).
func (s *UsageStore) EraseTenantUsage(ctx context.Context, req limes.EraseTenantUsageRequest) error {
	if err := req.Validate(); err != nil {
		return err
	}
	return s.inTenantTx(ctx, req.TenantID, func(tx pgx.Tx) error {
		return eraseUsage(ctx, tx, req)
	})
}

// eraseUsage locks the tenant's meter rows in the meter's order, then
// removes them and the ledger.
func eraseUsage(ctx context.Context, tx pgx.Tx, req limes.EraseTenantUsageRequest) error {
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
	_ limes.TenantUsageEraser  = (*UsageStore)(nil)
)
