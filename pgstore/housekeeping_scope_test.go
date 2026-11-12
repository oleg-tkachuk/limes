package pgstore

import (
	"context"
	"errors"
	"testing"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/meter"
)

// Every housekeeping call under a scope that admits no tenant is refused,
// not run over no rows, as PurgeCharges is: a worker that forgot its scope
// would otherwise report nothing to do on every call. One scoped to a
// tenant, or to every tenant, runs.
func TestHousekeepingRefusesAScopeOfNoTenant(t *testing.T) {
	f := newTenantFixture(t)
	calls := map[string]func(context.Context) error{
		"Store.Purge": func(ctx context.Context) error {
			_, err := f.records.Purge(ctx, limes.PurgeRequest{})
			return err
		},
		"UsageStore.ReleaseExpired": func(ctx context.Context) error {
			_, err := f.usage.ReleaseExpired(ctx, meter.ReleaseExpiredRequest{})
			return err
		},
		"UsageStore.PurgeOrphans": func(ctx context.Context) error {
			_, err := f.usage.PurgeOrphans(ctx, meter.PurgeOrphansRequest{})
			return err
		},
	}
	for name, call := range calls {
		if err := call(context.Background()); !errors.Is(err, limes.ErrScopeMismatch) {
			t.Errorf("%s scoped to no tenant: err = %v, want ErrScopeMismatch", name, err)
		}
		for scope, ctx := range map[string]context.Context{
			"a tenant":     WithTenant(context.Background(), f.mine),
			"every tenant": WithAllTenants(context.Background()),
		} {
			if err := call(ctx); err != nil {
				t.Errorf("%s scoped to %s: %v", name, scope, err)
			}
		}
	}
}
