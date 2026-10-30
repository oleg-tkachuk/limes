package pgstore

import (
	"bytes"
	"context"
	"slices"
	"strconv"
	"strings"
	"uuid"

	"github.com/jackc/pgx/v5"

	"github.com/oleg-tkachuk/limes"
)

// The columns RevokeMatching selects by (migrate/ddl/migrations/025_revoke_matching.sql),
// each given only when its selector is, so the query reads the index of
// the one given rather than testing the others row by row.
const (
	columnSubject    = "c.subject"
	columnOnBehalfOf = "c.on_behalf_of"
	columnClientID   = "c.client_id"
	columnSession    = "c.session"
	// columnRunID is the agent run's (migrate/ddl/migrations/029_revoke_by_run.sql).
	columnRunID = "c.run_id"
)

// sqlRevokeMatchingHead and sqlRevokeMatchingTail surround the selectors:
// the tenant's capabilities with no entry of their own, a batch of them in
// id order, locked in the order the meter locks a lineage's records, each
// given an entry. The revocation triggers mark their descendants revoked
// and notify, as for Revoke.
const (
	sqlRevokeMatchingHead = `
WITH due AS (
    SELECT c.id FROM limes.capabilities c
    WHERE c.tenant_id = $1
      AND NOT EXISTS (SELECT 1 FROM limes.revocations r WHERE r.capability_id = c.id)`
	sqlRevokeMatchingTail = `
    ORDER BY c.id
    LIMIT $2
    FOR UPDATE OF c
)
INSERT INTO limes.revocations (capability_id, tenant_id, revoked_at, note, actor, cascaded)
SELECT id, $1, $3, $4, $5, false FROM due
ON CONFLICT (capability_id) DO NOTHING`
)

// A revocation across tenants (docs/adr/0014-bulk-revocation-across-tenants.md)
// runs in two statements: it selects a batch of matches, in order of tenant
// and id and locking nothing, then takes the revoke lock of each tenant the
// batch holds — in that order, as every revocation across tenants does —
// before it locks a row, as a revoke in one tenant does, and writes an entry
// for each match that still has none.
const (
	// sqlSelectEveryTenantHead and sqlSelectEveryTenantTail surround the
	// selectors of a batch across tenants, its limit $1.
	sqlSelectEveryTenantHead = `
SELECT c.id, c.tenant_id FROM limes.capabilities c
WHERE NOT EXISTS (SELECT 1 FROM limes.revocations r WHERE r.capability_id = c.id)`
	sqlSelectEveryTenantTail = `
ORDER BY c.tenant_id, c.id
LIMIT $1`

	// sqlRevokeSelected writes an entry, in each capability's own tenant,
	// for those of ids $1 that still have none, locked in the order they
	// were selected, and returns each entry's tenant.
	sqlRevokeSelected = `
WITH due AS (
    SELECT c.id, c.tenant_id FROM limes.capabilities c
    WHERE c.id = ANY ($1)
      AND NOT EXISTS (SELECT 1 FROM limes.revocations r WHERE r.capability_id = c.id)
    ORDER BY c.tenant_id, c.id
    FOR UPDATE OF c
)
INSERT INTO limes.revocations (capability_id, tenant_id, revoked_at, note, actor, cascaded)
SELECT id, tenant_id, $2, $3, $4, false FROM due
ON CONFLICT (capability_id) DO NOTHING
RETURNING tenant_id`
)

// maxTenantsPerBatch bounds the tenants one batch across tenants revokes in,
// and so the advisory locks its transaction holds, well within PostgreSQL's
// max_locks_per_transaction (64 by default); a batch cut short says More.
const maxTenantsPerBatch = 16

// selectorsSQL is the conditions of req's selectors, numbered from the
// arguments in args, and args with their values.
func selectorsSQL(req limes.RevokeMatchingRequest, args []any) (string, []any) {
	return selectorConditionsSQL(req.Subject, req.OnBehalfOf, req.ClientID, req.Session, req.RunID, args)
}

// selectorConditionsSQL is a condition on capabilities c for each selector
// set, numbered from the arguments in args, and args with their values.
func selectorConditionsSQL(subject, onBehalfOf, clientID, session string, run uuid.UUID, args []any) (string, []any) {
	var where strings.Builder
	for _, sel := range selectorConditions(subject, onBehalfOf, clientID, session, run) {
		args = append(args, sel.value)
		where.WriteString("\n      AND " + sel.column + " = $" + strconv.Itoa(len(args)))
	}
	return where.String(), args
}

