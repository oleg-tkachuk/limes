package otelmetric_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/memstore"
	"github.com/oleg-tkachuk/limes/otelmetric"
)

// Cache instrument names and attributes as an exporter reads them.
const (
	metricCacheHits       = "limes.revocation_cache.hits"
	metricCacheMisses     = "limes.revocation_cache.misses"
	metricCacheLookups    = "limes.revocation_cache.lookups"
	metricCacheEvictions  = "limes.revocation_cache.evictions"
	metricCacheEntries    = "limes.revocation_cache.entries"
	metricCacheClears     = "limes.revocation_cache.clears"
	metricClearsRevoked   = "limes.revocation_cache.clears_revoked"
	metricClearsNoLineage = "limes.revocation_cache.clears_without_lineage"
	metricLineage         = "limes.revocation_cache.lineage"
	metricSynced          = "limes.revocation_cache.synced"
	metricSyncs           = "limes.revocation_cache.syncs"
	metricSyncsLost       = "limes.revocation_cache.syncs_lost"
	metricLastSyncLoss    = "limes.revocation_cache.last_sync_loss"
	metricSignals         = "limes.revocation_cache.signals"
	metricSignalsWhole    = "limes.revocation_cache.signals_whole"
	attrKind              = "kind"
	kindCapability        = "capability"
	kindBiscuit           = "biscuit"
)

// instrumentedCache is a RevocationCache over memstore whose counts reader
// collects.
func instrumentedCache(t *testing.T) (*limes.RevocationCache, *sdkmetric.ManualReader, metric.Registration) {
	t.Helper()
	records, err := memstore.NewStore(memstore.StoreConfig{})
	if err != nil {
		t.Fatal(err)
	}
	cache, err := limes.NewRevocationCache(limes.RevocationCacheConfig{Capabilities: records, Biscuits: records})
	if err != nil {
		t.Fatal(err)
	}
	reader := sdkmetric.NewManualReader()
	reg, err := otelmetric.InstrumentRevocationCache(cache, otelmetric.WithMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))))
	if err != nil {
		t.Fatal(err)
	}
	return cache, reader, reg
}

// collected is every int64 data point of a collection, sum or gauge, by
// instrument name.
func collected(t *testing.T, reader *sdkmetric.ManualReader) map[string][]point {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	out := map[string][]point{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			switch data := m.Data.(type) {
			case metricdata.Sum[int64]:
				for _, dp := range data.DataPoints {
					out[m.Name] = append(out[m.Name], point{float64(dp.Value), dp.Attributes})
				}
			case metricdata.Gauge[int64]:
				for _, dp := range data.DataPoints {
					out[m.Name] = append(out[m.Name], point{float64(dp.Value), dp.Attributes})
				}
			}
		}
	}
	return out
}

// The cache's counts reach the provider by kind, read at collection.
func TestInstrumentRevocationCacheReportsItsCounts(t *testing.T) {
	ctx := context.Background()
	cache, reader, _ := instrumentedCache(t)
	id := uuid.New()
	_, _ = cache.IsRevoked(ctx, id)
	_, _ = cache.IsRevoked(ctx, id)
	_, _ = cache.IsRevoked(ctx, id)
	_, _ = cache.IsBiscuitRevoked(ctx, [][]byte{[]byte("a")})
	got := collected(t, reader)
	for _, tc := range []struct {
		name, kind string
		want       float64
	}{
		{metricCacheHits, kindCapability, 2},
		{metricCacheMisses, kindCapability, 1},
		{metricCacheLookups, kindCapability, 1},
		{metricCacheEntries, kindCapability, 1},
		{metricCacheEvictions, kindCapability, 0},
		{metricCacheHits, kindBiscuit, 0},
		{metricCacheMisses, kindBiscuit, 1},
		{metricCacheLookups, kindBiscuit, 1},
		{metricCacheEntries, kindBiscuit, 1},
	} {
		if v := valueAt(got[tc.name], attrKind, tc.kind); v != tc.want {
			t.Errorf("%s{kind=%s} = %v, want %v", tc.name, tc.kind, v, tc.want)
		}
	}
	cache.Clear()
	got = collected(t, reader)
	if ps := got[metricCacheClears]; len(ps) != 1 || ps[0].value != 1 || ps[0].attrs.Len() != 0 {
		t.Errorf("%s = %+v, want one point of 1 with no attributes", metricCacheClears, ps)
	}
	if v := valueAt(got[metricCacheEntries], attrKind, kindCapability); v != 0 {
		t.Errorf("%s after Clear = %v, want 0", metricCacheEntries, v)
	}
	// memstore names each answer's ancestors, so ClearRevoked is narrow.
	cache.ClearRevoked(id)
	cache.ClearRevoked(id)
	got = collected(t, reader)
	if ps := got[metricClearsRevoked]; len(ps) != 1 || ps[0].value != 2 || ps[0].attrs.Len() != 0 {
		t.Errorf("%s = %+v, want one point of 2 with no attributes", metricClearsRevoked, ps)
	}
	if ps := got[metricCacheClears]; len(ps) != 1 || ps[0].value != 1 {
		t.Errorf("%s after ClearRevoked = %+v, want still 1", metricCacheClears, ps)
	}
}

