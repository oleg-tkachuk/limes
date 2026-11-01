package pgstore

import (
	"cmp"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/meter"
)

// Refund returns spend from one ledger entry to the counters it debited.
func (s *UsageStore) Refund(ctx context.Context, req meter.RefundRequest) (meter.RefundReceipt, error) {
	if err := req.Validate(); err != nil {
		return meter.RefundReceipt{}, err
	}
	var receipt meter.RefundReceipt
	err := s.retryDeadlock(ctx, retriedRefund, func() error {
		return s.inTx(ctx, func(tx pgx.Tx) error {
			e, c, err := s.lockRefunded(ctx, tx, req.ChargeID)
			if err != nil {
				return err
			}
			receipt = meter.RefundReceipt{ChargeID: e.ID, TenantID: e.TenantID, UnitCode: e.UnitCode}
			amount, err := refundAmount(e, req)
			if err != nil || amount == 0 {
				return err
			}
			if err := s.refund(ctx, tx, c, e, amount); err != nil {
				return err
			}
			receipt.Amount = amount
			return nil
		})
	})
	if err != nil {
		return meter.RefundReceipt{}, err
	}
	return receipt, nil
}

// lockRefunded reads the charge id and locks the counters it debited, then
// the charge itself. It is read once to learn which counters those are,
// which are locked first, and locked last, as every call locks the ledger:
// an erase that holds the aggregate and deletes the charges does not meet a
// refund holding the charge and waiting for the aggregate.
func (s *UsageStore) lockRefunded(ctx context.Context, tx pgx.Tx, id uuid.UUID) (ledgerEntry, *counters, error) {
	e, ok, err := readEntry(ctx, tx, sqlChargeByID, id)
	if err != nil {
		return ledgerEntry{}, nil, err
	}
	if !ok {
		return ledgerEntry{}, nil, meter.ErrChargeNotFound
	}
	c, err := lock(ctx, tx, lockRequest{
		now:      s.clock(),
		tenantID: e.TenantID,
		existing: append([]uuid.UUID{e.CapabilityID}, e.ancestors...),
		copies:   e.copies, copiesCapID: e.CapabilityID,
	})
	if err != nil {
		return ledgerEntry{}, nil, err
	}
	e, ok, err = readEntry(ctx, tx, sqlChargeByID+` FOR UPDATE`, id)
	if err != nil {
		return ledgerEntry{}, nil, err
	}
	if !ok {
		return ledgerEntry{}, nil, meter.ErrChargeNotFound
	}
	return e, c, nil
}

// refundAmount is what req gives back of the charge e: all that is left of
// it, or the amount asked for, which may not exceed that.
func refundAmount(e ledgerEntry, req meter.RefundRequest) (limes.Nanos, error) {
	remaining := e.Amount - e.RefundedAmount
	if req.Remaining {
		return remaining, nil
	}
	if req.Amount > remaining {
		return 0, fmt.Errorf("%w: %v requested, %v of %v left", meter.ErrRefundExceedsCharge, req.Amount, remaining, e.Amount)
	}
	return req.Amount, nil
}

// refund gives amount of the charge e back to every counter in c that spent
// it, and records it on the charge. A charge of a period since closed is
// history: the current period's spend never counted it, so it has nothing
// to give back.
func (s *UsageStore) refund(ctx context.Context, tx pgx.Tx, c *counters, e ledgerEntry, amount limes.Nanos) error {
	if leased, err := s.refundLeased(ctx, tx, c, e, amount); leased || err != nil {
		return err
	}
	for _, u := range c.usage {
		u.spent = max0(u.spent - amount)
	}
	refundTenant := c.tenant.persisted && c.tenant.period == e.tenantPeriod
	if refundTenant {
		c.tenant.spent = max0(c.tenant.spent - amount)
	}
	for _, r := range c.copies {
		r.spent = max0(r.spent - amount)
	}
	return c.write(ctx, tx, s.clock(), refundTenant, heldStmt{sql: sqlRefunded, args: []any{e.ID, int64(amount)}})
}

const sqlRefunded = `UPDATE limes.charges SET refunded = refunded + $2 WHERE id = $1`

// GetCharge returns one charge of the ledger by its id.
func (s *UsageStore) GetCharge(ctx context.Context, chargeID uuid.UUID) (meter.ChargeRecord, error) {
	return s.readCharge(ctx, sqlChargeByID, chargeID)
}

// ChargeByRef returns the charge a capability took under an external
// reference; none is taken under the empty one.
func (s *UsageStore) ChargeByRef(ctx context.Context, req meter.ChargeByRefRequest) (meter.ChargeRecord, error) {
	if err := req.Validate(); err != nil {
		return meter.ChargeRecord{}, err
	}
	return s.readCharge(ctx, sqlChargeByRef, req.CapabilityID, req.ExternalRef)
}

