package pgstore

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/oleg-tkachuk/limes/meter"
)

// sqlMatchingHoldsHead and sqlMatchingHoldsTail surround the selectors of
// ReleaseMatching (selectorsSQL, on the capability c): tenant $1's open
// holds, a batch of $2 in id order, each of a revoked capability h that is
// c, or was delegated from it — a hold's ancestors are those it holds
// against — locked in id order, the order every call that locks several
// takes.
const (
	sqlMatchingHoldsHead = `
SELECT ` + sqlHoldColumns + ` FROM limes.reservations
WHERE id IN (
    SELECT r.id FROM limes.reservations r
    JOIN limes.capabilities h ON h.id = r.capability_id
    WHERE r.tenant_id = $1 AND h.revoked
      AND EXISTS (
        SELECT 1 FROM limes.capabilities c
        WHERE c.id = ANY (r.capability_id || r.ancestors) AND c.tenant_id = $1`
	sqlMatchingHoldsTail = `)
    ORDER BY r.id
    LIMIT $2)
ORDER BY id
FOR UPDATE`
)

// ReleaseMatching implements meter.MatchingReleaser in one transaction
// scoped to the request's tenant (Config.CheckScope): the holds selected
// are released as ReleaseExpired releases its own, their counters locked
// in the one order every call takes.
func (s *UsageStore) ReleaseMatching(ctx context.Context, req meter.ReleaseMatchingRequest) (meter.ReleaseMatchingResult, error) {
	if err := req.Validate(); err != nil {
		return meter.ReleaseMatchingResult{}, err
	}
	limit := meter.HousekeepingLimit(req.Limit)
	where, args := selectorConditionsSQL(req.Subject, req.OnBehalfOf, req.ClientID, req.Session, req.RunID, []any{req.TenantID, limit})
	var released int64
	err := s.inTenantTx(ctx, req.TenantID, func(tx pgx.Tx) error {
		holds, err := collectHolds(ctx, tx, sqlMatchingHoldsHead+where+sqlMatchingHoldsTail, args...)
		if err != nil {
			return err
		}
		if err := s.releaseHolds(ctx, tx, holds); err != nil {
			return err
		}
		released = int64(len(holds))
		return nil
	})
	if err != nil {
		return meter.ReleaseMatchingResult{}, err
	}
	return meter.ReleaseMatchingResult{Released: released, More: released >= int64(limit)}, nil
}

var _ meter.MatchingReleaser = (*UsageStore)(nil)
