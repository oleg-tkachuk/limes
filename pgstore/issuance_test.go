package pgstore

import (
	"context"
	"errors"
	"testing"
	"uuid"

	"github.com/oleg-tkachuk/limes"
)

// A capability issued where no tenant is in scope is refused by the store
// itself, and recorded in its tenant through ScopedIssuance, whatever
// tenant the caller's context names.
func TestScopedIssuanceRecordsInTheCapabilitysTenant(t *testing.T) {
	fresh(t)
	clock := newFakeClock()
	store, err := NewStore(Config{DB: app, Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	tenant := uuid.New()
	capability := func() limes.Capability {
		return limes.Capability{
			ID: uuid.New(), ExpiresAt: clock.Now().Add(rlsTTL),
			Subject: limes.Principal{Type: limes.PrincipalTypeUser, TenantID: tenant, Subject: "user"},
			Caveats: limes.Caveats{Ops: []limes.Op{limes.OpGet}, Requests: limes.RequestCeiling{Unlimited: true}},
		}
	}
	if err := store.Insert(t.Context(), limes.InsertRequest{Capability: capability()}); !errors.Is(err, limes.ErrScopeMismatch) {
		t.Fatalf("Insert with no tenant in scope = %v, want ErrScopeMismatch", err)
	}
	scoped := ScopedIssuance(store)
	for name, scope := range map[string]func(context.Context) context.Context{
		"no tenant in scope":      func(ctx context.Context) context.Context { return ctx },
		"another tenant in scope": func(ctx context.Context) context.Context { return WithTenant(ctx, uuid.New()) },
	} {
		c := capability()
		if err := scoped.Insert(scope(t.Context()), limes.InsertRequest{Capability: c}); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if _, err := store.GetRecord(WithTenant(t.Context(), tenant), c.ID); err != nil {
			t.Errorf("%s: the record is not in its tenant: %v", name, err)
		}
		if revoked, err := scoped.IsRevoked(t.Context(), c.ID); err != nil || revoked {
			t.Errorf("%s: IsRevoked = %v, %v", name, revoked, err)
		}
	}
}
