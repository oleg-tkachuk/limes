package pgstore

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/oleg-tkachuk/limes"
)

// Refund returns spend from one ledger entry to the counters it debited.
func (s *UsageStore) Refund(ctx context.Context, req limes.RefundRequest) (limes.RefundReceipt, error) {
	if err := req.Validate(); err != nil {
		return limes.RefundReceipt{}, err
	}
	var receipt limes.RefundReceipt
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		// The charge is read once to learn which counters it debited, which
		// are locked first, and locked last, as every call locks the ledger:
		// an erase that holds the aggregate and deletes the charges does not
		// meet a refund holding the charge and waiting for the aggregate.
		e, ok, err := readEntry(ctx, tx, sqlChargeByID, req.ChargeID)
		if err != nil {
			return err
		}
		if !ok {
			return limes.ErrChargeNotFound
		}
		c, err := lock(ctx, tx, lockRequest{
			now:      s.clock(),
			tenantID: e.TenantID,
			existing: append([]uuid.UUID{e.CapabilityID}, e.ancestors...),
			copies:   e.copies,
		})
		if err != nil {
			return err
		}
		e, ok, err = readEntry(ctx, tx, sqlChargeByID+` FOR UPDATE`, req.ChargeID)
		if err != nil {
			return err
		}
		if !ok {
			return limes.ErrChargeNotFound
		}
		receipt = limes.RefundReceipt{ChargeID: e.ID, TenantID: e.TenantID, UnitCode: e.UnitCode}
		remaining := e.Amount - e.RefundedAmount
		amount := req.Amount
		if req.Remaining {
			amount = remaining
		}
		if amount > remaining {
			return fmt.Errorf("%w: %v requested, %v of %v left", limes.ErrRefundExceedsCharge, amount, remaining, e.Amount)
		}
		if amount == 0 {
			return nil
		}
		for _, u := range c.usage {
			u.spent = max0(u.spent - amount)
		}
		// A charge of a period since closed is history: the current period's
		// spend never counted it, so it has nothing to give back.
		refundTenant := c.tenant.persisted && c.tenant.period == e.tenantPeriod
		if refundTenant {
			c.tenant.spent = max0(c.tenant.spent - amount)
		}
		for _, r := range c.copies {
			r.spent = max0(r.spent - amount)
		}
		if err := c.write(ctx, tx, s.clock(), refundTenant); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, sqlRefunded, e.ID, int64(amount)); err != nil {
			return err
		}
		receipt.Amount = amount
		return nil
	})
	if err != nil {
		return limes.RefundReceipt{}, err
	}
	return receipt, nil
}

const sqlRefunded = `UPDATE limes.charges SET refunded = refunded + $2 WHERE id = $1`

// GetCharge returns one charge of the ledger by its id.
func (s *UsageStore) GetCharge(ctx context.Context, chargeID uuid.UUID) (limes.ChargeRecord, error) {
	return s.readCharge(ctx, sqlChargeByID, chargeID)
}

// ChargeByRef returns the charge a capability took under an external
// reference; none is taken under the empty one.
func (s *UsageStore) ChargeByRef(ctx context.Context, req limes.ChargeByRefRequest) (limes.ChargeRecord, error) {
	if req.ExternalRef == "" {
		return limes.ChargeRecord{}, limes.ErrChargeNotFound
	}
	return s.readCharge(ctx, sqlChargeByRef, req.CapabilityID, req.ExternalRef)
}

func (s *UsageStore) readCharge(ctx context.Context, query string, args ...any) (limes.ChargeRecord, error) {
	var out limes.ChargeRecord
	err := s.inReadTx(ctx, func(tx pgx.Tx) error {
		e, ok, err := readEntry(ctx, tx, query, args...)
		if err != nil {
			return err
		}
		if !ok {
			return limes.ErrChargeNotFound
		}
		out = e.ChargeRecord
		return nil
	})
	return out, err
}

