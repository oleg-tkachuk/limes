package otelmetric

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/oleg-tkachuk/limes/meter"
)

// metricTenantWait is the histogram of a metered call's wait for its
// tenant's aggregate (meter.StoreEvents.OnTenantWait).
const metricTenantWait = "limes.tenant.wait"

// attrOperation is the store call a measurement is about, one of the
// meter.StoreOperation values: bounded.
const attrOperation = "limes.operation"

// tenantWaitBuckets are the histogram's boundaries, in seconds: a wait
// under a millisecond is one call ahead on a local database, and one of
// seconds a queue of calls on one tenant. The SDK's default boundaries,
// up to 10000, suit milliseconds, not seconds.
var tenantWaitBuckets = []float64{0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

// NewStoreEvents returns the callbacks a usage store reports to as it works
// (pgstore.Config.Events), recorded as instruments:
// limes.tenant.wait, a histogram of how long each metered call waited for
// its tenant's aggregate, in seconds, by limes.operation, and by
// limes.tenant_id under WithTenantAttribute. The wait is that of the
// aggregate's row alone, not of the tenant's advisory lock nor of the
// call's chain (docs/adr/0022-unlimited-tenant-aggregate.md, Decision 6).
func NewStoreEvents(opts ...Option) (meter.StoreEvents, error) {
	var cfg meteringConfig
	for _, o := range opts {
		o(&cfg)
	}
	in := &meteringInstruments{tenant: cfg.tenant}
	wait, err := providerOf(cfg).Meter(instrumentationScope).Float64Histogram(metricTenantWait,
		metric.WithUnit(unitSeconds),
		metric.WithDescription("How long a metered call waited for its tenant's aggregate row: not the tenant's advisory lock, nor the counters of the call's chain."),
		metric.WithExplicitBucketBoundaries(tenantWaitBuckets...))
	if err != nil {
		return meter.StoreEvents{}, fmt.Errorf("otelmetric: instruments: %w", err)
	}
	return meter.StoreEvents{
		OnTenantWait: func(ctx context.Context, e meter.TenantWaitEvent) {
			wait.Record(ctx, e.Wait.Seconds(), in.attrs(e.TenantID, attribute.String(attrOperation, string(e.Operation))))
		},
	}, nil
}
