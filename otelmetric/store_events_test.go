package otelmetric

import (
	"context"
	"testing"
	"time"
	"uuid"

	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/oleg-tkachuk/limes/meter"
)

// The waits recorded: a short one and a long one, of one call.
const (
	shortWait = 2 * time.Millisecond
	longWait  = 300 * time.Millisecond
)

// tenantWaitPoints is the histogram's data points in one collection.
func tenantWaitPoints(t *testing.T, reader *sdkmetric.ManualReader) []metricdata.HistogramDataPoint[float64] {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != metricTenantWait {
				continue
			}
			if m.Unit != unitSeconds {
				t.Errorf("%s unit = %q, want %q", metricTenantWait, m.Unit, unitSeconds)
			}
			return m.Data.(metricdata.Histogram[float64]).DataPoints
		}
	}
	t.Fatalf("no %s in the collection", metricTenantWait)
	return nil
}

// Each wait is recorded in seconds, by operation; the tenant is an
// attribute only under WithTenantAttribute.
func TestStoreEventsRecordTenantWaits(t *testing.T) {
	tenant := uuid.New()
	for _, tc := range []struct {
		name       string
		opts       []Option
		wantTenant bool
	}{
		{name: "default"},
		{name: "with the tenant", opts: []Option{WithTenantAttribute()}, wantTenant: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader := sdkmetric.NewManualReader()
			events, err := NewStoreEvents(append(tc.opts, WithMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))))...)
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			events.OnTenantWait(ctx, meter.TenantWaitEvent{TenantID: tenant, Operation: meter.StoreOperationCharge, Wait: shortWait})
			events.OnTenantWait(ctx, meter.TenantWaitEvent{TenantID: tenant, Operation: meter.StoreOperationCharge, Wait: longWait})
			events.OnTenantWait(ctx, meter.TenantWaitEvent{TenantID: tenant, Operation: meter.StoreOperationRefund, Wait: shortWait})

			points := tenantWaitPoints(t, reader)
			if len(points) != 2 {
				t.Fatalf("%d series, want one per operation: %+v", len(points), points)
			}
			for _, p := range points {
				op, _ := p.Attributes.Value(attribute.Key(attrOperation))
				_, hasTenant := p.Attributes.Value(attribute.Key(attrTenantID))
				if hasTenant != tc.wantTenant {
					t.Errorf("%s: tenant attribute present = %v, want %v", op.AsString(), hasTenant, tc.wantTenant)
				}
				switch op.AsString() {
				case string(meter.StoreOperationCharge):
					if p.Count != 2 || p.Sum != (shortWait+longWait).Seconds() {
						t.Errorf("Charge: count %d, sum %v; want 2, %v", p.Count, p.Sum, (shortWait + longWait).Seconds())
					}
				case string(meter.StoreOperationRefund):
					if p.Count != 1 || p.Sum != shortWait.Seconds() {
						t.Errorf("Refund: count %d, sum %v; want 1, %v", p.Count, p.Sum, shortWait.Seconds())
					}
				default:
					t.Errorf("unexpected operation %q", op.AsString())
				}
			}
		})
	}
}
