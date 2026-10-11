package memstore

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/metertest"
)

// contractTTL is how long the capabilities the contract checks record live.
const contractTTL = time.Hour

func TestMeterContract(t *testing.T) {
	metertest.Run(t, func(t *testing.T) metertest.Env[struct{}] {
		t.Helper()
		records := New[struct{}]()
		tenant := uuid.New()
		var offset atomic.Int64 // how far Advance has moved the clock
		now := func() time.Time { return time.Now().Add(time.Duration(offset.Load())) }
		return metertest.Env[struct{}]{
			Ctx:     context.Background(),
			Usage:   NewUsage(records).WithClock(now),
			Tenant:  tenant,
			Advance: func(d time.Duration) { offset.Add(int64(d)) },
			NewCapability: func(parent uuid.UUID, maxBudget limes.Nanos) (uuid.UUID, error) {
				c := limes.Capability{
					ID: uuid.New(), ParentID: parent, ExpiresAt: time.Now().Add(contractTTL),
					Subject: limes.Principal{TenantID: tenant, Subject: "agent"},
					Caveats: limes.Caveats{Ops: []limes.Op{limes.OpGet}, MaxBudgetAmount: maxBudget},
				}
				return c.ID, records.Insert(context.Background(), c, limes.Principal{Subject: "issuer"})
			},
		}
	})
}

// listScope marks the context ListCtx hands out, so the test can tell it was
// the one ListTenantBudgets ran under.
type listScope struct{}

// A store that scopes each tenant's rows gets its tenants, their contexts and
// the context of a read across tenants from the Env, and the checks use them.
func TestMeterContractUsesTheTenantHooks(t *testing.T) {
	var tenants, scoped atomic.Int64
	base := t.Context()
	listCtx := context.WithValue(base, listScope{}, true)
	listed := &listRecorder{}
	metertest.Run(t, func(t *testing.T) metertest.Env[struct{}] {
		t.Helper()
		ctx := base
		records := New[struct{}]()
		tenant := uuid.New()
		return metertest.Env[struct{}]{
			Ctx:    ctx,
			Usage:  listed.wrap(NewUsage(records)),
			Tenant: tenant,
			NewTenant: func() (uuid.UUID, error) {
				tenants.Add(1)
				return uuid.New(), nil
			},
			TenantCtx: func(uuid.UUID) context.Context {
				scoped.Add(1)
				return ctx
			},
			ListCtx: listCtx,
			NewCapability: func(parent uuid.UUID, maxBudget limes.Nanos) (uuid.UUID, error) {
				c := limes.Capability{
					ID: uuid.New(), ParentID: parent, ExpiresAt: time.Now().Add(contractTTL),
					Subject: limes.Principal{TenantID: tenant, Subject: "agent"},
					Caveats: limes.Caveats{Ops: []limes.Op{limes.OpGet}, MaxBudgetAmount: maxBudget},
				}
				return c.ID, records.Insert(ctx, c, limes.Principal{Subject: "issuer"})
			},
		}
	})
	if tenants.Load() == 0 || scoped.Load() == 0 {
		t.Errorf("NewTenant ran %d times and TenantCtx %d; want both used", tenants.Load(), scoped.Load())
	}
	if listed.outside.Load() != 0 || listed.inside.Load() == 0 {
		t.Errorf("ListTenantBudgets ran %d times under ListCtx and %d outside it; want every list under it",
			listed.inside.Load(), listed.outside.Load())
	}
}

// listRecorder counts the ListTenantBudgets calls made under ListCtx and
// outside it.
type listRecorder struct {
	inside, outside atomic.Int64
}

func (r *listRecorder) wrap(u *UsageStore[struct{}]) limes.UsageStore[struct{}] {
	return &recordingUsage{UsageStore: u, r: r}
}

type recordingUsage struct {
	*UsageStore[struct{}]
	r *listRecorder
}

func (u *recordingUsage) ListTenantBudgets(ctx context.Context, req limes.ListTenantBudgetsRequest) ([]limes.TenantBudgetSummary, string, error) {
	if ctx.Value(listScope{}) != nil {
		u.r.inside.Add(1)
	} else {
		u.r.outside.Add(1)
	}
	return u.UsageStore.ListTenantBudgets(ctx, req)
}
