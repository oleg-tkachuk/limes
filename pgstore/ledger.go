package pgstore

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/oleg-tkachuk/limes"
)

// Ledger implements limes.LedgerReader over the store's charges ledger.
type Ledger struct {
	conn
}

// NewLedger returns a ledger reader over cfg.DB, whose schema Migrate
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
)

// truncOf is the date_trunc field of b, and false for ChargeBucketNone,
// which sums the period whole. A bucket this store does not name is
// refused rather than summed as another.
func truncOf(b limes.ChargeBucket) (string, bool, error) {
	switch b {
	case limes.ChargeBucketNone:
		return "", false, nil
	case limes.ChargeBucketHour:
		return truncHour, true, nil
	case limes.ChargeBucketDay:
		return truncDay, true, nil
	case limes.ChargeBucketMonth:
		return truncMonth, true, nil
	default:
		return "", false, fmt.Errorf("%w: pgstore: sum charges: bucket %s is not one this store knows", limes.ErrInvalidRequest, b)
	}
}

// sqlSumCharges sums one tenant's charges in [$2, $3), grouped by unit and by
// the bucket ($4, a date_trunc field, in UTC; NULL for none) and whichever of
// op ($5), actor ($6) and capability ($7) the request names, then pages
// them past the cursor's place ($8, a flag, then $9..$14) in SumCharges'
// order: buckets in time order, the largest charged first, then the group's
// fields ascending, bytewise. A group not named is one value — ” or the
// nil uuid — and a period summed whole is one bucket at the epoch, so every
// column orders and compares. The sums are numeric, exact past bigint, and
// they order and compare as numbers: their text, which the scan reads, is
// named apart so that no output column shadows them in ORDER BY.
const sqlSumCharges = `
WITH sums AS (
    SELECT
        CASE WHEN $4::text IS NULL THEN to_timestamp(0) ELSE date_trunc($4::text, at, 'UTC') END AS bucket,
        unit_code,
        CASE WHEN $5::boolean THEN op ELSE '' END AS op,
        CASE WHEN $6::boolean THEN actor ELSE '' END AS actor,
        CASE WHEN $7::boolean THEN capability_id ELSE '00000000-0000-0000-0000-000000000000'::uuid END AS capability_id,
        sum(amount) AS charged,
        sum(refunded) AS refunded,
        count(*) AS charges
    FROM limes.charges
    WHERE tenant_id = $1 AND at >= $2 AND at < $3
    GROUP BY 1, 2, 3, 4, 5
)
SELECT bucket, unit_code, op, actor, capability_id, charged::text AS charged_text, refunded::text AS refunded_text, charges
FROM sums
WHERE NOT $8::boolean
   OR (bucket, -charged, unit_code COLLATE "C", op COLLATE "C", actor COLLATE "C", capability_id)
    > ($9::timestamptz, -($10::numeric), $11::text COLLATE "C", $12::text COLLATE "C", $13::text COLLATE "C", $14::uuid)
ORDER BY sums.bucket, sums.charged DESC, sums.unit_code COLLATE "C", sums.op COLLATE "C", sums.actor COLLATE "C", sums.capability_id
LIMIT $15`

// sumCursor is pgstore's cursor of SumCharges: the last row's place, as
// the query orders by it.
type sumCursor struct {
	Bucket  time.Time `json:"b"`
	Charged int64     `json:"c"`
	Unit    string    `json:"u"`
	Op      string    `json:"o"`
	Actor   string    `json:"a"`
	CapID   uuid.UUID `json:"i"`
}

// SumCharges implements limes.LedgerReader, in one transaction scoped to
// the tenant (Config.CheckScope).
func (l *Ledger) SumCharges(ctx context.Context, req limes.SumChargesRequest) (limes.Page[limes.ChargeSum], error) {
	if err := req.Validate(); err != nil {
		return limes.Page[limes.ChargeSum]{}, err
	}
	field, bucketed, err := truncOf(req.Bucket)
	if err != nil {
		return limes.Page[limes.ChargeSum]{}, err
	}
	var trunc *string
	if bucketed {
		trunc = &field
	}
	var after sumCursor
	if req.Cursor != "" {
		if after, err = decodeSumCursor(req.Cursor); err != nil {
			return limes.Page[limes.ChargeSum]{}, err
		}
	}
	limit := limes.PageLimit(req.Limit)
	var out []limes.ChargeSum
	err = l.inTenantReadTx(ctx, req.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, sqlSumCharges,
			req.TenantID, req.From, req.To, trunc, req.ByOp, req.ByActor, req.ByCapability,
			req.Cursor != "", after.Bucket, strconv.FormatInt(after.Charged, 10), after.Unit, after.Op, after.Actor, after.CapID,
			limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var s limes.ChargeSum
			var bucket time.Time
			var op, charged, refunded string
			if err := rows.Scan(&bucket, &s.UnitCode, &op, &s.Actor, &s.CapabilityID, &charged, &refunded, &s.Charges); err != nil {
				return err
			}
			s.Op = limes.Op(op)
			if s.Charged, err = sumOf(charged); err != nil {
				return err
			}
			if s.Refunded, err = sumOf(refunded); err != nil {
				return err
			}
			if bucketed {
				s.BucketStart = bucket.UTC()
			}
			out = append(out, s)
		}
		return rows.Err()
	})
	if err != nil {
		return limes.Page[limes.ChargeSum]{}, err
	}
	if len(out) <= limit {
		return limes.Page[limes.ChargeSum]{Items: out}, nil
	}
	out = out[:limit]
	return limes.Page[limes.ChargeSum]{Items: out, Next: placeOfSum(out[len(out)-1], bucketed).encode()}, nil
}

// placeOfSum is s's place in the query's order. Its bucket is the one the
// query compares by: the epoch for a period summed whole.
func placeOfSum(s limes.ChargeSum, bucketed bool) sumCursor {
	bucket := time.Unix(0, 0).UTC()
	if bucketed {
		bucket = s.BucketStart
	}
	return sumCursor{Bucket: bucket, Charged: int64(s.Charged), Unit: s.UnitCode, Op: string(s.Op), Actor: s.Actor, CapID: s.CapabilityID}
}

// sumOf reads a numeric sum, or ErrCounterOverflow past MaxNanos.
func sumOf(text string) (limes.Nanos, error) {
	n, err := strconv.ParseInt(text, 10, 64)
	if err != nil || limes.Nanos(n) > limes.MaxNanos {
		return 0, fmt.Errorf("%w: a charge sum of %s, past %s", limes.ErrCounterOverflow, text, limes.MaxNanos)
	}
	return limes.Nanos(n), nil
}

// encode is the cursor as SumCharges hands it out: opaque, URL-safe.
func (c sumCursor) encode() string {
	raw, err := json.Marshal(c)
	if err != nil {
		panic(err) // a struct of a time, an integer, strings and a uuid always encodes
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// decodeSumCursor reads a cursor encode wrote; anything else is
// ErrInvalidRequest.
func decodeSumCursor(s string) (sumCursor, error) {
	bad := fmt.Errorf("%w: pgstore: charge sum cursor %q", limes.ErrInvalidRequest, s)
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return sumCursor{}, bad
	}
	var c sumCursor
	if err := json.Unmarshal(raw, &c); err != nil || limes.Nanos(c.Charged).Validate() != nil {
		return sumCursor{}, bad
	}
	return c, nil
}

var _ limes.LedgerReader = (*Ledger)(nil)
