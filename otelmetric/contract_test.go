package otelmetric_test

import (
	"cmp"
	"context"
	"sync/atomic"
	"testing"
	"time"
	"uuid"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/memstore"
	"github.com/oleg-tkachuk/limes/metertest"
	"github.com/oleg-tkachuk/limes/otelmetric"
)

// contractTTL is how long the capabilities the contract checks record live.
const contractTTL = time.Hour

// contractCopyQuota is the quota of copies per capability the contract's
// usage store is built with: above every check's own copies.
const contractCopyQuota = 16

// Instrument returns a meter.UsageStore, so what it returns keeps the
// contract the store it wraps keeps: every call reaches the store, every
// answer and error comes back unchanged. Checked over memstore.
//
// No DeleteCapability: memstore removes a record only once it has expired,
// so the checks of what follows a removal are left to memstore's own run.
func TestInstrumentedMeterContract(t *testing.T) {
	expiry := time.Now().Add(contractTTL)
	metertest.Run(t, func(t *testing.T) metertest.Env {
		t.Helper()
		records, err := memstore.NewStore(memstore.StoreConfig{})
		if err != nil {
			t.Fatal(err)
		}
		tenant := uuid.New()
		// The clock stands still but for Advance, so holds made together
		// expire together.
		start := time.Now()
		var offset atomic.Int64 // how far Advance has moved the clock
		now := func() time.Time { return start.Add(time.Duration(offset.Load())) }
		inner, err := memstore.NewUsageStore(memstore.UsageStoreConfig{Records: records, Now: now, MaxCopiesPerCapability: contractCopyQuota})
		if err != nil {
			t.Fatal(err)
		}
		usage, err := otelmetric.Instrument(inner, otelmetric.WithMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(sdkmetric.NewManualReader()))))
		if err != nil {
			t.Fatal(err)
		}
		return metertest.Env{
			MaxCopiesPerCapability: contractCopyQuota,
			Ctx:                    context.Background(),
			Usage:                  usage,
			Ledger:                 memstore.NewLedger(inner),
			Now:                    now,
			Tenant:                 tenant,
			Advance:                func(d time.Duration) { offset.Add(int64(d)) },
			NewCapability: func(spec metertest.CapabilitySpec) (uuid.UUID, error) {
				c := limes.Capability{
					ID: uuid.New(), ParentID: spec.Parent, ExpiresAt: expiry,
					Subject: limes.Principal{Type: limes.PrincipalTypeAgent, TenantID: cmp.Or(spec.Tenant, tenant), Subject: "agent"},
					Caveats: limes.Caveats{Ops: []limes.Op{limes.OpGet}, Requests: spec.Requests, Budget: spec.Budget, Copies: spec.CopyQuota()},
				}
				return c.ID, records.Insert(context.Background(), limes.InsertRequest{Capability: c, IssuedBy: limes.Principal{Subject: "issuer"}})
			},
		}
	})
}
