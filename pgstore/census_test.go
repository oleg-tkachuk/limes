package pgstore

import (
	"context"
	"errors"
	"testing"

	"github.com/oleg-tkachuk/limes"
)

// A census across every tenant runs only under a scope that admits every
// tenant: under one tenant's, or none, the policies would count that
// tenant's records, or none, as everyone's. The owner is not bound.
func TestCensusAcrossTenantsNeedsEveryTenant(t *testing.T) {
	f := newTenantFixture(t)
	every := limes.CountRecordsRequest{AllTenants: true}
	ownerCensus, err := NewCensus(Config{DB: owner, Now: f.clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	if page, err := ownerCensus.CountRecords(t.Context(), every); err != nil || len(page.Items) != 1 || page.Items[0].Total != 2 {
		t.Errorf("the owner's census of every tenant = %+v, %v; want both records", page.Items, err)
	}
	for name, tc := range map[string]struct {
		cfg  Config
		ctx  context.Context
		want error
	}{
		"WithAllTenants":                 {Config{}, WithAllTenants(t.Context()), nil},
		"WithAllTenantsReadOnly":         {Config{}, WithAllTenantsReadOnly(t.Context()), nil},
		"WithTenant":                     {Config{}, WithTenant(t.Context(), f.mine), limes.ErrScopeMismatch},
		"no scope":                       {Config{}, t.Context(), limes.ErrScopeMismatch},
		"the consumer's read-only Scope": {Config{Scope: consumerScope}, consumerReadOnly(t.Context()), nil},
		"the consumer's tenant Scope":    {Config{Scope: consumerScope}, WithTenant(t.Context(), f.mine), limes.ErrScopeMismatch},
	} {
		tc.cfg.DB, tc.cfg.Now = app, f.clock.Now
		census, err := NewCensus(tc.cfg)
		if err != nil {
			t.Fatal(err)
		}
		page, err := census.CountRecords(tc.ctx, every)
		if !errors.Is(err, tc.want) || (tc.want == nil && (len(page.Items) != 1 || page.Items[0].Total != 2)) {
			t.Errorf("%s: census of every tenant = %+v, %v; want %v", name, page.Items, err, tc.want)
		}
	}
	if _, err := NewCensus(Config{}); !errors.Is(err, errNoDB) {
		t.Errorf("NewCensus without a DB: err = %v, want errNoDB", err)
	}
}
