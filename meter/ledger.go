package meter

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes"
)

// LedgerReader sums the charges ledger over a period, for billing and
// reporting — one tenant's, or every tenant's: totals, the largest spenders
// by tenant, operation, actor or capability, and series by hour, day, week
// or month. It reads the ledger the Meter writes, so
// a consumer reports from it rather than from a store's tables. It is a type
// of its own, beside UsageStore rather than one of its interfaces: a reporter
// holds it, a meter does not. memstore and pgstore each ship one; metertest
// checks it when its Env sets one.
type LedgerReader interface {
	// SumCharges returns one page of sums, each one group's: the charges of
	// req.TenantID, or of every tenant with req.AllTenants, taken in
	// [req.From, req.To) — by the store's clock, as ChargeRecord.At has it
	// — and kept by req.Filter, grouped by unit always, by req.Bucket, and
	// by each dimension in req.GroupBy. Amounts in different units are
	// never summed together. Within a bucket the largest charged come
	// first, so the first page of Limit rows is the top Limit; buckets come
	// in time order. A refund counts in the group of the charge it returns,
	// whenever it is made: a closed period's sums still move. A sum past
	// MaxNanos is ErrCounterOverflow. It sums the charges the ledger still
	// holds: one older than the ledger's retention (MinChargeRetention) may
	// be gone, and a period that old sums what is left of it.
	//
	// A sum across tenants is a read of every tenant's ledger: a store that
	// scopes calls to a tenant refuses it under any narrower scope, a tenant
	// filter included, with limes.ErrScopeMismatch, as it refuses
	// ListTenantBudgets.
	SumCharges(ctx context.Context, req SumChargesRequest) (limes.Page[ChargeSum], error)
}

// SumChargesRequest asks for one page of charge sums.
type SumChargesRequest struct {
	// TenantID is the tenant whose charges are summed; AllTenants sums
	// every tenant's instead. Exactly one of them is required.
	TenantID   uuid.UUID
	AllTenants bool
	// From and To bound the period, From included, To not; From before To.
	From, To time.Time
	// Bucket splits the period at UTC boundaries; the zero value,
	// ChargeBucketNone, sums it whole.
	Bucket ChargeBucket
	// GroupBy groups the sums by each dimension it names as well, in any
	// order; none sums each unit and bucket whole. A dimension named twice
	// is refused.
	GroupBy []ChargeDimension
	// Filter keeps only the charges it admits; the zero value keeps all.
	Filter ChargeFilter
	// Limit is the page size; see PageLimit. Negative is refused.
	Limit int
	// Cursor is the one the previous page returned; empty for the first.
	Cursor string
}

// Validate refuses a request that names no tenant and not every tenant, or
// both; a period that is empty or unbounded; a bucket or a dimension this
// module does not define, or a dimension named twice; a filter Validate
// refuses, or one of tenants beside a single tenant; or a negative page
// size.
func (r SumChargesRequest) Validate() error {
	switch {
	case r.TenantID == uuid.Nil && !r.AllTenants:
		return invalidRequest("sum charges: a tenant id or AllTenants required")
	case r.TenantID != uuid.Nil && r.AllTenants:
		return invalidRequest("sum charges: tenant id %s beside AllTenants", r.TenantID)
	case r.TenantID != uuid.Nil && len(r.Filter.Tenants) > 0:
		return invalidRequest("sum charges: a tenant filter beside tenant id %s", r.TenantID)
	case r.From.IsZero() || r.To.IsZero():
		return invalidRequest("sum charges: From and To required")
	case !r.From.Before(r.To):
		return invalidRequest("sum charges: From %s is not before To %s", r.From, r.To)
	case r.Limit < 0:
		return invalidRequest("sum charges: limit %d is negative", r.Limit)
	}
	if err := r.Bucket.Validate(); err != nil {
		return err
	}
	seen := map[ChargeDimension]bool{}
	for _, d := range r.GroupBy {
		if err := d.Validate(); err != nil {
			return err
		}
		if seen[d] {
			return invalidRequest("sum charges: dimension %s named twice", d)
		}
		seen[d] = true
	}
	return r.Filter.Validate()
}