func (s *UsageStore) readCharge(ctx context.Context, query string, args ...any) (meter.ChargeRecord, error) {
	var out meter.ChargeRecord
	err := s.inReadTx(ctx, func(tx pgx.Tx) error {
		e, ok, err := readEntry(ctx, tx, query, args...)
		if err != nil {
			return err
		}
		if !ok {
			return meter.ErrChargeNotFound
		}
		out = e.ChargeRecord
		return nil
	})
	return out, err
}

// GetReservation returns a hold that still counts, expired or not.
func (s *UsageStore) GetReservation(ctx context.Context, reservationID uuid.UUID) (meter.Reservation, error) {
	var out meter.Reservation
	err := s.inReadTx(ctx, func(tx pgx.Tx) error {
		h, err := scanHold(tx.QueryRow(ctx, sqlGetHold, reservationID))
		if errors.Is(err, pgx.ErrNoRows) {
			return meter.ErrReservationNotFound
		}
		out = h.reservation()
		return err
	})
	return out, err
}

// sqlListHolds pages a capability's holds soonest to expire first, ties by
// id, which PostgreSQL compares bytewise, as limes orders ids.
const sqlListHolds = `
SELECT ` + sqlHoldColumns + ` FROM limes.reservations
WHERE capability_id = $1
  AND ($2::timestamptz IS NULL OR (expires_at, id) > ($2, $3))
ORDER BY expires_at, id
LIMIT $4`

// sqlListTenantHolds pages a tenant's holds in sqlListHolds' order.
const sqlListTenantHolds = `
SELECT ` + sqlHoldColumns + ` FROM limes.reservations
WHERE tenant_id = $1
  AND ($2::timestamptz IS NULL OR (expires_at, id) > ($2, $3))
ORDER BY expires_at, id
LIMIT $4`

// ListReservations returns the capability's own holds a page at a time.
func (s *UsageStore) ListReservations(ctx context.Context, req meter.ListReservationsRequest) (limes.Page[meter.Reservation], error) {
	if err := req.Validate(); err != nil {
		return limes.Page[meter.Reservation]{}, err
	}
	return s.listHolds(ctx, s.inReadTx, sqlListHolds, req.CapabilityID, req.Cursor, req.Limit)
}

// ListTenantReservations implements meter.TenantReservationLister: the
// tenant's holds a page at a time, read under a scope that admits it.
func (s *UsageStore) ListTenantReservations(ctx context.Context, req meter.ListTenantReservationsRequest) (limes.Page[meter.Reservation], error) {
	if err := req.Validate(); err != nil {
		return limes.Page[meter.Reservation]{}, err
	}
	inTx := func(ctx context.Context, fn func(pgx.Tx) error) error { return s.inTenantReadTx(ctx, req.TenantID, fn) }
	return s.listHolds(ctx, inTx, sqlListTenantHolds, req.TenantID, req.Cursor, req.Limit)
}

// listHolds is a page, past cursor, of the holds query selects by key,
// read in a transaction inTx opens.
func (s *UsageStore) listHolds(ctx context.Context, inTx func(context.Context, func(pgx.Tx) error) error, query string, key uuid.UUID, cursor string, pageLimit int) (limes.Page[meter.Reservation], error) {
	var after holdPlace
	if cursor != "" {
		p, err := parseHoldPlace(cursor)
		if err != nil {
			return limes.Page[meter.Reservation]{}, err
		}
		after = p
	}
	limit := limes.PageLimit(pageLimit)
	var holds []hold
	err := inTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, query, key, after.expiresOrNil(), after.id, limit+1)
		if err != nil {
			return err
		}
		holds, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (hold, error) { return scanHold(row) })
		return err
	})
	if err != nil {
		return limes.Page[meter.Reservation]{}, err
	}
	out := make([]meter.Reservation, 0, len(holds))
	for _, h := range holds {
		out = append(out, h.reservation())
	}
	if len(out) <= limit {
		return limes.Page[meter.Reservation]{Items: out}, nil
	}
	out = out[:limit]
	last := out[len(out)-1]
	return limes.Page[meter.Reservation]{Items: out, Next: holdPlace{expires: last.ExpiresAt, id: last.ID}.String()}, nil
}

// holdPlace is a hold's place in ListReservations' order, and the cursor:
// its expiry and id.
type holdPlace struct {
	expires time.Time
	id      uuid.UUID
}

