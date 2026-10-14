package pgstore

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/storetest"
)

func TestStoreContract(t *testing.T) {
	storetest.Run(t, func(t *testing.T) storetest.Env {
		t.Helper()
		fresh(t)
		clock := newFakeClock()
		store, err := NewStore(Config{DB: app, Now: clock.Now, CheckTenant: checkTenant})
		if err != nil {
			t.Fatal(err)
		}
		tenant := newTenant(t)
		return storetest.Env{
			Ctx:       WithTenant(t.Context(), tenant),
			Store:     store,
			Tenant:    tenant,
			NewTenant: func() (uuid.UUID, error) { return newTenant(t), nil },
			DeleteTenant: func(tenant uuid.UUID) error {
				_, err := owner.Exec(t.Context(), sqlDeleteTenant, tenant)
				return err
			},
			TenantCtx: func(tenant uuid.UUID) context.Context { return WithTenant(t.Context(), tenant) },
			Now:       clock.Now,
			Advance:   clock.Advance,
		}
	})
}

// A Config without a DB, or with a nil pool or connection, builds no store:
// the error comes at construction, not on the first call.
func TestNewStoreRequiresADB(t *testing.T) {
	var pool *pgxpool.Pool
	var conn *pgx.Conn
	for _, tc := range []struct {
		name string
		db   DB
	}{
		{"none", nil},
		{"nil pool", pool},
		{"nil conn", conn},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewStore(Config{DB: tc.db}); !errors.Is(err, errNoDB) {
				t.Errorf("NewStore: err = %v, want errNoDB", err)
			}
			if _, err := NewUsageStore(Config{DB: tc.db}); !errors.Is(err, errNoDB) {
				t.Errorf("NewUsageStore: err = %v, want errNoDB", err)
			}
		})
	}
	if _, err := NewStore(Config{DB: app}); err != nil {
		t.Errorf("NewStore over a pool: %v", err)
	}
}

// Every call runs Scope first, in the transaction the call opened; an error
// from it ends the call.
func TestScopeRunsInEveryCall(t *testing.T) {
	fresh(t)
	var calls atomic.Int64
	store, err := NewStore(Config{DB: app, Scope: countedScope(&calls)})
	if err != nil {
		t.Fatal(err)
	}
	tenant := uuid.New()
	ctx := WithTenant(t.Context(), tenant)
	c := limes.Capability{
		ID: uuid.New(), ExpiresAt: time.Now().Add(time.Hour),
		Subject: limes.Principal{Type: limes.PrincipalAgent, TenantID: tenant, Subject: "agent:scoped"},
	}
	if err := store.Insert(ctx, limes.InsertRequest{Capability: c}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetRecord(ctx, c.ID); err != nil {
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

// countingDB counts what a store asks of its database.
type countingDB struct {
	DB
	begins, statements atomic.Int64
}

func (d *countingDB) Begin(ctx context.Context) (pgx.Tx, error) {
	d.begins.Add(1)
	return d.DB.Begin(ctx)
}

func (d *countingDB) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	d.statements.Add(1)
	return d.DB.QueryRow(ctx, sql, args...)
}

// A revocation check is on the path of every request: one statement, one
// round trip, no transaction around it.
func TestRevocationChecksAreOneStatement(t *testing.T) {
	f := newTenantFixture(t)
	db := &countingDB{DB: app}
	store, err := NewStore(Config{DB: db})
	if err != nil {
		t.Fatal(err)
	}
	for name, check := range map[string]func() (bool, error){
		"IsRevoked":        func() (bool, error) { return store.IsRevoked(t.Context(), f.child.ID) },
		"IsBiscuitRevoked": func() (bool, error) { return store.IsBiscuitRevoked(t.Context(), [][]byte{[]byte("copy")}) },
	} {
		db.begins.Store(0)
		db.statements.Store(0)
		if _, err := check(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if b, s := db.begins.Load(), db.statements.Load(); b != 0 || s != 1 {
			t.Errorf("%s: %d transactions and %d statements, want none and one", name, b, s)
		}
	}
}
