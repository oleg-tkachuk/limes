package pgstore

import (
	"context"
	"strconv"
	"strings"

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

// revokeMatchingSQL is the statement for req's selectors, and its
// arguments.
func revokeMatchingSQL(req limes.RevokeMatchingRequest, now any) (string, []any) {
	args := []any{req.TenantID, req.BatchLimit(), now, req.Note, req.Actor}
	var where strings.Builder
	for _, sel := range []struct{ column, value string }{
		{columnSubject, req.Subject}, {columnOnBehalfOf, req.OnBehalfOf},
		{columnClientID, req.ClientID}, {columnSession, req.Session},
	} {
		if sel.value == "" {
			continue
		}
		args = append(args, sel.value)
		where.WriteString("\n      AND " + sel.column + " = $" + strconv.Itoa(len(args)))
	}
	return sqlRevokeMatchingHead + where.String() + sqlRevokeMatchingTail, args
}

// RevokeMatching implements limes.BulkRevoker, scoped to the request's
// tenant as every call naming a tenant is (Config.CheckScope): under the
// tenant's revoke lock, as Revoke, an entry for a batch of its matching
// capabilities that have none.
func (s *Store) RevokeMatching(ctx context.Context, req limes.RevokeMatchingRequest) (limes.RevokeMatchingResult, error) {
	if err := req.Validate(); err != nil {
		return limes.RevokeMatchingResult{}, err
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
	return limes.RevokeMatchingResult{Revoked: revoked, More: revoked >= int64(req.BatchLimit())}, nil
}

var _ limes.BulkRevoker = (*Store)(nil)
