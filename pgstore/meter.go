package pgstore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/oleg-tkachuk/limes"
)

// UsageStore implements limes.UsageStore[pgx.Tx] and limes.CopyUsageReader:
// request and spend counters, holds, the charges ledger and the tenant
// aggregates. It reads each capability's lineage and ceilings from the
// records a Store over the same database keeps.
//
// Every call locks what it changes in one order — a hold, then the tenant
// aggregate, then the capabilities' counters by id, then the copies' by
// revocation id — so two calls never wait on each other in a cycle. onCharged
// runs on the call's own transaction: it is the pgx.Tx it is handed.
type UsageStore struct {
	conn
}

// NewUsageStore returns a usage store over cfg.DB, whose schema Migrate
// created.
func NewUsageStore(cfg Config) (*UsageStore, error) {
	c, err := newConn(cfg)
	if err != nil {
		return nil, err
	}
	return &UsageStore{c}, nil
}

// ancestor is a capability above the one metered, with the ceilings its
// record carries.
type ancestor struct {
	id       uuid.UUID
	requests limes.RequestCeiling
	budget   limes.BudgetCeiling
}

const (
	// sqlLineage walks up from $1 to one ancestor past $2
	// (limes.MaxDelegationDepth), nearest first.
	sqlLineage = `
WITH RECURSIVE chain (id, parent_id, depth) AS (
    SELECT id, parent_id, 0 FROM limes.capabilities WHERE id = $1
  UNION ALL
    SELECT c.id, c.parent_id, chain.depth + 1
    FROM limes.capabilities c
    JOIN chain ON c.id = chain.parent_id
    WHERE chain.depth <= $2
)
SELECT id FROM chain ORDER BY depth`

	// sqlCapabilityExpiry reads when a capability's record expires, NULL for
	// one that never does.
	sqlCapabilityExpiry = `SELECT expires_at FROM limes.capabilities WHERE id = $1`

	// sqlHoldRecords locks the records of a lineage for share, in id order,
	// so Purge cannot drop one while the call counts against it.
	sqlHoldRecords = `
SELECT id, capability -> 'caveats' -> 'requests', capability -> 'caveats' -> 'budget'
FROM limes.capabilities
WHERE id = ANY ($1)
ORDER BY id
FOR SHARE`
)

