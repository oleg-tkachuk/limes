package pgstore

import (
	"context"
	"errors"
	"testing"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/meter"
)

// A purge under a scope that admits no tenant is refused, not run over no
// rows: a worker that forgot its scope would otherwise report nothing to
// purge on every call. One scoped to a tenant, or to every tenant, runs.
func TestPurgeChargesRefusesAScopeOfNoTenant(t *testing.T) {
	f := newTenantFixture(t)
	ledger, err := NewLedger(Config{DB: app, Now: f.clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.PurgeCharges(context.Background(), meter.PurgeChargesRequest{}); !errors.Is(err, limes.ErrScopeMismatch) {
		t.Errorf("PurgeCharges scoped to no tenant: err = %v, want ErrScopeMismatch", err)
	}
	for name, ctx := range map[string]context.Context{
		"a tenant":     WithTenant(context.Background(), f.mine),
		"every tenant": WithAllTenants(context.Background()),
	} {
		if _, err := ledger.PurgeCharges(ctx, meter.PurgeChargesRequest{}); err != nil {
			t.Errorf("PurgeCharges scoped to %s: %v", name, err)
		}
	}
}