func (p holdPlace) expiresOrNil() *time.Time {
	if p.expires.IsZero() {
		return nil
	}
	return &p.expires
}

// holdCursorSep separates a cursor's fields: the expiry's Unix seconds, its
// microseconds within the second, and the id.
const holdCursorSep = "_"

// holdCursorFields is how many fields a cursor has.
const holdCursorFields = 3

// microsPerSecond bounds a cursor's microseconds.
const microsPerSecond = int64(time.Second / time.Microsecond)

func (p holdPlace) String() string {
	return strings.Join([]string{
		strconv.FormatInt(p.expires.Unix(), 10),
		strconv.FormatInt(int64(p.expires.Nanosecond())/int64(time.Microsecond), 10),
		p.id.String(),
	}, holdCursorSep)
}

func parseHoldPlace(cursor string) (holdPlace, error) {
	invalid := fmt.Errorf("%w: pgstore: cursor %q", limes.ErrInvalidRequest, cursor)
	fields := strings.Split(cursor, holdCursorSep)
	if len(fields) != holdCursorFields {
		return holdPlace{}, invalid
	}
	sec, err1 := strconv.ParseInt(fields[0], 10, 64)
	usec, err2 := strconv.ParseInt(fields[1], 10, 64)
	id, err3 := uuid.Parse(fields[2])
	if err1 != nil || err2 != nil || err3 != nil || usec < 0 || usec >= microsPerSecond {
		return holdPlace{}, invalid
	}
	return holdPlace{expires: time.Unix(sec, usec*int64(time.Microsecond)).UTC(), id: id}, nil
}

const sqlGetUsage = `SELECT request_count, spent, reserved, unit_code FROM limes.usage WHERE capability_id = $1`

// GetUsage returns a capability's counters, its children's included.
func (s *UsageStore) GetUsage(ctx context.Context, capID uuid.UUID) (meter.Usage, error) {
	u := meter.Usage{CapabilityID: capID}
	err := s.inReadTx(ctx, func(tx pgx.Tx) error {
		var spent, reserved int64
		err := tx.QueryRow(ctx, sqlGetUsage, capID).Scan(&u.RequestCount, &spent, &reserved, &u.UnitCode)
		if errors.Is(err, pgx.ErrNoRows) {
			return meter.ErrUsageNotFound
		}
		u.SpentAmount, u.ReservedAmount = limes.Nanos(spent), limes.Nanos(reserved)
		return err
	})
	if err != nil {
		return meter.Usage{}, err
	}
	return u, nil
}

const sqlCopyUsage = `
SELECT revocation_id, capability_id, request_count, spent, reserved
FROM limes.copy_usage WHERE capability_id = $1 AND revocation_id = ANY ($2)`

// CopyUsage returns the counters of each copy named that has been used.
func (s *UsageStore) CopyUsage(ctx context.Context, req meter.CopyUsageRequest) (meter.CopyUsageResult, error) {
	if err := req.Validate(); err != nil {
		return meter.CopyUsageResult{}, err
	}
	var out []meter.CopyUsage
	err := s.inReadTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, sqlCopyUsage, req.CapabilityID, req.RevocationIDs)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (meter.CopyUsage, error) {
			var c meter.CopyUsage
			var spent, reserved int64
			err := row.Scan(&c.RevocationID, &c.CapabilityID, &c.RequestCount, &spent, &reserved)
			c.SpentAmount, c.ReservedAmount = limes.Nanos(spent), limes.Nanos(reserved)
			return c, err
		})
		return err
	})
	if err != nil {
		return meter.CopyUsageResult{}, err
	}
	if len(out) == 0 {
		out = nil
	}
	return meter.CopyUsageResult{Copies: out}, nil
}

// GetTenantBudget returns a tenant's aggregate.
func (s *UsageStore) GetTenantBudget(ctx context.Context, tenantID uuid.UUID) (meter.TenantBudget, error) {
	var out meter.TenantBudget
	err := s.inTenantReadTx(ctx, tenantID, func(tx pgx.Tx) error {
		b, err := readTenant(ctx, tx, sqlGetTenant, tenantID)
		if err != nil {
			return err
		}
		if !b.persisted {
			return meter.ErrTenantBudgetNotFound
		}
		out = b.budget()
		return nil
	})
	return out, err
}

const sqlGetTenant = `
SELECT tenant_id, ceiling_max, ceiling_unlimited, spent, reserved, unit_code,
       period_start, period_end, updated_at, resource_version, period
FROM limes.tenant_budgets WHERE tenant_id = $1`

