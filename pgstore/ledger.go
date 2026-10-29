package pgstore

import (
	"context"
	"fmt"
	"strconv"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/meter"
)

// Ledger implements meter.LedgerReader over the store's charges ledger.
//
// A charge carries the store's time, so a test of a report backdates its
// charges through Config.Now rather than by writing the charges table, whose
// columns are this package's own: build the stores on a clock the test sets,
// record a capability that outlives the period, and move the clock forward
// before each charge.
//
//	at := start
//	cfg := pgstore.Config{DB: pool, Now: func() time.Time { return at }}
//	records, _ := pgstore.NewStore(cfg)
//	usage, _ := pgstore.NewUsageStore(cfg)
//	_ = records.Insert(ctx, limes.InsertRequest{Capability: c}) // c.ExpiresAt past the period
//	subject, _ := limes.StoredMeterSubject(ctx, records, c.ID)
//	at = start.AddDate(0, 0, 1)
//	_, _ = usage.Charge(ctx, meter.ChargeRequest{Subject: subject, Amount: amount}, nil)
type Ledger struct {
	conn
}

// NewLedger returns a ledger reader over cfg.DB, whose schema migrate.Run
// created.
func NewLedger(cfg Config) (*Ledger, error) {
	c, err := newConn(cfg)
	if err != nil {
		return nil, err
	}
	return &Ledger{c}, nil
}

// The date_trunc fields of the buckets, by name: the bucket's number never
// reaches SQL.
const (
	truncHour  = "hour"
	truncDay   = "day"
	truncMonth = "month"
	// truncWeek is date_trunc's ISO week, which starts on a Monday.
	truncWeek = "week"
)

// truncOf is the date_trunc field of b, and false for ChargeBucketNone,
// which sums the period whole.
// A bucket this store does not name is refused rather than summed as
// another.
func truncOf(b meter.ChargeBucket) (string, bool, error) {
	switch b {
	case meter.ChargeBucketNone:
		return "", false, nil
	case meter.ChargeBucketHour:
		return truncHour, true, nil
	case meter.ChargeBucketDay:
		return truncDay, true, nil
	case meter.ChargeBucketMonth:
		return truncMonth, true, nil
	case meter.ChargeBucketWeek:
		return truncWeek, true, nil
	default:
		return "", false, fmt.Errorf("%w: pgstore: sum charges: bucket %s is not one this store knows", limes.ErrInvalidRequest, b)
	}
}

// sqlSumCharges sums the charges of one tenant ($1), or of every tenant
// when $1 is NULL, in [$2, $3), kept by the filter's lists ($4 tenants, $5
// operations, $6 actors, $7 capabilities; an empty list keeps all), grouped
// by unit and by the bucket ($8, a date_trunc field, in UTC; NULL for none)
// and whichever of tenant ($9), op ($10), actor ($11) and capability ($12)
// the request names, then pages them past the cursor's place ($13, a flag,
// then $14..$20) in SumCharges' order: buckets in time order, the largest
// charged first, then the group's fields ascending, bytewise — unit,
// tenant, op, actor, capability. A group not named is one value — ” or the
// nil uuid — and a period summed whole is one bucket at the epoch, so every
// column orders and compares. The sums are numeric, exact past bigint, and
// they order and compare as numbers: their text, which the scan reads, is
// named apart so that no output column shadows them in ORDER BY.
const sqlSumCharges = `
WITH sums AS (
    SELECT
        CASE WHEN $8::text IS NULL THEN to_timestamp(0) ELSE date_trunc($8::text, at, 'UTC') END AS bucket,
        unit_code,
        CASE WHEN $9::boolean THEN tenant_id ELSE '00000000-0000-0000-0000-000000000000'::uuid END AS tenant_id,
        CASE WHEN $10::boolean THEN op ELSE '' END AS op,
        CASE WHEN $11::boolean THEN actor ELSE '' END AS actor,
        CASE WHEN $12::boolean THEN capability_id ELSE '00000000-0000-0000-0000-000000000000'::uuid END AS capability_id,
        sum(amount) AS charged,
        sum(refunded) AS refunded,
        count(*) AS charges
    FROM limes.charges
    WHERE ($1::uuid IS NULL OR tenant_id = $1) AND at >= $2 AND at < $3
      AND (cardinality($4::uuid[]) = 0 OR tenant_id = ANY ($4::uuid[]))
      AND (cardinality($5::text[]) = 0 OR op = ANY ($5::text[]))
      AND (cardinality($6::text[]) = 0 OR actor = ANY ($6::text[]))
      AND (cardinality($7::uuid[]) = 0 OR capability_id = ANY ($7::uuid[]))
    GROUP BY 1, 2, 3, 4, 5, 6
)
SELECT bucket, unit_code, tenant_id, op, actor, capability_id, charged::text AS charged_text, refunded::text AS refunded_text, charges
FROM sums
WHERE NOT $13::boolean
   OR (bucket, -charged, unit_code COLLATE "C", tenant_id, op COLLATE "C", actor COLLATE "C", capability_id)
    > ($14::timestamptz, -($15::numeric), $16::text COLLATE "C", $17::uuid, $18::text COLLATE "C", $19::text COLLATE "C", $20::uuid)
ORDER BY sums.bucket, sums.charged DESC, sums.unit_code COLLATE "C", sums.tenant_id, sums.op COLLATE "C", sums.actor COLLATE "C", sums.capability_id
LIMIT $21`

