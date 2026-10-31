package pgstore

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/oleg-tkachuk/limes/meter"
)

// sqlMatchingHoldsHead and sqlMatchingHoldsTail surround the selectors of
// ReleaseMatching (selectorsSQL, on the capability c): tenant $1's open
// holds — every tenant's, $1 being NULL — a batch of $2 in id order, each
// of a revoked capability h that is c, or was delegated from it — a hold's
// ancestors are those it holds against, in its own tenant — locked in id
// order, the order every call that locks several takes.
const (
	sqlMatchingHoldsHead = `
SELECT ` + sqlHoldColumns + ` FROM limes.reservations
WHERE id IN (
    SELECT r.id FROM limes.reservations r
    JOIN limes.capabilities h ON h.id = r.capability_id
    WHERE ($1::uuid IS NULL OR r.tenant_id = $1) AND h.revoked
      AND EXISTS (
        SELECT 1 FROM limes.capabilities c
        WHERE c.id = ANY (r.capability_id || r.ancestors) AND c.tenant_id = r.tenant_id`
	sqlMatchingHoldsTail = `)
    ORDER BY r.id
    LIMIT $2)
ORDER BY id
FOR UPDATE`
)

// ReleaseMatching implements meter.MatchingReleaser in one transaction
// scoped to the request's tenant (Config.CheckScope) — or, with AllTenants,
// to writing every tenant (WithAllTenants), any narrower scope refused as
// limes.ErrScopeMismatch: the holds selected are released as ReleaseExpired
// releases its own, their counters locked in the one order every call
// takes, tenant by tenant. Those locks are on rows, so a batch across
// tenants needs no bound on its tenants, as RevokeMatching's does.
func (s *UsageStore) ReleaseMatching(ctx context.Context, req meter.ReleaseMatchingRequest) (meter.ReleaseMatchingResult, error) {
	if err := req.Validate(); err != nil {
		return meter.ReleaseMatchingResult{}, err
	}
	limit := meter.HousekeepingLimit(req.Limit)
	var tenant any
	inTx := func(fn func(pgx.Tx) error) error { return s.inEveryTenantTx(ctx, fn) }
	if !req.AllTenants {
		tenant = req.TenantID
		inTx = func(fn func(pgx.Tx) error) error { return s.inTenantTx(ctx, req.TenantID, fn) }
	}
	where, args := selectorConditionsSQL(req.Subject, req.OnBehalfOf, req.ClientID, req.Session, req.RunID, []any{tenant, limit})
	var res meter.ReleaseMatchingResult
	err := inTx(func(tx pgx.Tx) error {
		holds, err := collectHolds(ctx, tx, sqlMatchingHoldsHead+where+sqlMatchingHoldsTail, args...)
		if err != nil {
			return err
		}
		if err := s.releaseHolds(ctx, tx, holds); err != nil {
			return err
		}
		res.Released = int64(len(holds))
		for _, h := range holds {
			res.Tenants = append(res.Tenants, h.tenantID)
		}
		return nil
	})
	if err != nil {
		return meter.ReleaseMatchingResult{}, err
	}
	if len(res.Tenants) > 0 {
		res.Tenants = sortedUnique(res.Tenants)
	}
	res.More = res.Released >= int64(limit)
	return res, nil
}

var _ meter.MatchingReleaser = (*UsageStore)(nil)