// revokeMatchingSQL is the statement for req's selectors in one tenant, and
// its arguments.
func revokeMatchingSQL(req limes.RevokeMatchingRequest, now any) (string, []any) {
	where, args := selectorsSQL(req, []any{req.TenantID, req.BatchLimit(), now, req.Note, req.Actor})
	return sqlRevokeMatchingHead + where + sqlRevokeMatchingTail, args
}

// selectEveryTenantSQL is the selection of a batch across tenants for req's
// selectors, and its arguments.
func selectEveryTenantSQL(req limes.RevokeMatchingRequest) (string, []any) {
	where, args := selectorsSQL(req, []any{req.BatchLimit()})
	return sqlSelectEveryTenantHead + where + sqlSelectEveryTenantTail, args
}

// selected is one match of a batch across tenants.
type selected struct {
	ID     uuid.UUID
	Tenant uuid.UUID
}

// firstTenants is the matches of the first maxTenantsPerBatch tenants of
// batch, which is in order of tenant, those tenants, and whether any match
// was left out.
func firstTenants(batch []selected) ([]uuid.UUID, []uuid.UUID, bool) {
	var ids, tenants []uuid.UUID
	for _, m := range batch {
		if len(tenants) == 0 || tenants[len(tenants)-1] != m.Tenant {
			if len(tenants) == maxTenantsPerBatch {
				return ids, tenants, true
			}
			tenants = append(tenants, m.Tenant)
		}
		ids = append(ids, m.ID)
	}
	return ids, tenants, false
}

// RevokeMatching implements limes.BulkRevoker, scoped to the request's
// tenant as every call naming a tenant is (Config.CheckScope): under the
// tenant's revoke lock, as Revoke, an entry for a batch of its matching
// capabilities that have none. With AllTenants it runs under a scope that
// admits writing every tenant (WithAllTenants), refusing any narrower as
// limes.ErrScopeMismatch, and a batch spans at most maxTenantsPerBatch
// tenants, each under its revoke lock.
func (s *Store) RevokeMatching(ctx context.Context, req limes.RevokeMatchingRequest) (limes.RevokeMatchingResult, error) {
	if err := req.Validate(); err != nil {
		return limes.RevokeMatchingResult{}, err
	}
	if req.AllTenants {
		return s.revokeEveryTenant(ctx, req)
	}
	sql, args := revokeMatchingSQL(req, s.clock())
	var revoked int64
	err := s.inTenantTx(ctx, req.TenantID, func(tx pgx.Tx) error {
		if err := lockForRevoke(ctx, tx, req.TenantID); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, sql, args...)
		revoked = tag.RowsAffected()
		return err
	})
	if err != nil {
		return limes.RevokeMatchingResult{}, err
	}
	res := limes.RevokeMatchingResult{Revoked: revoked, More: revoked >= int64(req.BatchLimit())}
	if revoked > 0 {
		res.Tenants = []uuid.UUID{req.TenantID}
	}
	return res, nil
}

// revokeEveryTenant is RevokeMatching across tenants: a batch selected, its
// tenants' revoke locks taken in order, then an entry for each match still
// without one. More reports a full selection or one cut at
// maxTenantsPerBatch, not the entries written, which a revoke between the
// two statements may have made fewer.
func (s *Store) revokeEveryTenant(ctx context.Context, req limes.RevokeMatchingRequest) (limes.RevokeMatchingResult, error) {
	sql, args := selectEveryTenantSQL(req)
	var res limes.RevokeMatchingResult
	err := s.inEveryTenantTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, sql, args...)
		if err != nil {
			return err
		}
		batch, err := pgx.CollectRows(rows, pgx.RowToStructByPos[selected])
		if err != nil {
			return err
		}
		ids, tenants, cut := firstTenants(batch)
		res.More = cut || len(batch) >= req.BatchLimit()
		for _, tenant := range tenants {
			if err := lockForRevoke(ctx, tx, tenant); err != nil {
				return err
			}
		}
		rows, err = tx.Query(ctx, sqlRevokeSelected, ids, s.clock(), req.Note, req.Actor)
		if err != nil {
			return err
		}
		written, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
		if err != nil {
			return err
		}
		res.Revoked = int64(len(written))
		res.Tenants = distinctTenants(written)
		return nil
	})
	if err != nil {
		return limes.RevokeMatchingResult{}, err
	}
	return res, nil
}

// distinctTenants is each tenant of written once, in ascending order.
func distinctTenants(written []uuid.UUID) []uuid.UUID {
	slices.SortFunc(written, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
	return slices.Compact(written)
}

var _ limes.BulkRevoker = (*Store)(nil)