// sumCursor is pgstore's cursor of SumCharges: the last row's place, as
// the query orders by it.
type sumCursor struct {
	Bucket  time.Time `json:"b"`
	Charged int64     `json:"c"`
	Unit    string    `json:"u"`
	Tenant  uuid.UUID `json:"t"`
	Op      string    `json:"o"`
	Actor   string    `json:"a"`
	CapID   uuid.UUID `json:"i"`
}

// SumCharges implements meter.LedgerReader, in one transaction scoped to
// the tenant (Config.CheckScope) or, with AllTenants, to every tenant to
// read (WithAllTenantsReadOnly), as ListTenantBudgets is.
func (l *Ledger) SumCharges(ctx context.Context, req meter.SumChargesRequest) (limes.Page[meter.ChargeSum], error) {
	if err := req.Validate(); err != nil {
		return limes.Page[meter.ChargeSum]{}, err
	}
	q, err := sumQueryOf(req)
	if err != nil {
		return limes.Page[meter.ChargeSum]{}, err
	}
	var out []meter.ChargeSum
	sum := func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, sqlSumCharges, q.args...)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (meter.ChargeSum, error) {
			return scanSum(row, q.bucketed)
		})
		return err
	}
	if req.AllTenants {
		err = l.inEveryTenantReadTx(ctx, sum)
	} else {
		err = l.inTenantReadTx(ctx, req.TenantID, sum)
	}
	if err != nil {
		return limes.Page[meter.ChargeSum]{}, err
	}
	if len(out) <= q.limit {
		return limes.Page[meter.ChargeSum]{Items: out}, nil
	}
	out = out[:q.limit]
	return limes.Page[meter.ChargeSum]{Items: out, Next: placeOfSum(out[len(out)-1], q.bucketed).encode()}, nil
}

// sumQuery is a SumChargesRequest as sqlSumCharges reads it: its arguments,
// whether it buckets, and the page's limit.
type sumQuery struct {
	args     []any
	bucketed bool
	limit    int
}

// sumQueryOf is req's sumQuery, refusing a bucket or a cursor it cannot
// read. A page asks for one row past its limit, to learn whether more
// follow.
func sumQueryOf(req meter.SumChargesRequest) (sumQuery, error) {
	field, bucketed, err := truncOf(req.Bucket)
	if err != nil {
		return sumQuery{}, err
	}
	var trunc *string
	if bucketed {
		trunc = &field
	}
	var after sumCursor
	if req.Cursor != "" {
		if after, err = decodeSumCursor(req.Cursor); err != nil {
			return sumQuery{}, err
		}
	}
	var tenant *uuid.UUID
	if !req.AllTenants {
		tenant = &req.TenantID
	}
	ops := make([]string, len(req.Filter.Ops))
	for i, op := range req.Filter.Ops {
		ops[i] = string(op)
	}
	limit := limes.PageLimit(req.Limit)
	return sumQuery{bucketed: bucketed, limit: limit, args: []any{
		tenant, req.From, req.To, nonNil(req.Filter.Tenants), ops, nonNil(req.Filter.Actors), nonNil(req.Filter.Capabilities),
		trunc, req.Groups(meter.ChargeDimensionTenant), req.Groups(meter.ChargeDimensionOp),
		req.Groups(meter.ChargeDimensionActor), req.Groups(meter.ChargeDimensionCapability),
		req.Cursor != "", after.Bucket, strconv.FormatInt(after.Charged, 10), after.Unit, after.Tenant, after.Op, after.Actor, after.CapID,
		limit + 1,
	}}, nil
}

