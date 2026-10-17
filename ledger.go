package limes

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// LedgerReader sums a tenant's charges ledger over a period, for billing and
// reporting: totals, the largest spenders by operation, actor or capability,
// and series by hour, day, week or month. It reads the ledger the Meter writes, so
// a consumer reports from it rather than from a store's tables. It is a type
// of its own, beside UsageStore rather than one of its interfaces: a reporter
// holds it, a meter does not. memstore and pgstore each ship one; metertest
// checks it when its Env sets one.
type LedgerReader interface {
	// SumCharges returns one page of sums, each one group's: the charges of
	// req.TenantID taken in [req.From, req.To) — by the store's clock, as
	// ChargeRecord.At has it — grouped by unit always, by req.Bucket, and
	// by whichever of operation, actor and capability req names. Within a
	// bucket the largest charged come first, so the first page of Limit
	// rows is the top Limit; buckets come in time order. A refund counts in
	// the group of the charge it returns, whenever it is made: a closed
	// period's sums still move. A sum past MaxNanos is ErrCounterOverflow.
	SumCharges(ctx context.Context, req SumChargesRequest) (Page[ChargeSum], error)
}

// SumChargesRequest asks for one page of a tenant's charge sums.
type SumChargesRequest struct {
	// TenantID is the tenant whose charges are summed. Required: a sum
	// across tenants is not this request.
	TenantID uuid.UUID
	// From and To bound the period, From included, To not; From before To.
	From, To time.Time
	// Bucket splits the period at UTC boundaries; ChargeBucketNone sums it
	// whole, and so does ChargeBucketUnspecified, which takes it.
	Bucket ChargeBucket
	// ByOp, ByActor and ByCapability group the sums by the charge's Op,
	// Actor and capability as well.
	ByOp, ByActor, ByCapability bool
	// Limit is the page size; see PageLimit. Negative is refused.
	Limit int
	// Cursor is the one the previous page returned; empty for the first.
	Cursor string
}

// Validate refuses a request with no tenant, a period that is empty or
// unbounded, a bucket this module does not define, or a negative page size.
func (r SumChargesRequest) Validate() error {
	switch {
	case r.TenantID == uuid.Nil:
		return invalidRequest("sum charges: tenant id required")
	case r.From.IsZero() || r.To.IsZero():
		return invalidRequest("sum charges: From and To required")
	case !r.From.Before(r.To):
		return invalidRequest("sum charges: From %s is not before To %s", r.From, r.To)
	case r.Limit < 0:
		return invalidRequest("sum charges: limit %d is negative", r.Limit)
	}
	return r.Bucket.Validate()
}

// ChargeSum is one group's sums. The fields a request did not group by are
// zero.
type ChargeSum struct {
	// BucketStart is where the bucket begins, in UTC; the zero time when
	// the request sums the period whole. A bucket at either end of the
	// period sums only the charges within it.
	BucketStart time.Time
	UnitCode    string
	Op          Op
	Actor       string
	// CapabilityID is the capability charged.
	CapabilityID uuid.UUID
	// Charged is what the group's charges took; Refunded, what has been
	// returned from them since. Charged less Refunded is the net.
	Charged  Nanos
	Refunded Nanos
	// Charges counts the group's charges.
	Charges int64
	_       noCompare
}

// ChargeBucket is how SumCharges splits a period in time.
type ChargeBucket uint8

// The buckets, each cut at UTC boundaries. A new one is appended, so that no
// bucket's number changes.
const (
	// ChargeBucketUnspecified takes the default, ChargeBucketNone: a request
	// that names no bucket sums its period whole.
	ChargeBucketUnspecified ChargeBucket = iota
	ChargeBucketHour
	ChargeBucketDay
	ChargeBucketMonth
	// ChargeBucketWeek is an ISO week, from Monday 00:00 UTC.
	ChargeBucketWeek
	// ChargeBucketNone sums the period whole, in one bucket.
	ChargeBucketNone
	chargeBucketEnd // one past the last
)

// String is the bucket's name.
func (b ChargeBucket) String() string {
	switch b {
	case ChargeBucketUnspecified:
		return "unspecified"
	case ChargeBucketNone:
		return "none"
	case ChargeBucketHour:
		return "hour"
	case ChargeBucketDay:
		return "day"
	case ChargeBucketMonth:
		return "month"
	case ChargeBucketWeek:
		return "week"
	default:
		return fmt.Sprintf("ChargeBucket(%d)", uint8(b))
	}
}

// Validate refuses a bucket this module does not define.
func (b ChargeBucket) Validate() error {
	if b >= chargeBucketEnd {
		return invalidRequest("sum charges: bucket %s is not one this module defines", b)
	}
	return nil
}

// Start is where the bucket holding t begins, in UTC; the zero time for
// ChargeBucketNone, which holds the whole period, and for
// ChargeBucketUnspecified, which takes it.
func (b ChargeBucket) Start(t time.Time) time.Time {
	t = t.UTC()
	switch b {
	case ChargeBucketHour:
		return t.Truncate(time.Hour)
	case ChargeBucketDay:
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	case ChargeBucketMonth:
		return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
	case ChargeBucketWeek:
		return time.Date(t.Year(), t.Month(), t.Day()-daysSinceMonday(t.Weekday()), 0, 0, 0, 0, time.UTC)
	default:
		return time.Time{}
	}
}

// daysSinceMonday is how many days into its ISO week, which starts on a
// Monday, a day of weekday d falls.
func daysSinceMonday(d time.Weekday) int {
	return (int(d) - int(time.Monday) + daysPerWeek) % daysPerWeek
}

// daysPerWeek is the length of a week, in days.
const daysPerWeek = 7
