package otelmetric_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
	"uuid"

	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/memstore"
	"github.com/oleg-tkachuk/limes/meter"
	"github.com/oleg-tkachuk/limes/otelmetric"
)

// scopeCapability is limes.scope of a refusal by the capability's own
// ceiling, or an ancestor's.
const scopeCapability = "capability"

// wrapper wraps a memstore usage store in one of the module's decorators,
// as a meter.RequestCounter.
type wrapper func(*memstore.UsageStore, ...otelmetric.Option) (meter.RequestCounter, error)

// wrappers are the decorators that count a Bump.
var wrappers = map[string]wrapper{
	"Instrument": func(u *memstore.UsageStore, opts ...otelmetric.Option) (meter.RequestCounter, error) {
		return otelmetric.Instrument(u, opts...)
	},
	"InstrumentRequests": func(u *memstore.UsageStore, opts ...otelmetric.Option) (meter.RequestCounter, error) {
		return otelmetric.InstrumentRequests(u, opts...)
	},
}

// countedBy is a memstore wrapped by wrap, whose measurements reader
// collects.
func countedBy(t *testing.T, wrap wrapper, opts ...otelmetric.Option) (meter.RequestCounter, *sdkmetric.ManualReader, *memstore.Store) {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	opts = append(opts, otelmetric.WithMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))))
	records, err := memstore.NewStore(memstore.StoreConfig{Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	inner, err := memstore.NewUsageStore(memstore.UsageStoreConfig{Records: records})
	if err != nil {
		t.Fatal(err)
	}
	counter, err := wrap(inner, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return counter, reader, records
}

// bumpScript is every Bump outcome a decorator labels: allowed, a replay
// counted once, a failed onBumped, a capability's request limit and a copy
// past its quota, and the zero subject. tenant names the subjects' tenant.
func bumpScript(t *testing.T, counter meter.RequestCounter, records *memstore.Store, tenant uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	runs := 0
	keyed := meter.BumpRequest{Subject: recordedSubject(ctx, t, records, subjectSpec{capID: uuid.New(), tenant: tenant}), ExternalRef: "call-1"}
	for range 2 {
		if _, err := counter.Bump(ctx, keyed, func(context.Context, meter.Bumped) error { runs++; return nil }); err != nil {
			t.Fatal(err)
		}
	}
	if runs != 1 {
		t.Errorf("onBumped ran %d times, want once: the replay runs none", runs)
	}
	boom := errors.New("side effect failed")
	if _, err := counter.Bump(ctx, meter.BumpRequest{Subject: keyed.Subject}, func(context.Context, meter.Bumped) error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("a bump whose onBumped failed: %v", err)
	}
	one := meter.BumpRequest{Subject: recordedSubject(ctx, t, records, subjectSpec{capID: uuid.New(), tenant: tenant, requests: requestsOf(1)})}
	if _, err := counter.Bump(ctx, one, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := counter.Bump(ctx, one, nil); !errors.Is(err, limes.ErrRequestLimitExceeded) {
		t.Fatalf("a bump past the capability's request limit: %v", err)
	}
	capID := uuid.New()
	limitedCopies[capID] = true
	t.Cleanup(func() { delete(limitedCopies, capID) })
	copyOf := func(id string) limes.CopyCeiling {
		return limes.CopyCeiling{RevocationID: []byte(id), Requests: &limes.RequestCeiling{Unlimited: true}}
	}
	two := recordedSubject(ctx, t, records, subjectSpec{capID: capID, tenant: tenant, copies: []limes.CopyCeiling{copyOf("a"), copyOf("b")}})
	if _, err := counter.Bump(ctx, meter.BumpRequest{Subject: two}, nil); !errors.Is(err, limes.ErrRequestLimitExceeded) {
		t.Fatalf("a bump of two copies under a quota of one: %v", err)
	}
	if _, err := counter.Bump(ctx, meter.BumpRequest{Subject: limes.MeterSubject{}}, nil); err == nil {
		t.Fatal("a bump of the zero subject was not refused")
	}
}

// sumAt is the sum of the points whose attribute key is value.
func sumAt(ps []point, key, value string) float64 {
	var sum float64
	for _, p := range ps {
		if v, ok := p.attrs.Value(attribute.Key(key)); ok && v.AsString() == value {
			sum += p.value
		}
	}
	return sum
}

// pointKeys is every point of limes.request.bumps as a sorted list of its
// attributes and value, for two readers to be compared.
func pointKeys(t *testing.T, reader *sdkmetric.ManualReader) []string {
	t.Helper()
	var keys []string
	for _, p := range points(t, reader, metricRequestBumps) {
		keys = append(keys, fmt.Sprintf("%s=%v", p.attrs.Encoded(attribute.DefaultEncoder()), p.value))
	}
	slices.Sort(keys)
	return keys
}

// Both decorators count a Bump on limes.request.bumps, by limes.outcome and
// limes.scope: allowed, a replay once, a failed callback and the zero
// subject as error, and a limit refused in the scope of its ceiling.
func TestEveryWrapperLabelsBumpOutcomes(t *testing.T) {
	for name, wrap := range wrappers {
		t.Run(name, func(t *testing.T) {
			counter, reader, records := countedBy(t, wrap)
			bumpScript(t, counter, records, uuid.New())
			ps := points(t, reader, metricRequestBumps)
			for outcome, want := range map[string]float64{"allowed": 2, "error": 2, string(limes.ReasonRequestLimitExceeded): 2} {
				if got := sumAt(ps, attrOutcome, outcome); got != want {
					t.Errorf("%s bumps = %v, want %v", outcome, got, want)
				}
			}
			for scope, want := range map[string]float64{scopeCapability: 1, scopeCopy: 1} {
				if got := valueAt(ps, attrScope, scope); got != want {
					t.Errorf("bumps refused in scope %s = %v, want %v", scope, got, want)
				}
			}
			for _, p := range ps {
				if p.attrs.HasValue(attribute.Key(attrTenant)) {
					t.Errorf("a bump carries limes.tenant_id without WithTenantAttribute: %v", p.attrs)
				}
			}
		})
	}
}

// With and without WithTenantAttribute, the two decorators give the same
// points for the same bumps: one series, whichever a binary uses. Under it
// every point carries a tenant, the zero subject's the zero tenant.
func TestWrappersCountBumpsAlike(t *testing.T) {
	for _, opts := range [][]otelmetric.Option{nil, {otelmetric.WithTenantAttribute()}} {
		tenant := uuid.New()
		got := map[string][]string{}
		for name, wrap := range wrappers {
			counter, reader, records := countedBy(t, wrap, opts...)
			bumpScript(t, counter, records, tenant)
			got[name] = pointKeys(t, reader)
		}
		if !slices.Equal(got["Instrument"], got["InstrumentRequests"]) {
			t.Errorf("with %d options: Instrument counts %q, InstrumentRequests %q", len(opts), got["Instrument"], got["InstrumentRequests"])
		}
		if len(opts) == 0 {
			continue
		}
		for _, key := range got["InstrumentRequests"] {
			if !strings.Contains(key, attrTenant+"="+tenant.String()) && !strings.Contains(key, attrTenant+"="+uuid.Nil().String()) {
				t.Errorf("under WithTenantAttribute a point carries neither the subjects' nor the zero tenant: %s", key)
			}
		}
	}
}

// InstrumentRequests needs a counter, and hands back a counter alone: the
// store's other interfaces do not reach through it.
func TestInstrumentRequestsIsACounterAlone(t *testing.T) {
	if _, err := otelmetric.InstrumentRequests(nil); !errors.Is(err, limes.ErrInvalidConfig) {
		t.Fatalf("InstrumentRequests(nil) = %v, want limes.ErrInvalidConfig", err)
	}
	counter, _, _ := countedBy(t, wrappers["InstrumentRequests"])
	if _, ok := counter.(meter.UsageStore); ok {
		t.Error("InstrumentRequests' result is a meter.UsageStore")
	}
	if _, ok := counter.(meter.CopyUsageReader); ok {
		t.Error("InstrumentRequests' result reads copy counters")
	}
}