// readTenant reads one aggregate with query, sqlGetTenant or sqlLockTenant.
func readTenant(ctx context.Context, tx pgx.Tx, query string, tenantID uuid.UUID) (budgetRow, error) {
	return scanTenant(tx.QueryRow(ctx, query, tenantID), tenantID)
}

// scanTenant reads one aggregate of tenantID from row; an unpersisted one
// when there is none.
func scanTenant(row pgx.Row, tenantID uuid.UUID) (budgetRow, error) {
	b := budgetRow{tenantID: tenantID}
	var ceilingMax, spent, reserved int64
	err := row.Scan(
		&b.tenantID, &ceilingMax, &b.ceiling.Unlimited, &spent, &reserved, &b.unit,
		&b.start, &b.end, &b.updated, &b.version, &b.period)
	if errors.Is(err, pgx.ErrNoRows) {
		return budgetRow{tenantID: tenantID}, nil
	}
	if err != nil {
		return budgetRow{}, err
	}
	b.ceiling.Max, b.spent, b.reserved, b.persisted = limes.Nanos(ceilingMax), limes.Nanos(spent), limes.Nanos(reserved), true
	return b, nil
}

// SetTenantBudget upserts a tenant's ceiling under its version guard.
func (s *UsageStore) SetTenantBudget(ctx context.Context, req meter.SetTenantBudgetRequest) (meter.TenantBudget, error) {
	if err := req.Validate(); err != nil {
		return meter.TenantBudget{}, err
	}
	// A reset closes the period: what the tenant's leases spent is the
	// closing period's, so they are settled into it first, each in a
	// transaction of its own.
	if req.ResetSpend {
		if _, err := s.settleEach(ctx, func(fn func(pgx.Tx) error) error { return s.inTenantReadTx(ctx, req.TenantID, fn) }, sqlTenantLeases, req.TenantID); err != nil {
			return meter.TenantBudget{}, err
		}
	}
	var out meter.TenantBudget
	err := s.inTenantTx(ctx, req.TenantID, func(tx pgx.Tx) error {
		var err error
		out, err = s.setTenantBudget(ctx, tx, req)
		return err
	})
	return out, err
}

// setTenantBudget is SetTenantBudget in tx.
func (s *UsageStore) setTenantBudget(ctx context.Context, tx pgx.Tx, req meter.SetTenantBudgetRequest) (meter.TenantBudget, error) {
	if err := notErased(ctx, tx, req.TenantID); err != nil {
		return meter.TenantBudget{}, err
	}
	// The aggregate is created, with no unit yet, before it is locked:
	// locking a row that is not there locks nothing, and a first charge
	// that created it meanwhile would be overwritten by the write below.
	// Created here, the call waits for that charge and reads its counts.
	if _, err := tx.Exec(ctx, sqlCreateTenant, req.TenantID, "", s.clock()); err != nil {
		return meter.TenantBudget{}, err
	}
	b, err := lockTenant(ctx, tx, req.TenantID)
	if err != nil {
		return meter.TenantBudget{}, err
	}
	// A budget with no ceiling set yet expects version 0; an existing one
	// its current version.
	if req.ExpectedVersion != b.version {
		return meter.TenantBudget{}, meter.ErrTenantBudgetVersionMismatch
	}
	// A ceiling that names no unit — the zero one — keeps the tenant's.
	unit := cmp.Or(req.Ceiling.UnitCode, b.unit)
	if err := checkBudgetUnit(b, req, unit); err != nil {
		return meter.TenantBudget{}, err
	}
	now := s.clock()
	c := &counters{tenant: setBudget(b, req, unit, now)}
	if err := c.write(ctx, tx, now, true); err != nil {
		return meter.TenantBudget{}, err
	}
	return c.tenant.budget(), nil
}

// checkBudgetUnit refuses moving b to unit while it counts anything in
// another. A reset closes the spend, never the holds still open in the old
// unit.
func checkBudgetUnit(b budgetRow, req meter.SetTenantBudgetRequest, unit string) error {
	if !b.persisted || b.unit == "" || unit == b.unit {
		return nil
	}
	if b.reserved > 0 || (b.spent > 0 && !req.ResetSpend) {
		return fmt.Errorf("%w: tenant %s has %s counted in %s",
			limes.ErrUnitCodeMismatch, req.TenantID, b.spent+b.reserved, b.unit)
	}
	return nil
}

// setBudget is b set as req asks, in unit, at now, at a new version: a
// reset closes the spend and opens a period.
func setBudget(b budgetRow, req meter.SetTenantBudgetRequest, unit string, now time.Time) budgetRow {
	b.ceiling, b.unit, b.end = req.Ceiling, unit, nil
	if !req.PeriodEnd.IsZero() {
		end := req.PeriodEnd.UTC().Truncate(timeResolution)
		b.end = &end
	}
	if req.ResetSpend {
		b.spent, b.start = 0, now
		b.period++
	}
	if b.start.IsZero() {
		b.start = now
	}
	b.version++
	return b
}