// Groups reports whether r groups the sums by d.
func (r SumChargesRequest) Groups(d ChargeDimension) bool { return slices.Contains(r.GroupBy, d) }

// ChargeDimension is a field of a charge SumCharges can group its sums by.
// The unit is not one: sums are always kept apart by unit.
type ChargeDimension uint8

// The dimensions. A new one is appended, so that no dimension's number
// changes.
const (
	// ChargeDimensionUnspecified is the zero value, which names no
	// dimension and is refused.
	ChargeDimensionUnspecified ChargeDimension = iota
	// ChargeDimensionTenant groups by the charge's tenant.
	ChargeDimensionTenant
	// ChargeDimensionOp groups by the charge's Op.
	ChargeDimensionOp
	// ChargeDimensionActor groups by the charge's Actor.
	ChargeDimensionActor
	// ChargeDimensionCapability groups by the capability charged.
	ChargeDimensionCapability
	chargeDimensionEnd // one past the last
)

// String is the dimension's name.
func (d ChargeDimension) String() string {
	switch d {
	case ChargeDimensionUnspecified:
		return "unspecified"
	case ChargeDimensionTenant:
		return "tenant"
	case ChargeDimensionOp:
		return "op"
	case ChargeDimensionActor:
		return "actor"
	case ChargeDimensionCapability:
		return "capability"
	default:
		return fmt.Sprintf("ChargeDimension(%d)", uint8(d))
	}
}

// Validate refuses the zero value and a dimension this module does not
// define.
func (d ChargeDimension) Validate() error {
	if d == ChargeDimensionUnspecified || d >= chargeDimensionEnd {
		return invalidRequest("sum charges: dimension %s is not one this module defines", d)
	}
	return nil
}

// MaxChargeFilterValues bounds each list of a ChargeFilter, so that a
// filter stays one bounded match per charge.
const MaxChargeFilterValues = 1000

// ChargeFilter keeps the charges whose fields are among the values it
// lists, field by field; an empty list keeps every value of its field.
type ChargeFilter struct {
	// Tenants keeps the charges of these tenants. Only beside AllTenants:
	// a request for one tenant needs none.
	Tenants []uuid.UUID
	// Ops, Actors and Capabilities keep the charges of these operations,
	// actors and capabilities.
	Ops          []limes.Op
	Actors       []string
	Capabilities []uuid.UUID
}

// Validate refuses a list longer than MaxChargeFilterValues, and a value
// that names nothing: the nil uuid, an empty operation or actor.
func (f ChargeFilter) Validate() error {
	for name, n := range map[string]int{"tenants": len(f.Tenants), "ops": len(f.Ops), "actors": len(f.Actors), "capabilities": len(f.Capabilities)} {
		if n > MaxChargeFilterValues {
			return invalidRequest("sum charges: %d %s in the filter, at most %d", n, name, MaxChargeFilterValues)
		}
	}
	if slices.Contains(f.Tenants, uuid.Nil) || slices.Contains(f.Capabilities, uuid.Nil) {
		return invalidRequest("sum charges: a filter names the nil id")
	}
	if slices.Contains(f.Ops, "") || slices.Contains(f.Actors, "") {
		return invalidRequest("sum charges: a filter names an empty operation or actor")
	}
	return nil
}

// Admits reports whether f keeps a charge of tenant, op, actor and
// capability, for a store that filters in Go.
func (f ChargeFilter) Admits(tenant uuid.UUID, op limes.Op, actor string, capability uuid.UUID) bool {
	return admitsValue(f.Tenants, tenant) && admitsValue(f.Ops, op) &&
		admitsValue(f.Actors, actor) && admitsValue(f.Capabilities, capability)
}

// admitsValue is whether a filter list keeps v: an empty one keeps all.
func admitsValue[T comparable](list []T, v T) bool {
	return len(list) == 0 || slices.Contains(list, v)
}