// scanSum reads one group's sum; its bucket's start only when bucketed.
func scanSum(row pgx.CollectableRow, bucketed bool) (meter.ChargeSum, error) {
	var s meter.ChargeSum
	var bucket time.Time
	var op, charged, refunded string
	if err := row.Scan(&bucket, &s.UnitCode, &s.TenantID, &op, &s.Actor, &s.CapabilityID, &charged, &refunded, &s.Charges); err != nil {
		return meter.ChargeSum{}, err
	}
	s.Op = limes.Op(op)
	var err error
	if s.Charged, err = sumOf(charged); err != nil {
		return meter.ChargeSum{}, err
	}
	if s.Refunded, err = sumOf(refunded); err != nil {
		return meter.ChargeSum{}, err
	}
	if bucketed {
		s.BucketStart = bucket.UTC()
	}
	return s, nil
}

// placeOfSum is s's place in the query's order. Its bucket is the one the
// query compares by: the epoch for a period summed whole.
func placeOfSum(s meter.ChargeSum, bucketed bool) sumCursor {
	bucket := time.Unix(0, 0).UTC()
	if bucketed {
		bucket = s.BucketStart
	}
	return sumCursor{Bucket: bucket, Charged: int64(s.Charged), Unit: s.UnitCode, Tenant: s.TenantID, Op: string(s.Op), Actor: s.Actor, CapID: s.CapabilityID}
}

// nonNil is list, or an empty one for nil: a nil slice reaches SQL as NULL,
// whose cardinality is NULL rather than 0, which would keep no charge.
func nonNil[T any](list []T) []T {
	if list == nil {
		return []T{}
	}
	return list
}

// sumOf reads a numeric sum, or ErrCounterOverflow past MaxNanos.
func sumOf(text string) (limes.Nanos, error) {
	n, err := strconv.ParseInt(text, 10, 64)
	if err != nil || limes.Nanos(n) > limes.MaxNanos {
		return 0, fmt.Errorf("%w: a charge sum of %s, past %s", meter.ErrCounterOverflow, text, limes.MaxNanos)
	}
	return limes.Nanos(n), nil
}

// encode is the cursor as SumCharges hands it out: opaque, URL-safe.
func (c sumCursor) encode() string { return encodeCursor(c) }

// decodeSumCursor reads a cursor encode wrote; anything else is
// ErrInvalidRequest.
func decodeSumCursor(s string) (sumCursor, error) {
	var c sumCursor
	if err := decodeCursor(s, &c); err != nil || limes.Nanos(c.Charged).Validate() != nil {
		return sumCursor{}, fmt.Errorf("%w: pgstore: charge sum cursor %q", limes.ErrInvalidRequest, s)
	}
	return c, nil
}

var _ meter.LedgerReader = (*Ledger)(nil)

// sqlPurgeCharges removes the oldest charges taken before $1, at most $2 of
// them, of the tenants the call's scope reaches, by charges_at
// (migrate/ddl/migrations/011_ledger_indexes.sql), each then by its key: the batch as an
// array, which the planner does not hash-join against the whole ledger. A
// charge a refund holds is skipped, for a later batch, rather than waited
// on.
const sqlPurgeCharges = `
WITH due AS (
    SELECT id FROM limes.charges
    WHERE at < $1
    ORDER BY at, id
    LIMIT $2
    FOR UPDATE SKIP LOCKED
)
DELETE FROM limes.charges WHERE id = ANY (ARRAY(SELECT id FROM due))`

// PurgeCharges implements meter.LedgerHousekeeping: the charges taken more
// than req.Retention() ago by the store's clock, oldest first, at most the
// batch's limit, in one transaction of the call's scope — every tenant's
// under WithAllTenants, one tenant's under WithTenant. A scope that admits no
// tenant is refused with limes.ErrScopeMismatch, rather than purging nothing
// on every call while the ledger grows.
func (l *Ledger) PurgeCharges(ctx context.Context, req meter.PurgeChargesRequest) (meter.PurgeChargesResult, error) {
	if err := req.Validate(); err != nil {
		return meter.PurgeChargesResult{}, err
	}
	var purged int64
	err := l.inTx(ctx, func(tx pgx.Tx) error {
		if err := checkSchemaAdmitsAny(ctx, tx); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, sqlPurgeCharges, l.clock().Add(-req.Retention()), meter.HousekeepingLimit(req.Limit))
		purged = tag.RowsAffected()
		return err
	})
	if err != nil {
		return meter.PurgeChargesResult{}, err
	}
	return meter.PurgeChargesResult{Purged: purged}, nil
}

var _ meter.LedgerHousekeeping = (*Ledger)(nil)