// The classes ListTenantBudgets ranks by: a ceiling of 0 allows nothing and
// leads, a bounded one ranks by its utilization, and an unlimited one has
// none and trails.
const (
	classZero = iota
	classBounded
	classUnlimited
)

// utilizationScale makes spent / max an integer that orders as the fraction
// does: two different fractions with denominators within MaxNanos differ by
// more than 1/MaxNanos², which this scale keeps apart, and equal ones meet.
const utilizationScale = "1e40"

// The rows sqlListTenants keeps, as its $1 names them: never the filter's
// number, which is limes's to renumber.
const (
	keepEvery     = "every"
	keepUnlimited = "unlimited"
	keepAtOrPast  = "at-or-past"
)

// keepOf is the rows f keeps, as sqlListTenants names them. A filter this
// package does not name — one a later limes adds — is refused rather than
// read as another: a filter that narrows must never list every tenant.
func keepOf(f meter.TenantBudgetFilter) (string, error) {
	switch f {
	case meter.TenantBudgetFilterUnspecified, meter.TenantBudgetFilterAll:
		return keepEvery, nil
	case meter.TenantBudgetFilterUnlimited:
		return keepUnlimited, nil
	case meter.TenantBudgetFilterAtOrPast:
		return keepAtOrPast, nil
	}
	return "", fmt.Errorf("%w: pgstore: list tenant budgets: filter %s is not one this store knows", limes.ErrInvalidRequest, f)
}

// sqlListTenants pages the aggregates most at risk first, ties by tenant id,
// after the place ($4 class, $5 spent, $6 max, $7 tenant) when $4 is set.
// $1 is the rows to keep, as keepOf names them, $2 the threshold in basis
// points and $3 the basis points in a whole.
const sqlListTenants = `
WITH ranked AS (
    SELECT b.*,
        CASE WHEN ceiling_unlimited THEN 2 WHEN ceiling_max = 0 THEN 0 ELSE 1 END AS class,
        CASE WHEN NOT ceiling_unlimited AND ceiling_max > 0
             THEN div(spent::numeric * $9::numeric, ceiling_max) ELSE 0 END AS utilization
    FROM limes.tenant_budgets b
), place AS (
    SELECT $4::int AS class,
        CASE WHEN $4::int = 1 THEN div($5::bigint::numeric * $9::numeric, $6::bigint) ELSE 0 END AS utilization,
        $7::uuid AS tenant_id
)
SELECT r.tenant_id, r.ceiling_max, r.ceiling_unlimited, r.spent, r.reserved, r.unit_code,
       r.period_start, r.period_end, r.updated_at, r.resource_version, r.period
FROM ranked r, place p
WHERE CASE $1::text
        WHEN '` + keepEvery + `' THEN true
        WHEN '` + keepUnlimited + `' THEN r.class = 2
        WHEN '` + keepAtOrPast + `' THEN r.class = 0 OR (r.class = 1 AND r.spent::numeric * $3::numeric >= $2::numeric * r.ceiling_max)
      END
  AND (p.class IS NULL
       OR r.class > p.class
       OR (r.class = p.class AND (r.utilization < p.utilization
           OR (r.utilization = p.utilization AND r.tenant_id > p.tenant_id))))
ORDER BY r.class, r.utilization DESC, r.tenant_id
LIMIT $8`

// ListTenantBudgets returns the aggregates most at risk first.
func (s *UsageStore) ListTenantBudgets(ctx context.Context, req meter.ListTenantBudgetsRequest) (limes.Page[meter.TenantBudget], error) {
	if err := req.Validate(); err != nil {
		return limes.Page[meter.TenantBudget]{}, err
	}
	after, err := tenantCursorArgs(req.Cursor)
	if err != nil {
		return limes.Page[meter.TenantBudget]{}, err
	}
	keep, err := keepOf(req.Filter)
	if err != nil {
		return limes.Page[meter.TenantBudget]{}, err
	}
	limit := limes.PageLimit(req.Limit)
	var rows []budgetRow
	// A list across tenants refuses a scope that admits fewer, as the
	// census does: under it the page would read as every tenant's.
	err = s.inEveryTenantReadTx(ctx, func(tx pgx.Tx) error {
		args := append([]any{keep, req.ThresholdBasisPoints, meter.BasisPointsPerWhole}, after...)
		r, err := tx.Query(ctx, sqlListTenants, append(args, limit+1, utilizationScale)...)
		if err != nil {
			return err
		}
		rows, err = pgx.CollectRows(r, scanListedBudget)
		return err
	})
	if err != nil {
		return limes.Page[meter.TenantBudget]{}, err
	}
	return tenantBudgetPage(rows, limit), nil
}

