package metertest_test

import (
	"context"
	"testing"
	"time"
	"uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/memstore"
	"github.com/oleg-tkachuk/limes/metertest"
)

// exampleTTL is how long the capabilities the examples record live.
const exampleTTL = time.Hour

// A usage store's own test file runs the suite against a fresh store per
// check; memstore stands in here for the store under test. Run takes the
// test's *testing.T, so the example shows the test function rather than
// running it. The optional fields of Env each turn on more checks.
func ExampleRun() {
	_ = func(t *testing.T) {
		metertest.Run(t, func(t *testing.T) metertest.Env {
			t.Helper()
			// The clock stands still but for Advance, so holds made together
			// expire together.
			start := time.Now()
			var offset time.Duration
			clock := func() time.Time { return start.Add(offset) }
			records, err := memstore.NewStore(memstore.StoreConfig{Now: clock})
			if err != nil {
				t.Fatal(err)
			}
			usage, err := memstore.NewUsageStore(memstore.UsageStoreConfig{Records: records, Now: clock})
			if err != nil {
				t.Fatal(err)
			}
			tenant := uuid.New()
			return metertest.Env{
				Ctx: context.Background(), Usage: usage, Tenant: tenant,
				Ledger: memstore.NewLedger(usage), Now: clock,
				Advance: func(d time.Duration) { offset += d },
				NewCapability: func(spec metertest.CapabilitySpec) (uuid.UUID, error) {
					if spec.Tenant == uuid.Nil() {
						spec.Tenant = tenant
					}
					c := limes.Capability{
						ID: uuid.New(), ParentID: spec.Parent, ExpiresAt: start.Add(exampleTTL), ClientID: spec.ClientID,
						Subject: limes.Principal{Type: limes.PrincipalTypeAgent, TenantID: spec.Tenant, Subject: "agent"},
						Caveats: limes.Caveats{
							Ops: []limes.Op{limes.OpGet}, Requests: spec.Requests, Budget: spec.Budget, Copies: spec.CopyQuota(),
						},
					}
					return c.ID, records.Insert(context.Background(), limes.InsertRequest{
						Capability: c, SubjectIssuer: spec.SubjectIssuer,
					})
				},
			}
		})
	}
}

// A store that only counts requests (meter.RequestCounter) runs the
// counter's checks alone.
func ExampleRunCounter() {
	_ = func(t *testing.T) {
		metertest.RunCounter(t, func(t *testing.T) metertest.CounterEnv {
			t.Helper()
			records, err := memstore.NewStore(memstore.StoreConfig{})
			if err != nil {
				t.Fatal(err)
			}
			usage, err := memstore.NewUsageStore(memstore.UsageStoreConfig{Records: records})
			if err != nil {
				t.Fatal(err)
			}
			tenant := uuid.New()
			return metertest.CounterEnv{
				Ctx: context.Background(), Counter: usage, Tenant: tenant,
				NewCapability: func(spec metertest.CapabilitySpec) (uuid.UUID, error) {
					c := limes.Capability{
						ID: uuid.New(), ParentID: spec.Parent, ExpiresAt: time.Now().Add(exampleTTL),
						Subject: limes.Principal{Type: limes.PrincipalTypeAgent, TenantID: tenant, Subject: "agent"},
						Caveats: limes.Caveats{Ops: []limes.Op{limes.OpGet}, Requests: spec.Requests, Budget: spec.Budget},
					}
					return c.ID, records.Insert(context.Background(), limes.InsertRequest{Capability: c})
				},
			}
		})
	}
}
