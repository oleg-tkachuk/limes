package otelmetric_test

import (
	"context"
	"fmt"
	"log"
	"slices"
	"time"

	"github.com/google/uuid"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/memstore"
	"github.com/oleg-tkachuk/limes/meter"
	"github.com/oleg-tkachuk/limes/otelmetric"
)

// metricNames collects reader once and returns the names of the metrics it
// read, sorted.
func metricNames(reader *sdkmetric.ManualReader) []string {
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		log.Fatal(err)
	}
	var names []string
	for _, scope := range rm.ScopeMetrics {
		for _, m := range scope.Metrics {
			names = append(names, m.Name)
		}
	}
	slices.Sort(names)
	return names
}

func ExampleInstrument() {
	ctx := context.Background()
	records, err := memstore.NewStore(memstore.StoreConfig{})
	if err != nil {
		log.Fatal(err)
	}
	store, err := memstore.NewUsageStore(memstore.UsageStoreConfig{Records: records})
	if err != nil {
		log.Fatal(err)
	}
	// In production the provider exports; a manual reader shows what it would.
	reader := sdkmetric.NewManualReader()
	usage, err := otelmetric.Instrument(store,
		otelmetric.WithMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))),
	)
	if err != nil {
		log.Fatal(err)
	}

	c := limes.Capability{
		ID: uuid.New(), IssuedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
		Subject: limes.Principal{Type: limes.PrincipalTypeService, TenantID: uuid.New(), Subject: "svc"},
		Caveats: limes.Caveats{
			Ops: []limes.Op{limes.OpGet}, Requests: limes.RequestCeiling{Max: 10},
			Budget: limes.BudgetCeiling{Max: limes.MustParseAmount("1"), UnitCode: "USD"},
		},
	}
	if err := records.Insert(ctx, limes.InsertRequest{Capability: c}); err != nil {
		log.Fatal(err)
	}
	subject, err := limes.StoredMeterSubject(ctx, records, c.ID)
	if err != nil {
		log.Fatal(err)
	}
	if _, err := usage.Charge(ctx, meter.ChargeRequest{Subject: subject, Amount: limes.MustParseAmount("0.5")}, nil); err != nil {
		log.Fatal(err)
	}
	for _, name := range metricNames(reader) {
		fmt.Println(name)
	}
	// Output:
	// limes.charge.amount
	// limes.charge.current_spend
	// limes.charge.decisions
}

func ExampleInstrumentRevocationCache() {
	ctx := context.Background()
	records, err := memstore.NewStore(memstore.StoreConfig{})
	if err != nil {
		log.Fatal(err)
	}
	cache, err := limes.NewRevocationCache(limes.RevocationCacheConfig{Capabilities: records, Biscuits: records})
	if err != nil {
		log.Fatal(err)
	}
	reader := sdkmetric.NewManualReader()
	reg, err := otelmetric.InstrumentRevocationCache(cache,
		otelmetric.WithMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))),
	)
	if err != nil {
		log.Fatal(err)
	}

	if _, err := cache.IsRevoked(ctx, uuid.New()); err != nil {
		log.Fatal(err)
	}
	// The counts are read when the provider collects, not on every check.
	for _, name := range metricNames(reader) {
		fmt.Println(name)
	}
	// Unregister stops the reports.
	if err := reg.Unregister(); err != nil {
		log.Fatal(err)
	}
	// Output:
	// limes.revocation_cache.clears
	// limes.revocation_cache.clears_revoked
	// limes.revocation_cache.entries
	// limes.revocation_cache.evictions
	// limes.revocation_cache.hits
	// limes.revocation_cache.lookups
	// limes.revocation_cache.misses
}