// tenantCursorArgs is the place a tenant budget cursor names, as
// sqlListTenants takes it: its class, nil for none, spent, ceiling and
// tenant.
func tenantCursorArgs(cursor string) ([]any, error) {
	if cursor == "" {
		return []any{(*int)(nil), int64(0), int64(0), uuid.Nil()}, nil
	}
	p, err := parseTenantPlace(cursor)
	if err != nil {
		return nil, err
	}
	return []any{&p.class, int64(p.spent), int64(p.max), p.tenantID}, nil
}

// scanListedBudget reads one listed tenant budget.
func scanListedBudget(row pgx.CollectableRow) (budgetRow, error) {
	var b budgetRow
	var ceilingMax, spent, reserved int64
	err := row.Scan(&b.tenantID, &ceilingMax, &b.ceiling.Unlimited, &spent, &reserved, &b.unit,
		&b.start, &b.end, &b.updated, &b.version, &b.period)
	b.ceiling.Max, b.spent, b.reserved, b.persisted = limes.Nanos(ceilingMax), limes.Nanos(spent), limes.Nanos(reserved), true
	return b, err
}

// tenantBudgetPage is the first limit of rows, and the cursor past the last
// when more follow.
func tenantBudgetPage(rows []budgetRow, limit int) limes.Page[meter.TenantBudget] {
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		next = placeOf(rows[limit-1]).encode()
	}
	out := make([]meter.TenantBudget, 0, len(rows))
	for _, b := range rows {
		out = append(out, b.budget())
	}
	return limes.Page[meter.TenantBudget]{Items: out, Next: next}
}

// tenantPlace is an aggregate's place in ListTenantBudgets' order, and the
// cursor: its class, the spend and ceiling that give its utilization
// exactly, and its tenant id.
type tenantPlace struct {
	class      int
	spent, max limes.Nanos
	tenantID   uuid.UUID
}

func placeOf(b budgetRow) tenantPlace {
	p := tenantPlace{tenantID: b.tenantID}
	switch {
	case b.ceiling.Unlimited:
		p.class = classUnlimited
	case b.ceiling.Max == 0:
		p.class = classZero
	default:
		p.class, p.spent, p.max = classBounded, b.spent, b.ceiling.Max
	}
	return p
}

// tenantCursorSep separates the cursor's fields; neither a class, an
// integer nor a uuid contains it.
const tenantCursorSep = "|"

// tenantCursorFields is how many fields a cursor has.
const tenantCursorFields = 4

// encode is the cursor as ListTenantBudgets hands it out: opaque, URL-safe.
func (p tenantPlace) encode() string {
	return base64.RawURLEncoding.EncodeToString([]byte(strings.Join([]string{
		strconv.Itoa(p.class), strconv.FormatInt(int64(p.spent), 10), strconv.FormatInt(int64(p.max), 10), p.tenantID.String(),
	}, tenantCursorSep)))
}

// parseTenantPlace reads a cursor encode wrote. Anything else is
// ErrInvalidRequest: a class this store does not rank by, a spend or ceiling
// that is negative or past MaxNanos, a bounded place with no ceiling.
func parseTenantPlace(s string) (tenantPlace, error) {
	bad := fmt.Errorf("%w: pgstore: tenant budget cursor %q", limes.ErrInvalidRequest, s)
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return tenantPlace{}, bad
	}
	fields := strings.Split(string(raw), tenantCursorSep)
	if len(fields) != tenantCursorFields {
		return tenantPlace{}, bad
	}
	p, ok := readTenantPlace(fields)
	if !ok {
		return tenantPlace{}, bad
	}
	return p, nil
}

// readTenantPlace reads a cursor's fields — class, spent, ceiling, tenant —
// refusing any out of range, or a ceiling its class contradicts.
func readTenantPlace(fields []string) (tenantPlace, bool) {
	class, err := strconv.Atoi(fields[0])
	if err != nil || class < classZero || class > classUnlimited {
		return tenantPlace{}, false
	}
	spent, err1 := strconv.ParseInt(fields[1], 10, 64)
	ceiling, err2 := strconv.ParseInt(fields[2], 10, 64)
	tenant, err3 := uuid.Parse(fields[3])
	if err := errors.Join(err1, err2, err3); err != nil ||
		limes.Nanos(spent).Validate() != nil || limes.Nanos(ceiling).Validate() != nil ||
		(class == classBounded) != (ceiling > 0) {
		return tenantPlace{}, false
	}
	return tenantPlace{class: class, spent: limes.Nanos(spent), max: limes.Nanos(ceiling), tenantID: tenant}, true
}