// The revocation signal's state and the causes of a whole clear reach the
// provider, each a single point; the time of the last loss only once there
// was one.
func TestInstrumentRevocationCacheReportsTheSignal(t *testing.T) {
	cache, reader, _ := instrumentedCache(t)
	if got := collected(t, reader); len(got[metricLastSyncLoss]) != 0 {
		t.Errorf("%s before any loss = %+v, want no point", metricLastSyncLoss, got[metricLastSyncLoss])
	}
	cache.Listening()
	cache.Signal(uuid.New())
	cache.Signal()
	before := time.Now().Unix()
	cache.NotListening()
	got := collected(t, reader)
	for name, want := range map[string]float64{
		metricLineage:         1,
		metricSynced:          0,
		metricSyncs:           1,
		metricSyncsLost:       1,
		metricSignals:         2,
		metricSignalsWhole:    1,
		metricClearsNoLineage: 0,
	} {
		if ps := got[name]; len(ps) != 1 || ps[0].value != want || ps[0].attrs.Len() != 0 {
			t.Errorf("%s = %+v, want one point of %v with no attributes", name, ps, want)
		}
	}
	if ps := got[metricLastSyncLoss]; len(ps) != 1 || ps[0].value < float64(before) {
		t.Errorf("%s = %+v, want one point at or after %d", metricLastSyncLoss, ps, before)
	}
	cache.Listening()
	if ps := collected(t, reader)[metricSynced]; len(ps) != 1 || ps[0].value != 1 {
		t.Errorf("%s while listening = %+v, want 1", metricSynced, ps)
	}
}

// Unregistered, the cache reports nothing more.
func TestInstrumentRevocationCacheUnregisters(t *testing.T) {
	cache, reader, reg := instrumentedCache(t)
	_, _ = cache.IsRevoked(context.Background(), uuid.New())
	if err := reg.Unregister(); err != nil {
		t.Fatal(err)
	}
	if got := collected(t, reader); len(got[metricCacheMisses]) != 0 {
		t.Errorf("after Unregister: %+v", got[metricCacheMisses])
	}
}

// No cache is refused rather than a callback that panics at collection.
func TestInstrumentRevocationCacheRefusesNoCache(t *testing.T) {
	if _, err := otelmetric.InstrumentRevocationCache(nil); err == nil {
		t.Error("a nil cache was accepted")
	}
}

// The flag gauges are dimensionless, unit "1", as OpenTelemetry spells it.
func TestInstrumentRevocationCacheUnits(t *testing.T) {
	_, reader, _ := instrumentedCache(t)
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	units := map[string]string{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			units[m.Name] = m.Unit
		}
	}
	for name, want := range map[string]string{metricLineage: "1", metricSynced: "1"} {
		if got := units[name]; got != want {
			t.Errorf("%s unit = %q, want %q", name, got, want)
		}
	}
}
