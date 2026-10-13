package pgstore

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/storetest"
)

func TestStoreContract(t *testing.T) {
	storetest.Run(t, func(t *testing.T) storetest.Env {
		t.Helper()
		fresh(t)
		clock := newFakeClock()
		store, err := NewStore(Config{DB: app, Now: clock.Now})
		if err != nil {
			t.Fatal(err)
		}
		return storetest.Env{
			Ctx:     t.Context(),
			Store:   store,
			Tenant:  uuid.New(),
			Now:     clock.Now,
			Advance: clock.Advance,
		}
	})
}

func TestNewStoreRequiresADB(t *testing.T) {
	if _, err := NewStore(Config{}); !errors.Is(err, errNoDB) {
		t.Fatalf("NewStore without a DB: err = %v, want errNoDB", err)
	}
}

// Every call runs Scope first, in the transaction the call opened; an error
// from it ends the call.
func TestScopeRunsInEveryCall(t *testing.T) {
	fresh(t)
	var calls atomic.Int64
	store, err := NewStore(Config{DB: app, Scope: scopeNothing(&calls)})
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	c := limes.Capability{
		ID: uuid.New(), ExpiresAt: time.Now().Add(time.Hour),
		Subject: limes.Principal{Type: limes.PrincipalAgent, TenantID: uuid.New(), Subject: "agent:scoped"},
	}
	if err := store.Insert(ctx, limes.InsertRequest{Capability: c}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.IsRevoked(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("Scope ran %d times over two calls, want 2", got)
	}

	refused := errors.New("no tenant in the context")
	store.scope = func(context.Context, pgx.Tx) error { return refused }
	if _, err := store.GetRecord(ctx, c.ID); !errors.Is(err, refused) {
		t.Errorf("GetRecord with a failing Scope: err = %v, want its error", err)
	}
}
