package memstore

import (
	"bytes"
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/meter"
)

// Ledger implements meter.LedgerReader and meter.LedgerHousekeeping over a
// UsageStore's ledger.
type Ledger struct {
	usage *UsageStore
}

// NewLedger returns the ledger reader of usage.
func NewLedger(usage *UsageStore) *Ledger {
	return &Ledger{usage: usage}
}

// sumKey is one group of SumCharges: the fields it groups by.
type sumKey struct {
	Bucket time.Time
	Unit   string
	Tenant uuid.UUID
	Op     limes.Op
	Actor  string
	CapID  uuid.UUID
}

// sumPlace is a place in SumCharges' order, which its cursor names: the
// last row's group and its charged sum.
type sumPlace struct {
	Key     sumKey
	Charged limes.Nanos
}

// SumCharges implements meter.LedgerReader.
func (l *Ledger) SumCharges(_ context.Context, req meter.SumChargesRequest) (limes.Page[meter.ChargeSum], error) {
	if err := req.Validate(); err != nil {
		return limes.Page[meter.ChargeSum]{}, err
	}
	var after *sumPlace
	if req.Cursor != "" {
		p, err := decodeSumCursor(req.Cursor)
		if err != nil {
			return limes.Page[meter.ChargeSum]{}, err
		}
		after = &p
	}
	sums, err := l.sum(req)
	if err != nil {
		return limes.Page[meter.ChargeSum]{}, err
	}
	slices.SortFunc(sums, func(a, b meter.ChargeSum) int { return compareSums(sumPlaceOf(a), sumPlaceOf(b)) })
	if after != nil {
		sums = slices.DeleteFunc(sums, func(s meter.ChargeSum) bool { return compareSums(sumPlaceOf(s), *after) <= 0 })
	}
	limit := limes.PageLimit(req.Limit)
	if len(sums) <= limit {
		return limes.Page[meter.ChargeSum]{Items: sums}, nil
	}
	sums = sums[:limit]
	return limes.Page[meter.ChargeSum]{Items: sums, Next: sumPlaceOf(sums[len(sums)-1]).encode()}, nil
}

// sum groups the charges of the tenant, or of every tenant, in the period
// and the filter as req asks.
func (l *Ledger) sum(req meter.SumChargesRequest) ([]meter.ChargeSum, error) {
	l.usage.mu.Lock()
	defer l.usage.mu.Unlock()
	groups := map[sumKey]*meter.ChargeSum{}
	for _, e := range l.usage.ledger {
		if !req.AllTenants && e.TenantID != req.TenantID || e.At.Before(req.From) || !e.At.Before(req.To) ||
			!req.Filter.Admits(e.TenantID, e.Op, e.Actor, e.CapabilityID) {
			continue
		}
		k := sumKey{Bucket: req.Bucket.Start(e.At), Unit: e.UnitCode}
		if req.Groups(meter.ChargeDimensionTenant) {
			k.Tenant = e.TenantID
		}
		if req.Groups(meter.ChargeDimensionOp) {
			k.Op = e.Op
		}
		if req.Groups(meter.ChargeDimensionActor) {
			k.Actor = e.Actor
		}
		if req.Groups(meter.ChargeDimensionCapability) {
			k.CapID = e.CapabilityID
		}
		g, ok := groups[k]
		if !ok {
			g = &meter.ChargeSum{BucketStart: k.Bucket, UnitCode: k.Unit, TenantID: k.Tenant, Op: k.Op, Actor: k.Actor, CapabilityID: k.CapID}
			groups[k] = g
		}
		var err error
		if g.Charged, err = addSum(g.Charged, e.Amount); err != nil {
			return nil, err
		}
		if g.Refunded, err = addSum(g.Refunded, e.RefundedAmount); err != nil {
			return nil, err
		}
		g.Charges++
	}
	out := make([]meter.ChargeSum, 0, len(groups))
	for _, g := range groups {
		out = append(out, *g)
	}
	return out, nil
}

// addSum is a + b, or ErrCounterOverflow past MaxNanos. Both are within it,
// so the sum fits an int64.
func addSum(a, b limes.Nanos) (limes.Nanos, error) {
	if s := a + b; s <= limes.MaxNanos {
		return s, nil
	}
	return 0, fmt.Errorf("%w: a charge sum past %s", meter.ErrCounterOverflow, limes.MaxNanos)
}

// sumPlaceOf is s's place in SumCharges' order.
func sumPlaceOf(s meter.ChargeSum) sumPlace {
	return sumPlace{Key: sumKey{Bucket: s.BucketStart, Unit: s.UnitCode, Tenant: s.TenantID, Op: s.Op, Actor: s.Actor, CapID: s.CapabilityID}, Charged: s.Charged}
}

// compareSums is SumCharges' order: buckets in time order, the largest
// charged first within one, then the group's fields ascending — unit,
// tenant, operation, actor, capability.
func compareSums(a, b sumPlace) int {
	return cmp.Or(
		a.Key.Bucket.Compare(b.Key.Bucket),
		cmp.Compare(b.Charged, a.Charged),
		cmp.Compare(a.Key.Unit, b.Key.Unit),
		bytes.Compare(a.Key.Tenant[:], b.Key.Tenant[:]),
		cmp.Compare(a.Key.Op, b.Key.Op),
		cmp.Compare(a.Key.Actor, b.Key.Actor),
		bytes.Compare(a.Key.CapID[:], b.Key.CapID[:]),
	)
}

// encode is the cursor as SumCharges hands it out: opaque, URL-safe.
func (p sumPlace) encode() string {
	raw, err := json.Marshal(p)
	if err != nil {
		panic(err) // a struct of strings, times, integers and a uuid always encodes
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// decodeSumCursor reads a cursor encode wrote; anything else is
// ErrInvalidRequest.
func decodeSumCursor(s string) (sumPlace, error) {
	bad := fmt.Errorf("%w: memstore: charge sum cursor %q", limes.ErrInvalidRequest, s)
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return sumPlace{}, bad
	}
	var p sumPlace
	if err := json.Unmarshal(raw, &p); err != nil || p.Charged.Validate() != nil {
		return sumPlace{}, bad
	}
	return p, nil
}

var _ meter.LedgerReader = (*Ledger)(nil)

// PurgeCharges implements meter.LedgerHousekeeping: the charges taken more
// than req.Retention() ago by the store's clock, oldest first, at most the
// batch's limit, leave the ledger and its indexes.
func (l *Ledger) PurgeCharges(_ context.Context, req meter.PurgeChargesRequest) (meter.PurgeChargesResult, error) {
	if err := req.Validate(); err != nil {
		return meter.PurgeChargesResult{}, err
	}
	s := l.usage
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := s.nowFn().Add(-req.Retention())
	var due []int
	for i, e := range s.ledger {
		if e.At.Before(cutoff) {
			due = append(due, i)
		}
	}
	slices.SortStableFunc(due, func(a, b int) int { return s.ledger[a].At.Compare(s.ledger[b].At) })
	due = due[:min(len(due), meter.HousekeepingLimit(req.Limit))]
	if len(due) == 0 {
		return meter.PurgeChargesResult{}, nil
	}
	gone := make(map[int]bool, len(due))
	for _, i := range due {
		gone[i] = true
	}
	kept := s.ledger
	s.ledger = nil
	clear(s.byCharge)
	clear(s.byRef)
	clear(s.byReservation)
	for i, e := range kept {
		if !gone[i] {
			s.appendLocked(e)
		}
	}
	return meter.PurgeChargesResult{Purged: int64(len(due))}, nil
}

var _ meter.LedgerHousekeeping = (*Ledger)(nil)