// ChargeSum is one group's sums. The fields a request did not group by are
// zero.
type ChargeSum struct {
	// BucketStart is where the bucket begins, in UTC; the zero time when
	// the request sums the period whole. A bucket at either end of the
	// period sums only the charges within it.
	BucketStart time.Time
	UnitCode    string
	// TenantID is the group's tenant, when the request groups by tenant.
	TenantID uuid.UUID
	Op       limes.Op
	Actor    string
	// CapabilityID is the capability charged.
	CapabilityID uuid.UUID
	// Charged is what the group's charges took; Refunded, what has been
	// returned from them since. Charged less Refunded is the net.
	Charged  limes.Nanos
	Refunded limes.Nanos
	// Charges counts the group's charges.
	Charges int64
	_       noCompare
}

// ChargeBucket is how SumCharges splits a period in time.
type ChargeBucket uint8

// The buckets, each cut at UTC boundaries, shortest first. A new one is
// appended, so that no bucket's number changes.
const (
	// ChargeBucketNone, the zero value, sums the period whole, in one
	// bucket: a request that names no bucket gets it.
	ChargeBucketNone ChargeBucket = iota
	ChargeBucketHour
	ChargeBucketDay
	// ChargeBucketWeek is an ISO week, from Monday 00:00 UTC.
	ChargeBucketWeek
	ChargeBucketMonth
	chargeBucketEnd // one past the last
)

// String is the bucket's name.
func (b ChargeBucket) String() string {
	switch b {
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
// ChargeBucketNone, which holds the whole period.
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

// LedgerHousekeeping removes old charges from the ledger, which otherwise
// grows by a row for every charge for as long as the store lives
// (ADR-0002). It is a type of its own beside UsageStore, as LedgerReader
// is: a housekeeping worker holds it. memstore's and pgstore's Ledger are
// each one; metertest checks it when its Env.Ledger is one and Env.Advance
// is set.
type LedgerHousekeeping interface {
	// PurgeCharges removes the charges taken more than req.Retention() ago,
	// by the store's clock, oldest first, at most req.Limit of them, of
	// every tenant the call's scope reaches, and reports how many it
	// removed. A worker calls it until a batch comes back short of the
	// limit. A charge removed takes its promises with it: its ExternalRef
	// charges anew, its settled hold is a reservation not found, Refund
	// and GetCharge answer ErrChargeNotFound, and SumCharges no longer
	// counts it. It refuses what PurgeChargesRequest.Validate refuses.
	PurgeCharges(ctx context.Context, req PurgeChargesRequest) (PurgeChargesResult, error)
}

// PurgeChargesRequest is one batch of LedgerHousekeeping.PurgeCharges.
type PurgeChargesRequest struct {
	// OlderThan is how long ago a charge must have been taken to go. Zero:
	// MinChargeRetention; anything shorter, negative included, is refused.
	OlderThan time.Duration
	// Limit bounds the charges removed. Zero: DefaultHousekeepingLimit;
	// negative is refused.
	Limit int
}

// Validate refuses a retention shorter than MinChargeRetention, other than
// zero, and a negative Limit, as ErrInvalidRequest.
func (r PurgeChargesRequest) Validate() error {
	if r.OlderThan != 0 && r.OlderThan < MinChargeRetention {
		return invalidRequest("purge charges: retention %s is shorter than %s", r.OlderThan, MinChargeRetention)
	}
	return validateHousekeepingLimit("purge charges", r.Limit)
}

// Retention is how long ago a charge must have been taken to go: OlderThan,
// or MinChargeRetention when it is zero.
func (r PurgeChargesRequest) Retention() time.Duration {
	if r.OlderThan == 0 {
		return MinChargeRetention
	}
	return r.OlderThan
}

// PurgeChargesResult is what one PurgeCharges removed.
type PurgeChargesResult struct {
	// Purged is the number of charges removed.
	Purged int64
	_      noCompare
}