const (
	// sqlDueHolds reads the soonest holds past their expiry, and locks them
	// in id order, the order every call that locks several takes.
	sqlDueHolds = `
SELECT ` + sqlHoldColumns + ` FROM limes.reservations
WHERE id IN (
    SELECT id FROM limes.reservations
    WHERE expires_at <= $1
    ORDER BY expires_at, id
    LIMIT $2)
ORDER BY id
FOR UPDATE`

	// sqlHoldsNaming locks every hold of the capabilities $1 or naming one
	// of them as an ancestor, in id order, before any counter is: dropping
	// those capabilities releases the first and rewrites the second.
	sqlHoldsNaming = `
SELECT ` + sqlHoldColumns + ` FROM limes.reservations
WHERE capability_id = ANY ($1) OR ancestors && $1
ORDER BY id
FOR UPDATE`

	// sqlOrphans are the capabilities with counters or holds and no record,
	// in id order so batches repeat.
	sqlOrphans = `
SELECT id FROM (
    SELECT capability_id AS id FROM limes.usage
    UNION
    SELECT capability_id FROM limes.reservations
) o
WHERE NOT EXISTS (SELECT 1 FROM ` + lineageView + ` c WHERE c.id = o.id)
ORDER BY id
LIMIT $1`

	sqlDeleteUsage = `DELETE FROM limes.usage WHERE capability_id = ANY ($1)`

	sqlDeleteCopies = `DELETE FROM limes.copy_usage WHERE capability_id = ANY ($1)`

	// sqlForgetAncestors strikes dropped capabilities from the ancestors of
	// the holds that remain: releasing or settling one later must not write
	// a dropped row back.
	sqlForgetAncestors = `
UPDATE limes.reservations
SET ancestors = coalesce(
    (SELECT array_agg(a ORDER BY n) FROM unnest(ancestors) WITH ORDINALITY AS t (a, n) WHERE a <> ALL ($1)),
    '{}')
WHERE ancestors && $1`
)

// ReleaseExpired releases the holds past their expiry, oldest first.
func (s *UsageStore) ReleaseExpired(ctx context.Context, req meter.ReleaseExpiredRequest) (meter.ReleaseExpiredResult, error) {
	if err := req.Validate(); err != nil {
		return meter.ReleaseExpiredResult{}, err
	}
	limit := meter.HousekeepingLimit(req.Limit)
	var released int64
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		holds, err := collectHolds(ctx, tx, sqlDueHolds, s.clock(), limit)
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
		return meter.ReleaseExpiredResult{}, err
	}
	settled, err := s.settleEach(ctx, func(fn func(pgx.Tx) error) error { return s.inTx(ctx, fn) }, sqlDueLeases, s.clock(), limit)
	res := meter.ReleaseExpiredResult{Released: released, SettledLeases: int64(len(settled))}
	return res, err
}

func collectHolds(ctx context.Context, tx pgx.Tx, query string, args ...any) ([]hold, error) {
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (hold, error) { return scanHold(row) })
}

// releaseHolds releases each of holds, locked, tenant by tenant in id order,
// the order every call locks aggregates in. Every counter the releases write
// is locked first, together (lockReleased).
func (s *UsageStore) releaseHolds(ctx context.Context, tx pgx.Tx, holds []hold) error {
	slices.SortFunc(holds, func(a, b hold) int {
		if c := slices.Compare(a.tenantID[:], b.tenantID[:]); c != 0 {
			return c
		}
		return slices.Compare(a.id[:], b.id[:])
	})
	if err := lockReleased(ctx, tx, holds); err != nil {
		return err
	}
	for _, h := range holds {
		if err := s.releaseHold(ctx, tx, h); err != nil {
			return err
		}
	}
	return nil
}