// GetReservation returns a hold that still counts, expired or not.
func (s *UsageStore) GetReservation(ctx context.Context, reservationID uuid.UUID) (limes.Reservation, error) {
	var out limes.Reservation
	err := s.inReadTx(ctx, func(tx pgx.Tx) error {
		h, err := scanHold(tx.QueryRow(ctx, sqlGetHold, reservationID))
		if errors.Is(err, pgx.ErrNoRows) {
			return limes.ErrReservationNotFound
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

// ListReservations returns the capability's own holds a page at a time.
func (s *UsageStore) ListReservations(ctx context.Context, req limes.ListReservationsRequest) (limes.Page[limes.Reservation], error) {
	if err := req.Validate(); err != nil {
		return limes.Page[limes.Reservation]{}, err
	}
	var after holdPlace
	if req.Cursor != "" {
		p, err := parseHoldPlace(req.Cursor)
		if err != nil {
			return limes.Page[limes.Reservation]{}, err
		}
		after = p
	}
	limit := limes.PageLimit(req.Limit)
	var holds []hold
	err := s.inReadTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, sqlListHolds, req.CapabilityID, after.expiresOrNil(), after.id, limit+1)
		if err != nil {
			return err
		}
		holds, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (hold, error) { return scanHold(row) })
		return err
	})
	if err != nil {
		return limes.Page[limes.Reservation]{}, err
	}
	out := make([]limes.Reservation, 0, len(holds))
	for _, h := range holds {
		out = append(out, h.reservation())
	}
	if len(out) <= limit {
		return limes.Page[limes.Reservation]{Items: out}, nil
	}
	out = out[:limit]
	last := out[len(out)-1]
	return limes.Page[limes.Reservation]{Items: out, Next: holdPlace{expires: last.ExpiresAt, id: last.ID}.String()}, nil
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
func (s *UsageStore) GetUsage(ctx context.Context, capID uuid.UUID) (limes.Usage, error) {
	u := limes.Usage{CapabilityID: capID}
	err := s.inReadTx(ctx, func(tx pgx.Tx) error {
		var spent, reserved int64
		err := tx.QueryRow(ctx, sqlGetUsage, capID).Scan(&u.RequestCount, &spent, &reserved, &u.UnitCode)
		if errors.Is(err, pgx.ErrNoRows) {
			return limes.ErrUsageNotFound
		}
		u.SpentAmount, u.ReservedAmount = limes.Nanos(spent), limes.Nanos(reserved)
		return err
	})
	if err != nil {
		return limes.Usage{}, err
	}
	return u, nil
}

const sqlCopyUsage = `
SELECT revocation_id, capability_id, request_count, spent, reserved
FROM limes.copy_usage WHERE revocation_id = ANY ($1)`

// CopyUsage returns the counters of each copy named that has been used.
func (s *UsageStore) CopyUsage(ctx context.Context, req limes.CopyUsageRequest) (limes.CopyUsageResult, error) {
	if err := req.Validate(); err != nil {
		return limes.CopyUsageResult{}, err
	}
	var out []limes.CopyUsage
	err := s.inReadTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, sqlCopyUsage, req.RevocationIDs)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (limes.CopyUsage, error) {
			var c limes.CopyUsage
			var spent, reserved int64
			err := row.Scan(&c.RevocationID, &c.CapabilityID, &c.RequestCount, &spent, &reserved)
			c.SpentAmount, c.ReservedAmount = limes.Nanos(spent), limes.Nanos(reserved)
			return c, err
		})
		return err
	})
	if err != nil {
		return limes.CopyUsageResult{}, err
	}
	if len(out) == 0 {
		out = nil
	}
	return limes.CopyUsageResult{Copies: out}, nil
}

