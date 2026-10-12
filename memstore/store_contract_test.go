package memstore

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/storetest"
)

func TestStoreContract(t *testing.T) {
	storetest.Run(t, func(t *testing.T) storetest.Env {
		t.Helper()
		// The store's clock stands still but for Advance, as storetest.Env.Now
		// asks.
		start := time.Now().UTC()
		var offset time.Duration
		clock := func() time.Time { return start.Add(offset) }
		store, err := NewStore(StoreConfig{Now: clock})
		if err != nil {
			t.Fatal(err)
		}
		return storetest.Env{
			Ctx: context.Background(), Store: store, Tenant: uuid.New(),
			Now: clock, Advance: func(d time.Duration) { offset += d },
		}
	})
}

// TestStoreContractKnowingTenants runs the suite against a store that knows
// its tenants, as one with a tenants table does, so the checks that need
// Env.DeleteTenant run against something.
func TestStoreContractKnowingTenants(t *testing.T) {
	storetest.Run(t, func(t *testing.T) storetest.Env {
		t.Helper()
		s := &tenantStore{Store: newTestStore(), tenants: map[uuid.UUID]tenantState{}}
		tenant := s.create()
		return storetest.Env{
			Ctx: context.Background(), Store: s, Tenant: tenant,
			NewTenant:    func() (uuid.UUID, error) { return s.create(), nil },
			DeleteTenant: s.trash,
		}
	})
}

// tenantState is where a tenant of a tenantStore stands.
type tenantState int

const (
	tenantUnknown tenantState = iota
	tenantLive
	tenantTrashed
)

// tenantStore is a Store that mints only for a live tenant it created.
type tenantStore struct {
	*Store
	mu      sync.Mutex
	tenants map[uuid.UUID]tenantState
}

func (s *tenantStore) create() uuid.UUID {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := uuid.New()
	s.tenants[id] = tenantLive
	return id
}

func (s *tenantStore) trash(id uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tenants[id] != tenantLive {
		return limes.ErrUnknownTenant
	}
	s.tenants[id] = tenantTrashed
	return nil
}

func (s *tenantStore) Insert(ctx context.Context, req limes.InsertRequest) error {
	s.mu.Lock()
	state := s.tenants[req.Capability.Subject.TenantID]
	s.mu.Unlock()
	switch state {
	case tenantTrashed:
		return limes.ErrTenantDeleted
	case tenantUnknown:
		return limes.ErrUnknownTenant
	case tenantLive:
	}
	return s.Store.Insert(ctx, req)
}