// lockReleased locks every counter releasing holds writes, before any is
// written, in the one order every call takes: the counters of every
// capability the holds are on or under, together, in capability id order,
// then each capability's copies, then the tenants' aggregates in tenant id
// order.
// Locked hold by hold instead, each hold's would be in order and the
// transaction's would not: one hold's root, then another's middle, against
// a call that holds the middle and waits for the root.
func lockReleased(ctx context.Context, tx pgx.Tx, holds []hold) error {
	var tenants, counters []uuid.UUID
	copies := map[uuid.UUID][][]byte{}
	for _, h := range holds {
		tenants = append(tenants, h.tenantID)
		counters = append(counters, h.capID)
		counters = append(counters, h.ancestors...)
		copies[h.capID] = append(copies[h.capID], copyIDs(h.copies)...)
	}
	if err := drain(tx.Query(ctx, sqlLockUsage, sortedUnique(counters))); err != nil {
		return err
	}
	for _, capID := range sortedUnique(slices.Collect(maps.Keys(copies))) {
		if ids := copies[capID]; len(ids) > 0 {
			if err := drain(tx.Query(ctx, sqlLockCopies, capID, ids)); err != nil {
				return err
			}
		}
	}
	for _, t := range sortedUnique(tenants) {
		if _, err := lockTenant(ctx, tx, t); err != nil {
			return err
		}
	}
	return nil
}

// sortedUnique is ids in id order, each once.
func sortedUnique(ids []uuid.UUID) []uuid.UUID {
	ids = slices.Clone(ids)
	slices.SortFunc(ids, func(a, b uuid.UUID) int { return slices.Compare(a[:], b[:]) })
	return slices.Compact(ids)
}

// drain reads every row a locking query returns, which is when it holds
// their locks, and reports its error.
func drain(rows pgx.Rows, err error) error {
	if err != nil {
		return err
	}
	for rows.Next() {
	}
	rows.Close()
	return rows.Err()
}

// DeleteUsage drops a capability's counters and its copies', releasing its
// holds first: nothing could settle or release them once it is gone.
func (s *UsageStore) DeleteUsage(ctx context.Context, req meter.DeleteUsageRequest) error {
	if err := req.Validate(); err != nil {
		return err
	}
	// Its lease first, in a transaction of its own: the ancestors and the
	// tenant it holds budget of are charged and released.
	if _, err := s.settleEach(ctx, func(fn func(pgx.Tx) error) error { return s.inTx(ctx, fn) }, sqlLeaseOf, req.CapabilityID); err != nil {
		return err
	}
	if err := s.inTx(ctx, func(tx pgx.Tx) error {
		return s.dropUsage(ctx, tx, []uuid.UUID{req.CapabilityID})
	}); err != nil {
		return err
	}
	return s.forgetLeaseAncestors(ctx, []uuid.UUID{req.CapabilityID})
}

// PurgeOrphans drops the counters of capabilities no longer on record, as
// DeleteUsage does, a batch at a time.
func (s *UsageStore) PurgeOrphans(ctx context.Context, req meter.PurgeOrphansRequest) (meter.PurgeOrphansResult, error) {
	if err := req.Validate(); err != nil {
		return meter.PurgeOrphansResult{}, err
	}
	limit := meter.HousekeepingLimit(req.Limit)
	// The orphans' leases first, each in a transaction of its own.
	if _, err := s.settleEach(ctx, func(fn func(pgx.Tx) error) error { return s.inTx(ctx, fn) }, sqlOrphanLeases, limit); err != nil {
		return meter.PurgeOrphansResult{}, err
	}
	var purged int64
	var dropped []uuid.UUID
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, sqlForgetErased); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, sqlOrphans, limit)
		if err != nil {
			return err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
		if err != nil {
			return err
		}
		purged, dropped = int64(len(ids)), ids
		if len(ids) == 0 {
			return nil
		}
		return s.dropUsage(ctx, tx, ids)
	})
	if err != nil {
		return meter.PurgeOrphansResult{}, err
	}
	if err := s.forgetLeaseAncestors(ctx, dropped); err != nil {
		return meter.PurgeOrphansResult{Purged: purged}, err
	}
	return meter.PurgeOrphansResult{Purged: purged}, nil
}

// dropUsage releases the holds of ids — first, as releasing one writes its
// ancestors' counters — then drops their counters and their copies', and
// strikes them from the ancestors of the holds that remain.
func (s *UsageStore) dropUsage(ctx context.Context, tx pgx.Tx, ids []uuid.UUID) error {
	locked, err := collectHolds(ctx, tx, sqlHoldsNaming, ids)
	if err != nil {
		return err
	}
	holds := slices.DeleteFunc(locked, func(h hold) bool { return !slices.Contains(ids, h.capID) })
	if err := s.releaseHolds(ctx, tx, holds); err != nil {
		return err
	}
	for _, q := range []string{sqlDeleteUsage, sqlDeleteCopies, sqlForgetAncestors} {
		if _, err := tx.Exec(ctx, q, ids); err != nil {
			return err
		}
	}
	return nil
}

var (
	_ meter.UsageStore      = (*UsageStore)(nil)
	_ meter.CopyUsageReader = (*UsageStore)(nil)
)

var _ meter.TenantReservationLister = (*UsageStore)(nil)
