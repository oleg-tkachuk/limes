package pgstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/meter"
)

// UsageStore implements meter.UsageStore and meter.CopyUsageReader:
// request and spend counters, holds, the charges ledger and the tenant
// aggregates. It reads each capability's lineage and ceilings from the
// records a Store over the same database keeps.
//
// Every call locks what it changes in one order — holds by id, then the
// tenant aggregate, then the capabilities' counters by id, then the copies'
// by revocation id, then the ledger — so two calls never wait on each other
// in a cycle. onCharged
// runs on the call's own transaction: it is the pgx.Tx it is handed.
type UsageStore struct {
	conn
}

// NewUsageStore returns a usage store over cfg.DB, whose schema migrate.Run
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
	// copies is its quota of Biscuit copies; a record written before it was
	// kept has none, the zero quota.
	copies limes.CopyQuota
}

// lineageView is what the meter and the erase read of the records
// (migrate/ddl/migrations/022_capability_lineage.sql), never the records table.
const lineageView = schema + ".capability_lineage"

const (
	// sqlLineage walks up from $1 to one ancestor past $2
	// (limes.MaxDelegationDepth), nearest first.
	sqlLineage = `
WITH RECURSIVE chain (id, parent_id, depth) AS (
    SELECT id, parent_id, 0 FROM ` + lineageView + ` WHERE id = $1
  UNION ALL
    SELECT c.id, c.parent_id, chain.depth + 1
    FROM ` + lineageView + ` c
    JOIN chain ON c.id = chain.parent_id
    WHERE chain.depth <= $2
)
SELECT id FROM chain ORDER BY depth`

	// sqlCapabilityExpiry reads when a capability's record expires, NULL for
	// one that never does.
	sqlCapabilityExpiry = `SELECT expires_at FROM ` + lineageView + ` WHERE id = $1`

	// sqlHoldRecords locks the records of a lineage for key share, in id
	// order, so Purge cannot drop one while the call counts against it: the
	// view is a plain projection, so the lock reaches the record rows. Key
	// share conflicts with that delete and not with the revoked flag a
	// revocation sets (migrate/ddl/migrations/015_revoked_flag.sql): with FOR SHARE,
	// metered calls overlapping one another would hold a record without a
	// gap, and its revoke's mark would wait behind them for as long.
	sqlHoldRecords = `
SELECT id, requests, budget, copies
FROM ` + lineageView + `
WHERE id = ANY ($1)
ORDER BY id
FOR KEY SHARE`
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
	held, err := holdRecords(ctx, tx, chain)
	if err != nil {
		return nil, err
	}
	if _, ok := held[capID]; !ok {
		return nil, fmt.Errorf("%w: pgstore: capability %s is not on record", limes.ErrNotFound, capID)
	}
	return nearestFirst(chain[1:], held), nil
}

// holdRecords reads the ceilings of chain's capabilities still on record.
func holdRecords(ctx context.Context, tx pgx.Tx, chain []uuid.UUID) (map[uuid.UUID]ancestor, error) {
	rows, err := tx.Query(ctx, sqlHoldRecords, chain)
	if err != nil {
		return nil, err
	}
	scanned, err := pgx.CollectRows(rows, scanAncestor)
	if err != nil {
		return nil, err
	}
	held := make(map[uuid.UUID]ancestor, len(scanned))
	for _, a := range scanned {
		held[a.id] = a
	}
	return held, nil
}

// scanAncestor reads one capability's ceilings.
func scanAncestor(row pgx.CollectableRow) (ancestor, error) {
	var id uuid.UUID
	var requests, budget, copies ceilingRecord
	if err := row.Scan(&id, &requests, &budget, &copies); err != nil {
		return ancestor{}, err
	}
	return ancestor{
		id:       id,
		requests: limes.RequestCeiling{Max: requests.Max, Unlimited: requests.Unlimited},
		budget:   limes.BudgetCeiling{Max: limes.Nanos(budget.Max), Unlimited: budget.Unlimited},
		copies:   limes.CopyQuota{Max: copies.Max, Unlimited: copies.Unlimited},
	}, nil
}

// nearestFirst is the ancestors of ids held, nearest first, stopping at the
// first one gone since the walk.
func nearestFirst(ids []uuid.UUID, held map[uuid.UUID]ancestor) []ancestor {
	var out []ancestor
	for _, id := range ids {
		a, ok := held[id]
		if !ok {
			break
		}
		out = append(out, a)
	}
	return out
}

