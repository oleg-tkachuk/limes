package otelmetric

import (
	"context"
	"errors"
	"fmt"
	"math"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/oleg-tkachuk/limes"
)

// OpenTelemetry instruments of a limes.RevocationCache
// (InstrumentRevocationCache), read from its Stats when the provider
// collects. Every one but the two of clears carries kind: capability for IsRevoked,
// biscuit for IsBiscuitRevoked.

// Instrument names, as an exporter shows them.
const (
	metricCacheHits      = "limes.revocation_cache.hits"
	metricCacheMisses    = "limes.revocation_cache.misses"
	metricCacheLookups   = "limes.revocation_cache.lookups"
	metricCacheEvictions = "limes.revocation_cache.evictions"
	metricCacheEntries   = "limes.revocation_cache.entries"
	metricCacheClears    = "limes.revocation_cache.clears"
	// metricCacheClearsRevoked counts the narrow clears, which drop only
	// the answers a revocation outdates.
	metricCacheClearsRevoked = "limes.revocation_cache.clears_revoked"
)

// attrKind is the attribute naming the kind of answer.
const attrKind = "kind"

// Kinds of answer.
const (
	kindCapability = "capability"
	kindBiscuit    = "biscuit"
)

// InstrumentRevocationCache reports cache's counts (limes.RevocationCacheStats)
// as observable instruments, read each time the provider collects, so the
// cache's checks pay nothing for them. Unregister the registration it returns
// to stop. Of the options, only WithMeterProvider applies: the cache's counts
// have no tenant.
func InstrumentRevocationCache(cache *limes.RevocationCache, opts ...Option) (metric.Registration, error) {
	if cache == nil {
		return nil, errors.New("otelmetric: InstrumentRevocationCache needs a cache")
	}
	var cfg meteringConfig
	for _, opt := range opts {
		opt(&cfg)
	}
	provider := cfg.provider
	if provider == nil {
		provider = otel.GetMeterProvider()
	}
	meter := provider.Meter(instrumentationScope)
	var (
		errs                                                    [7]error
		hits, misses, lookups, evictions, clears, clearsRevoked metric.Int64ObservableCounter
		entries                                                 metric.Int64ObservableGauge
	)
	hits, errs[0] = meter.Int64ObservableCounter(metricCacheHits,
		metric.WithDescription("Revocation questions answered from the cache, by kind."))
	misses, errs[1] = meter.Int64ObservableCounter(metricCacheMisses,
		metric.WithDescription("Revocation questions that waited on an upstream lookup, by kind."))
	lookups, errs[2] = meter.Int64ObservableCounter(metricCacheLookups,
		metric.WithDescription("Upstream lookups run for the misses, by kind."))
	evictions, errs[3] = meter.Int64ObservableCounter(metricCacheEvictions,
		metric.WithDescription("Live answers dropped to stay within the cache's bound, by kind."))
	entries, errs[4] = meter.Int64ObservableGauge(metricCacheEntries,
		metric.WithDescription("Answers the cache holds, by kind."))
	clears, errs[5] = meter.Int64ObservableCounter(metricCacheClears,
		metric.WithDescription("Calls to RevocationCache.Clear."))
	clearsRevoked, errs[6] = meter.Int64ObservableCounter(metricCacheClearsRevoked,
		metric.WithDescription("Calls to RevocationCache.ClearRevoked that dropped only what a revocation outdates."))
	if err := errors.Join(errs[:]...); err != nil {
		return nil, fmt.Errorf("otelmetric: cache instruments: %w", err)
	}
	kinds := [...]struct {
		attrs  metric.ObserveOption
		counts func(limes.RevocationCacheStats) limes.RevocationCacheCounts
	}{
		{metric.WithAttributes(attribute.String(attrKind, kindCapability)), func(s limes.RevocationCacheStats) limes.RevocationCacheCounts { return s.Capabilities }},
		{metric.WithAttributes(attribute.String(attrKind, kindBiscuit)), func(s limes.RevocationCacheStats) limes.RevocationCacheCounts { return s.Biscuits }},
	}
	reg, err := meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		st := cache.Stats()
		for _, k := range kinds {
			c := k.counts(st)
			o.ObserveInt64(hits, saturated(c.Hits), k.attrs)
			o.ObserveInt64(misses, saturated(c.Misses), k.attrs)
			o.ObserveInt64(lookups, saturated(c.Lookups), k.attrs)
			o.ObserveInt64(evictions, saturated(c.Evictions), k.attrs)
			o.ObserveInt64(entries, int64(c.Entries), k.attrs)
		}
		o.ObserveInt64(clears, saturated(st.Clears))
		o.ObserveInt64(clearsRevoked, saturated(st.ClearsRevoked))
		return nil
	}, hits, misses, lookups, evictions, entries, clears, clearsRevoked)
	if err != nil {
		return nil, fmt.Errorf("otelmetric: cache callback: %w", err)
	}
	return reg, nil
}

// saturated is n as an instrument takes it, held at the largest int64 a
// count could only pass after centuries of checks.
func saturated(n uint64) int64 {
	if n > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(n)
}
