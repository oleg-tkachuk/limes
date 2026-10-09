package limes_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/memstore"
)

// countingProvider counts every measurement any of its instruments takes.
type countingProvider struct {
	noop.MeterProvider
	n *atomic.Int64
}

func (p countingProvider) Meter(string, ...metric.MeterOption) metric.Meter {
	return countingMeter{n: p.n}
}

type countingMeter struct {
	noop.Meter
	n *atomic.Int64
}

func (m countingMeter) Int64Counter(string, ...metric.Int64CounterOption) (metric.Int64Counter, error) {
	return countingInt64{n: m.n}, nil
}

func (m countingMeter) Float64Counter(string, ...metric.Float64CounterOption) (metric.Float64Counter, error) {
	return countingFloat64{n: m.n}, nil
}

func (m countingMeter) Float64Histogram(string, ...metric.Float64HistogramOption) (metric.Float64Histogram, error) {
	return countingHistogram{n: m.n}, nil
}

type countingInt64 struct {
	noop.Int64Counter
	n *atomic.Int64
}

func (c countingInt64) Add(context.Context, int64, ...metric.AddOption) { c.n.Add(1) }

type countingFloat64 struct {
	noop.Float64Counter
	n *atomic.Int64
}

func (c countingFloat64) Add(context.Context, float64, ...metric.AddOption) { c.n.Add(1) }

type countingHistogram struct {
	noop.Float64Histogram
	n *atomic.Int64
}

func (c countingHistogram) Record(context.Context, float64, ...metric.RecordOption) { c.n.Add(1) }

// measurements is the count of every measurement taken in this test binary.
// OpenTelemetry binds the decorator's instruments to the first provider set,
// so the tests share one provider, set once, and read deltas of its count.
var (
	measurements     atomic.Int64
	measurementsOnce sync.Once
)

func countMeasurements() *atomic.Int64 {
	measurementsOnce.Do(func() { otel.SetMeterProvider(countingProvider{n: &measurements}) })
	return &measurements
}