// lineage returns capID's ancestors still on record, nearest first, holding
// their records for the rest of tx. ErrNotFound when capID itself is not on
// record: with no record its ancestors cannot be read, and metering it alone
// would let it spend past every live ancestor's ceiling.
func lineage(ctx context.Context, tx pgx.Tx, capID uuid.UUID) ([]ancestor, error) {
	rows, err := tx.Query(ctx, sqlLineage, capID, limes.MaxDelegationDepth)
	if err != nil {
		return nil, err
	}
	chain, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return nil, err
	}
	if len(chain) == 0 {
		return nil, fmt.Errorf("%w: pgstore: capability %s is not on record", limes.ErrNotFound, capID)
	}
	if len(chain) > limes.MaxDelegationDepth+1 {
		return nil, fmt.Errorf("%w: %s has more than %d ancestors", limes.ErrDelegationTooDeep, capID, limes.MaxDelegationDepth)
	}
	rows, err = tx.Query(ctx, sqlHoldRecords, chain)
	if err != nil {
		return nil, err
	}
	held := map[uuid.UUID]ancestor{}
	for rows.Next() {
		var id uuid.UUID
		var requests, budget ceilingRecord
		if err := rows.Scan(&id, &requests, &budget); err != nil {
			rows.Close()
			return nil, err
		}
		held[id] = ancestor{
			id:       id,
			requests: limes.RequestCeiling{Max: requests.Max, Unlimited: requests.Unlimited},
			budget:   limes.BudgetCeiling{Max: limes.Nanos(budget.Max), Unlimited: budget.Unlimited},
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if _, ok := held[capID]; !ok {
		return nil, fmt.Errorf("%w: pgstore: capability %s is not on record", limes.ErrNotFound, capID)
	}
	// Nearest first, stopping at the first ancestor gone since the walk.
	var out []ancestor
	for _, id := range chain[1:] {
		a, ok := held[id]
		if !ok {
			break
		}
		out = append(out, a)
	}
	return out, nil
}

// usageRow is one capability's counters.
type usageRow struct {
	id       uuid.UUID
	requests int64
	spent    limes.Nanos
	reserved limes.Nanos
	unit     string
}

// copyRow is one Biscuit copy's counters.
type copyRow struct {
	revocationID []byte
	capID        uuid.UUID
	requests     int64
	spent        limes.Nanos
	reserved     limes.Nanos
}

// budgetRow is a tenant aggregate.
type budgetRow struct {
	tenantID  uuid.UUID
	ceiling   limes.BudgetCeiling
	spent     limes.Nanos
	reserved  limes.Nanos
	unit      string
	start     time.Time
	end       *time.Time
	updated   time.Time
	version   int64
	period    int64
	persisted bool // false for a tenant with no row yet
}

func (b budgetRow) budget() limes.TenantBudget {
	// The row keeps one unit, the tenant's; the ceiling is in it.
	ceiling := b.ceiling
	ceiling.UnitCode = b.unit
	out := limes.TenantBudget{
		TenantID: b.tenantID, Ceiling: ceiling, SpentAmount: b.spent, ReservedAmount: b.reserved,
		PeriodStart: b.start, UpdatedAt: b.updated, ResourceVersion: b.version,
	}
	if b.end != nil {
		out.PeriodEnd = *b.end
	}
	return out
}

const (
	sqlCreateUsage = `
INSERT INTO limes.usage (capability_id, tenant_id)
SELECT id, $2 FROM unnest($1::uuid[]) AS id ORDER BY id
ON CONFLICT (capability_id) DO NOTHING`

	sqlLockUsage = `
SELECT capability_id, request_count, spent, reserved, unit_code
FROM limes.usage WHERE capability_id = ANY ($1)
ORDER BY capability_id
FOR UPDATE`

	sqlWriteUsage = `
UPDATE limes.usage u
SET request_count = v.request_count, spent = v.spent, reserved = v.reserved, unit_code = v.unit_code
FROM unnest($1::uuid[], $2::bigint[], $3::bigint[], $4::bigint[], $5::text[])
    AS v (capability_id, request_count, spent, reserved, unit_code)
WHERE u.capability_id = v.capability_id`

	sqlCreateCopies = `
INSERT INTO limes.copy_usage (revocation_id, capability_id, tenant_id)
SELECT id, $2, $3 FROM unnest($1::bytea[]) AS id ORDER BY id
ON CONFLICT (revocation_id) DO NOTHING`

	sqlLockCopies = `
SELECT revocation_id, capability_id, request_count, spent, reserved
FROM limes.copy_usage WHERE revocation_id = ANY ($1)
ORDER BY revocation_id
FOR UPDATE`

	sqlWriteCopies = `
UPDATE limes.copy_usage c
SET capability_id = v.capability_id, request_count = v.request_count, spent = v.spent, reserved = v.reserved
FROM unnest($1::bytea[], $2::uuid[], $3::bigint[], $4::bigint[], $5::bigint[])
    AS v (revocation_id, capability_id, request_count, spent, reserved)
WHERE c.revocation_id = v.revocation_id`

	// sqlCreateTenant starts a tenant's aggregate with no ceiling: it counts
	// from the first charge or hold, so a ceiling set later is measured
	// against what the tenant already spent and holds.
	sqlCreateTenant = `
INSERT INTO limes.tenant_budgets (tenant_id, ceiling_max, ceiling_unlimited, unit_code, period_start, updated_at)
VALUES ($1, 0, true, $2, $3, $3)
ON CONFLICT (tenant_id) DO NOTHING`

	sqlLockTenant = `
SELECT tenant_id, ceiling_max, ceiling_unlimited, spent, reserved, unit_code,
       period_start, period_end, updated_at, resource_version, period
FROM limes.tenant_budgets WHERE tenant_id = $1
FOR UPDATE`

	sqlWriteTenant = `
INSERT INTO limes.tenant_budgets
    (tenant_id, ceiling_max, ceiling_unlimited, spent, reserved, unit_code,
     period_start, period_end, updated_at, resource_version, period)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
ON CONFLICT (tenant_id) DO UPDATE SET
    ceiling_max = EXCLUDED.ceiling_max, ceiling_unlimited = EXCLUDED.ceiling_unlimited,
    spent = EXCLUDED.spent, reserved = EXCLUDED.reserved, unit_code = EXCLUDED.unit_code,
    period_start = EXCLUDED.period_start, period_end = EXCLUDED.period_end,
    updated_at = EXCLUDED.updated_at, resource_version = EXCLUDED.resource_version,
    period = EXCLUDED.period`
)

// counters is every row one call reads and changes, locked for the rest of
// its transaction.
type counters struct {
	tenant budgetRow
	usage  map[uuid.UUID]*usageRow
	copies map[string]*copyRow
}

// usageOf is the counters of id, zero when it has none.
func (c *counters) usageOf(id uuid.UUID) usageRow {
	if u, ok := c.usage[id]; ok {
		return *u
	}
	return usageRow{id: id}
}

// copyOf is the counters of a copy, zero when it has none.
func (c *counters) copyOf(id []byte) copyRow {
	if r, ok := c.copies[string(id)]; ok {
		return *r
	}
	return copyRow{revocationID: id}
}

// lockTenant reads the tenant's aggregate for update; persisted is false when
// it has no row.
func lockTenant(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) (budgetRow, error) {
	return readTenant(ctx, tx, sqlLockTenant, tenantID)
}

// lock reads, for update and in the one order every call takes, the tenant's
// aggregate — created first, without a ceiling, when create names a unit —
// then the counters of ids, created first for created, and the counters of
// copies, created first under capID. Counters of ids not created and not on
// record stay absent.
func lock(ctx context.Context, tx pgx.Tx, l lockRequest) (*counters, error) {
	c := &counters{usage: map[uuid.UUID]*usageRow{}, copies: map[string]*copyRow{}}
	if l.tenantID != uuid.Nil {
		if l.tenantUnit != "" {
			if _, err := tx.Exec(ctx, sqlCreateTenant, l.tenantID, l.tenantUnit, l.now); err != nil {
				return nil, err
			}
		}
		b, err := lockTenant(ctx, tx, l.tenantID)
		if err != nil {
			return nil, err
		}
		c.tenant = b
	}
	if len(l.created) > 0 {
		if _, err := tx.Exec(ctx, sqlCreateUsage, l.created, l.usageTenant); err != nil {
			return nil, err
		}
	}
	if ids := append(slices.Clone(l.created), l.existing...); len(ids) > 0 {
		rows, err := tx.Query(ctx, sqlLockUsage, ids)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			u := &usageRow{}
			var spent, reserved int64
			if err := rows.Scan(&u.id, &u.requests, &spent, &reserved, &u.unit); err != nil {
				rows.Close()
				return nil, err
			}
			u.spent, u.reserved = limes.Nanos(spent), limes.Nanos(reserved)
			c.usage[u.id] = u
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	if len(l.copies) > 0 {
		if l.createCopies {
			if _, err := tx.Exec(ctx, sqlCreateCopies, l.copies, l.copiesCapID, l.usageTenant); err != nil {
				return nil, err
			}
		}
		rows, err := tx.Query(ctx, sqlLockCopies, l.copies)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			r := &copyRow{}
			var spent, reserved int64
			if err := rows.Scan(&r.revocationID, &r.capID, &r.requests, &spent, &reserved); err != nil {
				rows.Close()
				return nil, err
			}
			r.spent, r.reserved = limes.Nanos(spent), limes.Nanos(reserved)
			c.copies[string(r.revocationID)] = r
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// lockRequest names what lock reads.
type lockRequest struct {
	now time.Time
	// tenantID is the aggregate to lock, none when nil; tenantUnit, when
	// set, creates it first.
	tenantID   uuid.UUID
	tenantUnit string
	// created are capabilities whose counters are created if missing, under
	// usageTenant; existing are locked only if they have counters.
	created, existing []uuid.UUID
	usageTenant       uuid.UUID
	// copies are revocation ids, created under copiesCapID when
	// createCopies.
	copies       [][]byte
	createCopies bool
	copiesCapID  uuid.UUID
}

// write stores every counter c holds, as it now stands.
func (c *counters) write(ctx context.Context, tx pgx.Tx, now time.Time, writeTenant bool) error {
	if writeTenant {
		b := c.tenant
		b.updated = now
		if _, err := tx.Exec(ctx, sqlWriteTenant,
			b.tenantID, int64(b.ceiling.Max), b.ceiling.Unlimited, int64(b.spent), int64(b.reserved), b.unit,
			b.start, b.end, b.updated, b.version, b.period); err != nil {
			return err
		}
		c.tenant = b
	}
	if len(c.usage) > 0 {
		var ids []uuid.UUID
		var requests, spent, reserved []int64
		var units []string
		for _, u := range c.usage {
			ids = append(ids, u.id)
			requests = append(requests, u.requests)
			spent = append(spent, int64(u.spent))
			reserved = append(reserved, int64(u.reserved))
			units = append(units, u.unit)
		}
		if _, err := tx.Exec(ctx, sqlWriteUsage, ids, requests, spent, reserved, units); err != nil {
			return err
		}
	}
	if len(c.copies) > 0 {
		var ids [][]byte
		var caps []uuid.UUID
		var requests, spent, reserved []int64
		for _, r := range c.copies {
			ids = append(ids, r.revocationID)
			caps = append(caps, r.capID)
			requests = append(requests, r.requests)
			spent = append(spent, int64(r.spent))
			reserved = append(reserved, int64(r.reserved))
		}
		if _, err := tx.Exec(ctx, sqlWriteCopies, ids, caps, requests, spent, reserved); err != nil {
			return err
		}
	}
	return nil
}

// copyIDs are the revocation ids of copies, in order.
func copyIDs(copies []limes.CopyCeiling) [][]byte {
	out := make([][]byte, 0, len(copies))
	for _, c := range copies {
		out = append(out, c.RevocationID)
	}
	return out
}

func ancestorIDs(ancestors []ancestor) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(ancestors))
	for _, a := range ancestors {
		out = append(out, a.id)
	}
	return out
}

func max0(n limes.Nanos) limes.Nanos {
	if n < 0 {
		return 0
	}
	return n
}

// heldCopyRecord is one Biscuit copy a hold is on, as the copies column
// holds it.
type heldCopyRecord struct {
	RevocationID []byte         `json:"revocation_id"`
	Requests     *ceilingRecord `json:"requests"`
	Budget       *ceilingRecord `json:"budget"`
}

func encodeHeldCopies(copies []limes.CopyCeiling) ([]byte, error) {
	out := make([]heldCopyRecord, 0, len(copies))
	for _, c := range copies {
		r := heldCopyRecord{RevocationID: c.RevocationID}
		if c.Requests != nil {
			r.Requests = &ceilingRecord{Max: c.Requests.Max, Unlimited: c.Requests.Unlimited}
		}
		if c.Budget != nil {
			r.Budget = &ceilingRecord{Max: int64(c.Budget.Max), Unlimited: c.Budget.Unlimited}
		}
		out = append(out, r)
	}
	return json.Marshal(out)
}

// decodeHeldCopies reads the copies of a hold in unit: a copy's budget is
// in its hold's unit, which the row keeps once, beside them.
func decodeHeldCopies(b []byte, unit string) ([]limes.CopyCeiling, error) {
	var records []heldCopyRecord
	if err := json.Unmarshal(b, &records); err != nil {
		return nil, fmt.Errorf("pgstore: decode held copies: %w", err)
	}
	if len(records) == 0 {
		return nil, nil
	}
	out := make([]limes.CopyCeiling, 0, len(records))
	for _, r := range records {
		c := limes.CopyCeiling{RevocationID: r.RevocationID}
		if r.Requests != nil {
			c.Requests = &limes.RequestCeiling{Max: r.Requests.Max, Unlimited: r.Requests.Unlimited}
		}
		if r.Budget != nil {
			c.Budget = &limes.BudgetCeiling{Max: limes.Nanos(r.Budget.Max), Unlimited: r.Budget.Unlimited, UnitCode: unit}
		}
		out = append(out, c)
	}
	return out, nil
}

// sameCopyIDs reports whether ids are the revocation ids of copies, in order:
// a hold is settled under the copies it was made under.
func sameCopyIDs(ids [][]byte, copies []limes.CopyCeiling) bool {
	return slices.EqualFunc(ids, copies, func(id []byte, c limes.CopyCeiling) bool { return bytes.Equal(id, c.RevocationID) })
}
