package otelmetric_test

// Benchmarks of what the instruments add: to a charge through Instrument,
// and to a revocation check once InstrumentRevocationCache reports the
// cache, against the in-memory reference stores. Each reports its
// allocations.
//
// The weekly benchmark workflow runs this file on this commit and, copied
// in, on the newest otelmetric release, and compares the two with
// benchstat (.github/ci/bench.ts). It uses the exported API alone, and
// nothing of the package's other test files, so the release compiles it as
// it is.

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/memstore"
	"github.com/oleg-tkachuk/limes/meter"
	"github.com/oleg-tkachuk/limes/otelmetric"
)

const (
	benchUnit = "USD"
	// benchCachedIDs is how many capabilities the cache benchmarks ask
	// about.
	benchCachedIDs = 1024
)

// benchProvider is a meter provider whose reader collects on demand.
func benchProvider() (*sdkmetric.MeterProvider, *sdkmetric.ManualReader) {
	reader := sdkmetric.NewManualReader()
	return sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)), reader
}

// A charge through the instrumented store: the store's own work and every
// measurement taken of it.
func BenchmarkInstrumentCharge(b *testing.B) {
	b.ReportAllocs()
	ctx := context.Background()
	records, err := memstore.NewStore(memstore.StoreConfig{})
	if err != nil {
		b.Fatal(err)
	}
	store, err := memstore.NewUsageStore(memstore.UsageStoreConfig{Records: records})
	if err != nil {
		b.Fatal(err)
	}
	provider, _ := benchProvider()
	usage, err := otelmetric.Instrument(store, otelmetric.WithMeterProvider(provider))
	if err != nil {
		b.Fatal(err)
	}
	now := time.Now()
	c := limes.Capability{
		ID: uuid.New(), IssuedAt: now, ExpiresAt: now.Add(time.Hour),
		Subject: limes.Principal{Type: limes.PrincipalTypeService, TenantID: uuid.New(), Subject: "svc"},
		Caveats: limes.Caveats{
			Ops: []limes.Op{limes.OpGet}, Requests: limes.RequestCeiling{Unlimited: true},
			Budget: limes.BudgetCeiling{Unlimited: true, UnitCode: benchUnit},
		},
	}
	if err := records.Insert(ctx, limes.InsertRequest{Capability: c}); err != nil {
		b.Fatal(err)
	}
	subject, err := limes.StoredMeterSubject(ctx, records, c.ID)
	if err != nil {
		b.Fatal(err)
	}
	req := meter.ChargeRequest{Subject: subject, Amount: limes.MustParseAmount("0.001")}
	for b.Loop() {
		if _, err := usage.Charge(ctx, req, nil); err != nil {
			b.Fatal(err)
		}
	}
}

// benchCache is a revocation cache holding benchCachedIDs answers, reported
// on a provider of its own, and their ids.
func benchCache(b *testing.B) (*limes.RevocationCache, *sdkmetric.ManualReader, []uuid.UUID) {
	b.Helper()
	b.ReportAllocs()
	records, err := memstore.NewStore(memstore.StoreConfig{})
	if err != nil {
		b.Fatal(err)
	}
	cache, err := limes.NewRevocationCache(limes.RevocationCacheConfig{Capabilities: records, Biscuits: records})
	if err != nil {
		b.Fatal(err)
	}
	ids := make([]uuid.UUID, benchCachedIDs)
	for i := range ids {
		ids[i] = uuid.New()
		if _, err := cache.IsRevoked(context.Background(), ids[i]); err != nil {
			b.Fatal(err)
		}
	}
	provider, reader := benchProvider()
	reg, err := otelmetric.InstrumentRevocationCache(cache, otelmetric.WithMeterProvider(provider))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { unregister(b, reg) })
	return cache, reader, ids
}

func unregister(b *testing.B, reg metric.Registration) {
	b.Helper()
	if err := reg.Unregister(); err != nil {
		b.Error(err)
	}
}

// One collection of a reported cache's counts: what a provider pays each
// time it exports.
func BenchmarkInstrumentRevocationCacheCollect(b *testing.B) {
	_, reader, _ := benchCache(b)
	ctx := context.Background()
	var rm metricdata.ResourceMetrics
	for b.Loop() {
		if err := reader.Collect(ctx, &rm); err != nil {
			b.Fatal(err)
		}
	}
}

// Checks from every processor at once on a reported cache, collected
// without pause beside them: the instruments are read only when the
// provider collects, so the checks should cost what they cost unreported.
func BenchmarkInstrumentRevocationCacheHitParallel(b *testing.B) {
	cache, reader, ids := benchCache(b)
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		var rm metricdata.ResourceMetrics
		for {
			select {
			case <-stop:
				return
			default:
				if err := reader.Collect(context.Background(), &rm); err != nil {
					b.Error(err)
					return
				}
			}
		}
	}()
	b.Cleanup(func() {
		close(stop)
		<-done
	})
	ctx := context.Background()
	var next atomic.Int64
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, err := cache.IsRevoked(ctx, ids[next.Add(1)%benchCachedIDs]); err != nil {
				b.Error(err)
				return
			}
		}
	})
}