// A replayed charge or settle moved nothing, so the decorator counts nothing:
// counting it again would report spend twice.
func TestMeteringStoreCountsNoReplay(t *testing.T) {
	count := countMeasurements()
	ctx := context.Background()
	usage := limes.WithMetering[struct{}](memstore.NewUsage[struct{}](nil))
	tenant, capID := uuid.New(), uuid.New()
	charge := limes.ChargeRequest{CapabilityID: capID, TenantID: tenant, Amount: limes.MustParseAmount("1"), ExternalRef: "call-1"}

	before := count.Load()
	if _, err := usage.Charge(ctx, charge, nil); err != nil {
		t.Fatal(err)
	}
	if count.Load() == before {
		t.Fatal("a charge took no measurement")
	}
	after := count.Load()
	if r, err := usage.Charge(ctx, charge, nil); err != nil || !r.Replayed {
		t.Fatalf("repeated charge = %+v, %v; want a replay", r, err)
	}
	if got := count.Load(); got != after {
		t.Errorf("a replayed charge took %d measurements", got-after)
	}

	res, err := usage.Reserve(ctx, limes.ReserveRequest{CapabilityID: capID, TenantID: tenant, Amount: limes.MustParseAmount("1")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := usage.Settle(ctx, limes.SettleRequest{ReservationID: res.ID, Amount: limes.MustParseAmount("1")}, nil); err != nil {
		t.Fatal(err)
	}
	after = count.Load()
	if r, err := usage.Settle(ctx, limes.SettleRequest{ReservationID: res.ID, Amount: limes.MustParseAmount("1")}, nil); err != nil || !r.Replayed {
		t.Fatalf("repeated settle = %+v, %v; want a replay", r, err)
	}
	if got := count.Load(); got != after {
		t.Errorf("a replayed settle took %d measurements", got-after)
	}
}

// hiddenCopies is a UsageStore that reads no copy counters: memstore's
// usage store with CopyUsage out of reach.
type hiddenCopies struct {
	limes.UsageStore[struct{}]
}

// The decorator reads copy counters exactly when the store it wraps does.
func TestWithMeteringKeepsCopyUsage(t *testing.T) {
	if limes.WithMetering[struct{}](nil) != nil {
		t.Fatal("WithMetering(nil) must stay nil")
	}
	inner := memstore.NewUsage[struct{}](nil)
	if _, ok := limes.WithMetering[struct{}](inner).(limes.CopyUsageReader); !ok {
		t.Error("wrapping a store that reads copy counters hid them")
	}
	if _, ok := limes.WithMetering[struct{}](hiddenCopies{inner}).(limes.CopyUsageReader); ok {
		t.Error("wrapping a store that reads no copy counters claimed to read them")
	}

	ctx := context.Background()
	usage := limes.WithMetering[struct{}](inner)
	copyID := []byte("copy-1")
	copies := []limes.CopyCeiling{{RevocationID: copyID, MaxRequests: 10}}
	if _, err := usage.Bump(ctx, limes.BumpRequest{CapabilityID: uuid.New(), TenantID: uuid.New(), Copies: copies}); err != nil {
		t.Fatal(err)
	}
	got, err := usage.(limes.CopyUsageReader).CopyUsage(ctx, [][]byte{copyID})
	if err != nil || len(got) != 1 || got[0].RequestCount != 1 {
		t.Errorf("CopyUsage through the decorator = %+v, %v; want one request counted", got, err)
	}
}

// Every method reaches the store the decorator wraps, and the ones that move
// a counter are measured.
func TestMeteringStorePassesEveryCallThrough(t *testing.T) {
	count := countMeasurements()
	ctx := context.Background()
	usage := limes.WithMetering[struct{}](memstore.NewUsage[struct{}](nil))
	tenant, capID := uuid.New(), uuid.New()
	measured := func(name string, call func() error) {
		t.Helper()
		before := count.Load()
		if err := call(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if count.Load() == before {
			t.Errorf("%s took no measurement", name)
		}
	}

	if _, err := usage.SetTenantBudget(ctx, limes.SetTenantBudgetRequest{TenantID: tenant, MaxBudgetAmount: limes.MustParseAmount("10")}); err != nil {
		t.Fatal(err)
	}
	measured("Bump", func() error {
		_, err := usage.Bump(ctx, limes.BumpRequest{CapabilityID: capID, TenantID: tenant})
		return err
	})
	var receipt limes.ChargeReceipt
	measured("Charge", func() (err error) {
		receipt, err = usage.Charge(ctx, limes.ChargeRequest{CapabilityID: capID, TenantID: tenant, Amount: limes.MustParseAmount("2"), ExternalRef: "r"}, nil)
		return err
	})
	measured("Refund", func() error {
		_, err := usage.Refund(ctx, limes.RefundRequest{ChargeID: receipt.ChargeID, Amount: limes.MustParseAmount("1")})
		return err
	})
	var res limes.Reservation
	measured("Reserve", func() (err error) {
		res, err = usage.Reserve(ctx, limes.ReserveRequest{CapabilityID: capID, TenantID: tenant, Amount: limes.MustParseAmount("1")})
		return err
	})

	if err := usage.Release(ctx, res.ID); err != nil {
		t.Errorf("Release: %v", err)
	}
	if u, err := usage.GetUsage(ctx, capID); err != nil || u.RequestCount != 1 || u.SpentAmount != limes.NanosPerUnit {
		t.Errorf("GetUsage = %+v, %v", u, err)
	}
	if b, err := usage.GetTenantBudget(ctx, tenant); err != nil || b.SpentAmount != limes.NanosPerUnit {
		t.Errorf("GetTenantBudget = %+v, %v", b, err)
	}
	if l, _, err := usage.ListTenantBudgets(ctx, limes.ListTenantBudgetsRequest{}); err != nil || len(l) != 1 {
		t.Errorf("ListTenantBudgets = %+v, %v", l, err)
	}
	if c, err := usage.GetCharge(ctx, receipt.ChargeID); err != nil || c.Refunded != limes.NanosPerUnit {
		t.Errorf("GetCharge = %+v, %v", c, err)
	}
	if c, err := usage.ChargeByRef(ctx, capID, "r"); err != nil || c.ChargeID != receipt.ChargeID {
		t.Errorf("ChargeByRef = %+v, %v", c, err)
	}
	if n, err := usage.ReleaseExpired(ctx); err != nil || n != 0 {
		t.Errorf("ReleaseExpired = %d, %v", n, err)
	}
	if err := usage.Delete(ctx, uuid.New()); err != nil {
		t.Errorf("Delete of a row never written: %v", err)
	}
	// The store knows no records, so the capability's usage is an orphan.
	if n, err := usage.PurgeOrphans(ctx); err != nil || n != 1 {
		t.Errorf("PurgeOrphans = %d, %v; want the one orphan", n, err)
	}
	if _, err := usage.GetUsage(ctx, capID); err == nil {
		t.Error("GetUsage found a purged row")
	}
}
