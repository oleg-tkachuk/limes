package memstore

import (
	"cmp"
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/metertest"
)

// contractTTL is how long the capabilities the contract checks record live.
const contractTTL = time.Hour

// contractExpiry is when every test capability expires: one instant, so a
// child never outlives its parent by the time between their inserts.
var contractExpiry = time.Now().Add(contractTTL)

func TestMeterContract(t *testing.T) {
	metertest.Run(t, func(t *testing.T) metertest.Env[struct{}] {
		t.Helper()
		records := newTestStore()
		tenant := uuid.New()
		// The clock stands still but for Advance, so holds made together
		// expire together.
		start := time.Now()
		var offset atomic.Int64 // how far Advance has moved the clock
		now := func() time.Time { return start.Add(time.Duration(offset.Load())) }
		return metertest.Env[struct{}]{
			Ctx:              context.Background(),
			Usage:            mustUsage(t, UsageStoreConfig[struct{}]{Records: records, Now: now}),
			Tenant:           tenant,
			Advance:          func(d time.Duration) { offset.Add(int64(d)) },
			DeleteCapability: removeRecord(records),
			NewCapability: func(spec metertest.CapabilitySpec) (uuid.UUID, error) {
				c := limes.Capability{
					ID: uuid.New(), ParentID: spec.Parent, ExpiresAt: contractExpiry,
					Subject: limes.Principal{Type: limes.PrincipalAgent, TenantID: cmp.Or(spec.Tenant, tenant), Subject: "agent"},
					Caveats: limes.Caveats{Ops: []limes.Op{limes.OpGet}, Requests: spec.Requests, Budget: spec.Budget},
				}
				return c.ID, records.Insert(context.Background(), limes.InsertRequest{Capability: c, IssuedBy: limes.Principal{Subject: "issuer"}})
			},
		}
	})
}

// listScope marks the context ListCtx hands out, so the test can tell it was
// the one ListTenantBudgets ran under.
type listScope struct{}

// tenantScope marks the context TenantCtx hands out with the tenant it was
// asked for, so the test can tell a write ran under that tenant's context.
type tenantScope struct{}

// A store that scopes each tenant's rows gets its tenants, their contexts and
// the context of a read across tenants from the Env, and the checks use them.
func TestMeterContractUsesTheTenantHooks(t *testing.T) {
	base := t.Context()
	listCtx := context.WithValue(base, listScope{}, true)
	listed := &listRecorder{}
	metertest.Run(t, func(t *testing.T) metertest.Env[struct{}] {
		t.Helper()
		ctx := base
		records := newTestStore()
		tenant := uuid.New()
		return metertest.Env[struct{}]{
			Ctx:    ctx,
			Usage:  listed.wrap(newTestUsage(records)),
			Tenant: tenant,
			NewTenant: func() (uuid.UUID, error) {
				id := uuid.New()
				listed.issued.Store(id, true)
				return id, nil
			},
			TenantCtx: func(tenant uuid.UUID) context.Context {
				return context.WithValue(ctx, tenantScope{}, tenant)
			},
			ListCtx: listCtx,
			NewCapability: func(spec metertest.CapabilitySpec) (uuid.UUID, error) {
				c := limes.Capability{
					ID: uuid.New(), ParentID: spec.Parent, ExpiresAt: contractExpiry,
					Subject: limes.Principal{Type: limes.PrincipalAgent, TenantID: cmp.Or(spec.Tenant, tenant), Subject: "agent"},
					Caveats: limes.Caveats{Ops: []limes.Op{limes.OpGet}, Requests: spec.Requests, Budget: spec.Budget},
				}
				return c.ID, records.Insert(ctx, limes.InsertRequest{Capability: c, IssuedBy: limes.Principal{Subject: "issuer"}})
			},
		}
	})
	if listed.scopedWrites.Load() == 0 || listed.strayWrites.Load() != 0 {
		t.Errorf("SetTenantBudget for a tenant from NewTenant ran %d times under its TenantCtx and %d otherwise; want every such write under it",
			listed.scopedWrites.Load(), listed.strayWrites.Load())
	}
	if listed.outside.Load() != 0 || listed.inside.Load() == 0 {
		t.Errorf("ListTenantBudgets ran %d times under ListCtx and %d outside it; want every list under it",
			listed.inside.Load(), listed.outside.Load())
	}
}

// listRecorder counts the ListTenantBudgets calls made under ListCtx and
// outside it, and the SetTenantBudget calls for a tenant NewTenant issued
// made under that tenant's TenantCtx and otherwise.
type listRecorder struct {
	inside, outside atomic.Int64
	// issued holds the tenants NewTenant handed out.
	issued                    sync.Map
	scopedWrites, strayWrites atomic.Int64
}

func (r *listRecorder) wrap(u *UsageStore[struct{}]) limes.UsageStore[struct{}] {
	return &recordingUsage{UsageStore: u, r: r}
}

type recordingUsage struct {
	*UsageStore[struct{}]
	r *listRecorder
}

func (u *recordingUsage) ListTenantBudgets(ctx context.Context, req limes.ListTenantBudgetsRequest) (limes.Page[limes.TenantBudget], error) {
	if ctx.Value(listScope{}) != nil {
		u.r.inside.Add(1)
	} else {
		u.r.outside.Add(1)
	}
	return u.UsageStore.ListTenantBudgets(ctx, req)
}

// SetTenantBudget for a tenant NewTenant issued must run under that tenant's
// TenantCtx; one for the Env's own Tenant runs under Ctx.
func (u *recordingUsage) SetTenantBudget(ctx context.Context, req limes.SetTenantBudgetRequest) (limes.TenantBudget, error) {
	if _, ok := u.r.issued.Load(req.TenantID); ok {
		if scope, _ := ctx.Value(tenantScope{}).(uuid.UUID); scope == req.TenantID {
			u.r.scopedWrites.Add(1)
		} else {
			u.r.strayWrites.Add(1)
		}
	}
	return u.UsageStore.SetTenantBudget(ctx, req)
}

// removeRecord removes a capability's record, as Purge does once it has
// expired, for metertest's check of PurgeOrphans.
func removeRecord(records *Store) func(uuid.UUID) error {
	return func(id uuid.UUID) error {
		records.mu.Lock()
		defer records.mu.Unlock()
		delete(records.caps, id)
		delete(records.issuedBy, id)
		return nil
	}
}