// usageRow is one capability's counters.
type usageRow struct {
	id       uuid.UUID
	requests int64
	spent    limes.Nanos
	reserved limes.Nanos
	unit     string
	// copiesUnder counts the copies of the capability and of those
	// delegated from it.
	copiesUnder int64
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

func (b budgetRow) budget() meter.TenantBudget {
	// The row keeps one unit, the tenant's; the ceiling is in it.
	ceiling := b.ceiling
	ceiling.UnitCode = b.unit
	out := meter.TenantBudget{
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
SELECT capability_id, request_count, spent, reserved, unit_code, copies_under
FROM limes.usage WHERE capability_id = ANY ($1)
ORDER BY capability_id
FOR UPDATE`

	sqlWriteUsage = `
UPDATE limes.usage u
SET request_count = v.request_count, spent = v.spent, reserved = v.reserved, unit_code = v.unit_code,
    copies_under = v.copies_under
FROM unnest($1::uuid[], $2::bigint[], $3::bigint[], $4::bigint[], $5::text[], $6::bigint[])
    AS v (capability_id, request_count, spent, reserved, unit_code, copies_under)
WHERE u.capability_id = v.capability_id`

	sqlCreateCopies = `
INSERT INTO limes.copy_usage (revocation_id, capability_id, tenant_id)
SELECT id, $2, $3 FROM unnest($1::bytea[]) AS id ORDER BY id
ON CONFLICT (capability_id, revocation_id) DO NOTHING`

	sqlCountCopies = `SELECT count(*) FROM limes.copy_usage WHERE capability_id = $1`

	sqlLockCopies = `
SELECT revocation_id, capability_id, request_count, spent, reserved
FROM limes.copy_usage WHERE capability_id = $1 AND revocation_id = ANY ($2)
ORDER BY revocation_id
FOR UPDATE`

	sqlWriteCopies = `
UPDATE limes.copy_usage c
SET request_count = v.request_count, spent = v.spent, reserved = v.reserved
FROM unnest($1::bytea[], $2::uuid[], $3::bigint[], $4::bigint[], $5::bigint[])
    AS v (revocation_id, capability_id, request_count, spent, reserved)
WHERE c.capability_id = v.capability_id AND c.revocation_id = v.revocation_id`

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
	if err := lockTenantOf(ctx, tx, l, c); err != nil {
		return nil, err
	}
	if err := lockUsageOf(ctx, tx, l, c); err != nil {
		return nil, err
	}
	if len(l.copies) == 0 {
		return c, nil
	}
	if l.createCopies {
		if err := createCopies(ctx, tx, l, c); err != nil {
			return nil, err
		}
	}
	if err := lockCopiesOf(ctx, tx, l, c); err != nil {
		return nil, err
	}
	return c, nil
}

// lockTenantOf reads l's tenant aggregate into c, created first, without a
// ceiling, when l names its unit; nothing when l names no tenant.
func lockTenantOf(ctx context.Context, tx pgx.Tx, l lockRequest, c *counters) error {
	if l.tenantID == uuid.Nil() {
		return nil
	}
	if l.tenantUnit != "" {
		if _, err := tx.Exec(ctx, sqlCreateTenant, l.tenantID, l.tenantUnit, l.now); err != nil {
			return err
		}
	}
	b, err := lockTenant(ctx, tx, l.tenantID)
	if err != nil {
		return err
	}
	c.tenant = b
	return nil
}

// lockUsageOf reads the usage counters of l's ids into c, created first for
// l.created. A row created that the transaction cannot see is another
// tenant's under the same id, which ON CONFLICT DO NOTHING left in place:
// refused, never counted as zero or written through nil.
func lockUsageOf(ctx context.Context, tx pgx.Tx, l lockRequest, c *counters) error {
	if len(l.created) > 0 {
		if _, err := tx.Exec(ctx, sqlCreateUsage, l.created, l.usageTenant); err != nil {
			return err
		}
	}
	ids := append(slices.Clone(l.created), l.existing...)
	if len(ids) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, sqlLockUsage, ids)
	if err != nil {
		return err
	}
	scanned, err := pgx.CollectRows(rows, scanUsage)
	if err != nil {
		return err
	}
	for _, u := range scanned {
		c.usage[u.id] = u
	}
	for _, id := range l.created {
		if _, ok := c.usage[id]; !ok {
			return fmt.Errorf("%w: %s", errCounterHidden, id)
		}
	}
	return nil
}

// scanUsage reads one locked usage counter.
func scanUsage(row pgx.CollectableRow) (*usageRow, error) {
	u := &usageRow{}
	var spent, reserved int64
	if err := row.Scan(&u.id, &u.requests, &spent, &reserved, &u.unit, &u.copiesUnder); err != nil {
		return nil, err
	}
	u.spent, u.reserved = limes.Nanos(spent), limes.Nanos(reserved)
	return u, nil
}

// createCopies creates the counters of l's copies under l.copiesCapID.
// Copies new to the capability count against its quota, and each
// capability on the chain counts the copies of everything delegated from it
// against its own, on its usage row: locked above, it keeps two calls under
// one ancestor from counting past it together.
func createCopies(ctx context.Context, tx pgx.Tx, l lockRequest, c *counters) error {
	tag, err := tx.Exec(ctx, sqlCreateCopies, l.copies, l.copiesCapID, l.usageTenant)
	if err != nil {
		return err
	}
	added := tag.RowsAffected()
	if added == 0 {
		return nil
	}
	var counted int64
	if err := tx.QueryRow(ctx, sqlCountCopies, l.copiesCapID).Scan(&counted); err != nil {
		return err
	}
	if counted > l.maxCopies {
		return fmt.Errorf("%w: capability %s would count %d, quota %d", meter.ErrCopyLimitExceeded, l.copiesCapID, counted, l.maxCopies)
	}
	return countChainCopies(l, c, added)
}

// countChainCopies adds added copies to what each capability on l's chain
// counts under it, refusing a count past its quota.
func countChainCopies(l lockRequest, c *counters, added int64) error {
	for _, q := range l.chainQuotas {
		u, ok := c.usage[q.id]
		if !ok {
			return fmt.Errorf("%w: %s", errCounterHidden, q.id)
		}
		u.copiesUnder += added
		if !q.quota.Admits(u.copiesUnder) {
			return fmt.Errorf("%w: capability %s and those delegated from it would count %d, quota %s", meter.ErrCopyLimitExceeded, q.id, u.copiesUnder, q.quota)
		}
	}
	return nil
}

// lockCopiesOf reads the counters of l's copies into c; when they were
// created, one the transaction cannot see is refused.
func lockCopiesOf(ctx context.Context, tx pgx.Tx, l lockRequest, c *counters) error {
	rows, err := tx.Query(ctx, sqlLockCopies, l.copiesCapID, l.copies)
	if err != nil {
		return err
	}
	scanned, err := pgx.CollectRows(rows, scanCopy)
	if err != nil {
		return err
	}
	for _, r := range scanned {
		c.copies[string(r.revocationID)] = r
	}
	if !l.createCopies {
		return nil
	}
	for _, id := range l.copies {
		if _, ok := c.copies[string(id)]; !ok {
			return fmt.Errorf("%w: copy %x of %s", errCounterHidden, id, l.copiesCapID)
		}
	}
	return nil
}

// scanCopy reads one locked copy counter.
func scanCopy(row pgx.CollectableRow) (*copyRow, error) {
	r := &copyRow{}
	var spent, reserved int64
	if err := row.Scan(&r.revocationID, &r.capID, &r.requests, &spent, &reserved); err != nil {
		return nil, err
	}
	r.spent, r.reserved = limes.Nanos(spent), limes.Nanos(reserved)
	return r, nil
}

// errCounterHidden is a counter row lock created that the transaction cannot
// see: one of another tenant holds the same id. Ids are random or signed, so
// it means a store written outside pgstore, and the call is refused.
var errCounterHidden = errors.New("pgstore: a counter row of the same id belongs to another tenant")

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
	// copies are revocation ids of copies of copiesCapID, created first when
	// createCopies. A copy is counted under its capability: a block's
	// revocation id alone can turn up on another capability's chain.
	copies       [][]byte
	createCopies bool
	// maxCopies is the quota of copies per capability — the capability's
	// own, bounded by the store's — which createCopies may not take
	// copiesCapID past.
	maxCopies   int64
	copiesCapID uuid.UUID
	// chainQuotas are the quotas of copiesCapID and its ancestors, each
	// counting the copies of its whole subtree on its usage row.
	chainQuotas []chainQuota
}

// chainQuota is one capability's quota of copies, over its subtree.
type chainQuota struct {
	id    uuid.UUID
	quota limes.CopyQuota
}

// quotasOf is the chain's quotas: self's, from its token, then each
// ancestor's, from its record.
func quotasOf(self uuid.UUID, quota limes.CopyQuota, ancestors []ancestor) []chainQuota {
	out := []chainQuota{{self, quota}}
	for _, a := range ancestors {
		out = append(out, chainQuota{a.id, a.copies})
	}
	return out
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
		var requests, spent, reserved, copiesUnder []int64
		var units []string
		for _, u := range c.usage {
			ids = append(ids, u.id)
			requests = append(requests, u.requests)
			spent = append(spent, int64(u.spent))
			reserved = append(reserved, int64(u.reserved))
			units = append(units, u.unit)
			copiesUnder = append(copiesUnder, u.copiesUnder)
		}
		if _, err := tx.Exec(ctx, sqlWriteUsage, ids, requests, spent, reserved, units, copiesUnder); err != nil {
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