// GetTenantBudget returns a tenant's aggregate.
func (s *UsageStore) GetTenantBudget(ctx context.Context, tenantID uuid.UUID) (limes.TenantBudget, error) {
	var out limes.TenantBudget
	err := s.inTenantReadTx(ctx, tenantID, func(tx pgx.Tx) error {
		b, err := readTenant(ctx, tx, sqlGetTenant, tenantID)
		if err != nil {
			return err
		}
		if !b.persisted {
			return limes.ErrTenantBudgetNotFound
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
	b := budgetRow{tenantID: tenantID}
	var ceilingMax, spent, reserved int64
	err := tx.QueryRow(ctx, query, tenantID).Scan(
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
func (s *UsageStore) SetTenantBudget(ctx context.Context, req limes.SetTenantBudgetRequest) (limes.TenantBudget, error) {
	if err := req.Validate(); err != nil {
		return limes.TenantBudget{}, err
	}
	var out limes.TenantBudget
	err := s.inTenantTx(ctx, req.TenantID, func(tx pgx.Tx) error {
		// The aggregate is created, with no unit yet, before it is locked:
		// locking a row that is not there locks nothing, and a first charge
		// that created it meanwhile would be overwritten by the write below.
		// Created here, the call waits for that charge and reads its counts.
		if _, err := tx.Exec(ctx, sqlCreateTenant, req.TenantID, "", s.clock()); err != nil {
			return err
		}
		b, err := lockTenant(ctx, tx, req.TenantID)
		if err != nil {
			return err
		}
		// A budget with no ceiling set yet expects version 0; an existing one
		// its current version.
		if req.ExpectedVersion != b.version {
			return limes.ErrTenantBudgetVersionMismatch
		}
		// A ceiling that names no unit — the zero one — keeps the tenant's.
		unit := req.Ceiling.UnitCode
		if unit == "" {
			unit = b.unit
		}
		// A reset closes the spend, never the holds still open in the old
		// unit.
		if b.persisted && b.unit != "" && unit != b.unit &&
			(b.reserved > 0 || (b.spent > 0 && !req.ResetSpend)) {
			return fmt.Errorf("%w: tenant %s has %s counted in %s",
				limes.ErrUnitCodeMismatch, req.TenantID, b.spent+b.reserved, b.unit)
		}
		now := s.clock()
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
		c := &counters{tenant: b}
		if err := c.write(ctx, tx, now, true); err != nil {
			return err
		}
		out = c.tenant.budget()
		return nil
	})
	return out, err
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
func keepOf(f limes.TenantBudgetFilter) (string, error) {
	switch f {
	case limes.TenantBudgetFilterUnspecified, limes.TenantBudgetFilterAll:
		return keepEvery, nil
	case limes.TenantBudgetFilterUnlimited:
		return keepUnlimited, nil
	case limes.TenantBudgetFilterAtOrPast:
		return keepAtOrPast, nil
	}
	return "", fmt.Errorf("%w: pgstore: list tenant budgets: filter %s is not one this store knows", limes.ErrInvalidRequest, f)
}

// sqlListTenants pages the aggregates most at risk first, ties by tenant id,
// after the place ($4 class, $5 spent, $6 max, $7 tenant) when $4 is set.
// $1 is the rows to keep, as keepOf names them, and $2 / $3 the threshold as
// an exact fraction.
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
        WHEN '` + keepAtOrPast + `' THEN r.class = 0 OR (r.class = 1 AND r.spent::numeric * 100 * $3::numeric >= $2::numeric * r.ceiling_max)
      END
  AND (p.class IS NULL
       OR r.class > p.class
       OR (r.class = p.class AND (r.utilization < p.utilization
           OR (r.utilization = p.utilization AND r.tenant_id > p.tenant_id))))
ORDER BY r.class, r.utilization DESC, r.tenant_id
LIMIT $8`

// ListTenantBudgets returns the aggregates most at risk first.
func (s *UsageStore) ListTenantBudgets(ctx context.Context, req limes.ListTenantBudgetsRequest) (limes.Page[limes.TenantBudget], error) {
	if err := req.Validate(); err != nil {
		return limes.Page[limes.TenantBudget]{}, err
	}
	var after *tenantPlace
	if req.Cursor != "" {
		p, err := parseTenantPlace(req.Cursor)
		if err != nil {
			return limes.Page[limes.TenantBudget]{}, err
		}
		after = &p
	}
	keep, err := keepOf(req.Filter)
	if err != nil {
		return limes.Page[limes.TenantBudget]{}, err
	}
	threshold := limes.ExactPercent(req.ThresholdPct)
	limit := limes.PageLimit(req.Limit)
	var class *int
	var spent, ceiling int64
	var tenant uuid.UUID
	if after != nil {
		class, spent, ceiling, tenant = &after.class, int64(after.spent), int64(after.max), after.tenantID
	}
	var rows []budgetRow
	err = s.inReadTx(ctx, func(tx pgx.Tx) error {
		r, err := tx.Query(ctx, sqlListTenants,
			keep, threshold.Num().String(), threshold.Denom().String(),
			class, spent, ceiling, tenant, limit+1, utilizationScale)
		if err != nil {
			return err
		}
		rows, err = pgx.CollectRows(r, func(row pgx.CollectableRow) (budgetRow, error) {
			var b budgetRow
			var ceilingMax, spent, reserved int64
			err := row.Scan(&b.tenantID, &ceilingMax, &b.ceiling.Unlimited, &spent, &reserved, &b.unit,
				&b.start, &b.end, &b.updated, &b.version, &b.period)
			b.ceiling.Max, b.spent, b.reserved, b.persisted = limes.Nanos(ceilingMax), limes.Nanos(spent), limes.Nanos(reserved), true
			return b, err
		})
		return err
	})
	if err != nil {
		return limes.Page[limes.TenantBudget]{}, err
	}
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		next = placeOf(rows[limit-1]).encode()
	}
	out := make([]limes.TenantBudget, 0, len(rows))
	for _, b := range rows {
		out = append(out, b.budget())
	}
	return limes.Page[limes.TenantBudget]{Items: out, Next: next}, nil
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
	class, err := strconv.Atoi(fields[0])
	if err != nil || class < classZero || class > classUnlimited {
		return tenantPlace{}, bad
	}
	spent, err1 := strconv.ParseInt(fields[1], 10, 64)
	ceiling, err2 := strconv.ParseInt(fields[2], 10, 64)
	tenant, err3 := uuid.Parse(fields[3])
	if err1 != nil || err2 != nil || err3 != nil ||
		limes.Nanos(spent).Validate() != nil || limes.Nanos(ceiling).Validate() != nil ||
		(class == classBounded) != (ceiling > 0) {
		return tenantPlace{}, bad
	}
	return tenantPlace{class: class, spent: limes.Nanos(spent), max: limes.Nanos(ceiling), tenantID: tenant}, nil
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
WHERE NOT EXISTS (SELECT 1 FROM limes.capabilities c WHERE c.id = o.id)
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

// housekeepingLimit resolves a batch limit, refusing a negative one.
func housekeepingLimit(limit int) (int, error) {
	if limit < 0 {
		return 0, fmt.Errorf("%w: pgstore: housekeeping limit %d is negative", limes.ErrInvalidRequest, limit)
	}
	if limit == 0 {
		return limes.DefaultHousekeepingLimit, nil
	}
	return limit, nil
}

// ReleaseExpired releases the holds past their expiry, oldest first.
func (s *UsageStore) ReleaseExpired(ctx context.Context, req limes.ReleaseExpiredRequest) (limes.ReleaseExpiredResult, error) {
	limit, err := housekeepingLimit(req.Limit)
	if err != nil {
		return limes.ReleaseExpiredResult{}, err
	}
	var released int64
	err = s.inTx(ctx, func(tx pgx.Tx) error {
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
		return limes.ReleaseExpiredResult{}, err
	}
	return limes.ReleaseExpiredResult{Released: released}, nil
}

func collectHolds(ctx context.Context, tx pgx.Tx, query string, args ...any) ([]hold, error) {
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (hold, error) { return scanHold(row) })
}

// releaseHolds releases each of holds, locked, tenant by tenant in id order,
// the order every call locks aggregates in.
func (s *UsageStore) releaseHolds(ctx context.Context, tx pgx.Tx, holds []hold) error {
	slices.SortFunc(holds, func(a, b hold) int {
		if c := slices.Compare(a.tenantID[:], b.tenantID[:]); c != 0 {
			return c
		}
		return slices.Compare(a.id[:], b.id[:])
	})
	for _, h := range holds {
		if err := s.releaseHold(ctx, tx, h); err != nil {
			return err
		}
	}
	return nil
}

// DeleteUsage drops a capability's counters and its copies', releasing its
// holds first: nothing could settle or release them once it is gone.
func (s *UsageStore) DeleteUsage(ctx context.Context, req limes.DeleteUsageRequest) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		return s.dropUsage(ctx, tx, []uuid.UUID{req.CapabilityID})
	})
}

// PurgeOrphans drops the counters of capabilities no longer on record, as
// DeleteUsage does, a batch at a time.
func (s *UsageStore) PurgeOrphans(ctx context.Context, req limes.PurgeOrphansRequest) (limes.PurgeOrphansResult, error) {
	limit, err := housekeepingLimit(req.Limit)
	if err != nil {
		return limes.PurgeOrphansResult{}, err
	}
	var purged int64
	err = s.inTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, sqlOrphans, limit)
		if err != nil {
			return err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
		if err != nil {
			return err
		}
		purged = int64(len(ids))
		if len(ids) == 0 {
			return nil
		}
		return s.dropUsage(ctx, tx, ids)
	})
	if err != nil {
		return limes.PurgeOrphansResult{}, err
	}
	return limes.PurgeOrphansResult{Purged: purged}, nil
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
	_ limes.UsageStore[pgx.Tx] = (*UsageStore)(nil)
	_ limes.CopyUsageReader    = (*UsageStore)(nil)
)
