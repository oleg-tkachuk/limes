package pgstore

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/meter"
)

// A tenant with no ceiling counts its spend and holds in stripes rather
// than on its aggregate's one row, unless Config.DisableTenantStripes is
// set (docs/adr/0022-unlimited-tenant-aggregate.md): its calls read the row
// without locking it and add their deltas to the stripe of their
// connection, so they queue only on a stripe their connection shares. A
// call that locks the row folds the stripes into it first, whatever the
// switch says.

// tenantStripes is how many stripes a tenant's aggregate is split into: a
// connection writes stripe pg_backend_pid() modulo it. Two connections
// whose pids meet modulo it share a stripe and queue on it.
const tenantStripes = 64

// sqlStriped is the condition, over the aggregate's row b and under the
// switch $2, on which a tenant's calls stripe: no ceiling, and a unit, so
// that a call never has to write the unit to the row it does not lock.
// stripes is the same condition in Go; the two must agree.
const sqlStriped = `$2::boolean AND b.ceiling_unlimited AND b.unit_code <> ''`

const (
	// sqlFoldStripes folds tenant $1's stripes into its aggregate's row,
	// locking the row, unless its calls stripe under the switch $2: the
	// stripes are deleted and their sums added, the spend and the holds
	// clamped at zero as max0 clamps them. The deleted rows are the ones
	// the sums come from — DELETE … RETURNING returns a stripe's last
	// version once a call that wrote it commits — so no delta is lost to
	// a stripe written meanwhile. It returns how many stripes it folded,
	// and whether every one was of the row's period: no row when there was
	// none to fold.
	sqlFoldStripes = `
WITH folded AS (
    DELETE FROM limes.tenant_budget_stripes x
    WHERE x.tenant_id = $1
      AND NOT EXISTS (SELECT 1 FROM limes.tenant_budgets b WHERE b.tenant_id = $1 AND ` + sqlStriped + `)
    RETURNING x.spent, x.reserved, x.period, x.updated_at
), total AS (
    SELECT sum(spent) AS spent, sum(reserved) AS reserved, max(updated_at) AS updated_at,
           min(period) AS low, max(period) AS high, count(*) AS stripes
    FROM folded
)
UPDATE limes.tenant_budgets b
SET spent = GREATEST(b.spent + t.spent, 0), reserved = GREATEST(b.reserved + t.reserved, 0),
    updated_at = GREATEST(b.updated_at, t.updated_at)
FROM total t
WHERE b.tenant_id = $1 AND t.stripes > 0
RETURNING t.stripes, t.low = b.period AND t.high = b.period`

	// sqlLockUnstripedTenant reads tenant $1's aggregate for update unless
	// its calls stripe under the switch $2: no row then, nor when it has
	// none.
	sqlLockUnstripedTenant = `
SELECT b.tenant_id, b.ceiling_max, b.ceiling_unlimited, b.spent, b.reserved, b.unit_code,
       b.period_start, b.period_end, b.updated_at, b.resource_version, b.period
FROM limes.tenant_budgets b WHERE b.tenant_id = $1 AND NOT (` + sqlStriped + `)
FOR UPDATE`

	// sqlWriteStripe adds a call's deltas to the stripe of its connection
	// among $2, and returns the database's clock once the stripe is
	// written, which times the wait for it.
	sqlWriteStripe = `
INSERT INTO limes.tenant_budget_stripes (tenant_id, stripe, period, spent, reserved, updated_at)
VALUES ($1, pg_backend_pid() % $2, $3, $4, $5, $6)
ON CONFLICT (tenant_id, stripe) DO UPDATE SET
    spent = tenant_budget_stripes.spent + EXCLUDED.spent,
    reserved = tenant_budget_stripes.reserved + EXCLUDED.reserved,
    updated_at = GREATEST(tenant_budget_stripes.updated_at, EXCLUDED.updated_at)
RETURNING clock_timestamp()`
)

// logKeyStripes is how many stripes a fold folded, as a log line counts.
const logKeyStripes = "stripes"

// errStripesOfAnotherPeriod is a fold that found a stripe of another
// period than its aggregate's: every change of period folds first, so the
// store's own invariant is broken, and the call is refused rather than
// count one period's spend in another.
var errStripesOfAnotherPeriod = fmt.Errorf("%w: pgstore: a tenant's stripes are of another period than its aggregate", limes.ErrInternal)

// stripes reports whether the calls of the tenant whose aggregate is b
// stripe under the switch on: sqlStriped in Go.
func stripes(b budgetRow, on bool) bool {
	return on && b.persisted && b.ceiling.Unlimited && b.unit != ""
}

// scanFold reads sqlFoldStripes' answer: how many stripes it folded.
func scanFold(row pgx.Row) (int64, error) {
	var folded int64
	var samePeriod bool
	err := row.Scan(&folded, &samePeriod)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if !samePeriod {
		return folded, errStripesOfAnotherPeriod
	}
	return folded, nil
}

// foldTenant folds tenant's stripes into its aggregate's row in tx, whatever
// the switch says, for op, and logs what it folded.
func (s *UsageStore) foldTenant(ctx context.Context, tx pgx.Tx, op meter.StoreOperation, tenant uuid.UUID) error {
	folded, err := scanFold(tx.QueryRow(ctx, sqlFoldStripes, tenant, false))
	s.logFold(ctx, op, tenant, folded, err)
	return err
}

// logFold logs a fold of tenant's stripes for op: one that folded any at
// INFO, one that found the store's invariant broken at ERROR.
func (s *UsageStore) logFold(ctx context.Context, op meter.StoreOperation, tenant uuid.UUID, folded int64, err error) {
	switch {
	case errors.Is(err, errStripesOfAnotherPeriod):
		s.log.ErrorContext(ctx, "pgstore: refused a call: a tenant's stripes are of another period than its aggregate",
			slog.String(logKeyOp, string(op)), slog.String(logKeyTenant, tenant.String()), slog.Int64(logKeyStripes, folded))
	case err == nil && folded > 0:
		s.log.InfoContext(ctx, "pgstore: folded a tenant's stripes into its aggregate",
			slog.String(logKeyOp, string(op)), slog.String(logKeyTenant, tenant.String()), slog.Int64(logKeyStripes, folded))
	}
}

// logBulkFold logs the stripes a lock of many tenants' aggregates folded,
// when it folded any.
func (s *UsageStore) logBulkFold(ctx context.Context, folded int64) {
	if folded > 0 {
		s.log.InfoContext(ctx, "pgstore: folded tenants' stripes into their aggregates", slog.Int64(logKeyStripes, folded))
	}
}

// stripeStmts are the statements that add c's change of its tenant's
// aggregate to its stripe, timed: the database's clock, then the write,
// which reads it again once the stripe is written, and onWait called with
// the difference once the batch is sent. None when c changed nothing.
func (c *counters) stripeStmts(now time.Time) []heldStmt {
	t := c.tenant
	spent, reserved := t.spent-t.readSpent, t.reserved-t.readReserved
	if spent == 0 && reserved == 0 {
		return nil
	}
	var before, after time.Time
	scanInto := func(dest *time.Time) func(pgx.BatchResults) error {
		return func(br pgx.BatchResults) error { return br.QueryRow().Scan(dest) }
	}
	return []heldStmt{
		{sql: sqlClock, into: scanInto(&before)},
		{sql: sqlWriteStripe, args: []any{t.tenantID, tenantStripes, t.period, int64(spent), int64(reserved), now}, into: scanInto(&after),
			done: func() {
				if c.onStripeWait != nil {
					c.onStripeWait(max(after.Sub(before), 0))
				}
			}},
	}
}
