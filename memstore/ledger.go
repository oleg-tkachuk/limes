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

// Ledger implements meter.LedgerReader over a UsageStore's ledger.
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

// sum groups the tenant's charges in the period as req asks.
func (l *Ledger) sum(req meter.SumChargesRequest) ([]meter.ChargeSum, error) {
	l.usage.mu.Lock()
	defer l.usage.mu.Unlock()
	groups := map[sumKey]*meter.ChargeSum{}
	for _, e := range l.usage.ledger {
		if e.TenantID != req.TenantID || e.At.Before(req.From) || !e.At.Before(req.To) {
			continue
		}
		k := sumKey{Bucket: req.Bucket.Start(e.At), Unit: e.UnitCode}
		if req.ByOp {
			k.Op = e.Op
		}
		if req.ByActor {
			k.Actor = e.Actor
		}
		if req.ByCapability {
			k.CapID = e.CapabilityID
		}
		g, ok := groups[k]
		if !ok {
			g = &meter.ChargeSum{BucketStart: k.Bucket, UnitCode: k.Unit, Op: k.Op, Actor: k.Actor, CapabilityID: k.CapID}
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
	return sumPlace{Key: sumKey{Bucket: s.BucketStart, Unit: s.UnitCode, Op: s.Op, Actor: s.Actor, CapID: s.CapabilityID}, Charged: s.Charged}
}

// compareSums is SumCharges' order: buckets in time order, the largest
// charged first within one, then the group's fields ascending.
func compareSums(a, b sumPlace) int {
	return cmp.Or(
		a.Key.Bucket.Compare(b.Key.Bucket),
		cmp.Compare(b.Charged, a.Charged),
		cmp.Compare(a.Key.Unit, b.Key.Unit),
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
