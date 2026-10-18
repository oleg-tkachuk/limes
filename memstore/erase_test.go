package memstore

import (
	"errors"
	"testing"

	"github.com/oleg-tkachuk/limes"
)

// An erased tenant stays erased while any of its records is left, so a
// capability on record cannot be metered; PurgeOrphans forgets the mark once
// the last record is gone, as nothing of the tenant can be metered then.
func TestPurgeOrphansForgetsAnErasedTenantWithNoRecordLeft(t *testing.T) {
	ctx, u, capID, tenant := newCopyFixture(t)
	if err := u.EraseTenantUsage(ctx, limes.EraseTenantUsageRequest{TenantID: tenant}); err != nil {
		t.Fatal(err)
	}
	setBudget := func() error {
		_, err := u.SetTenantBudget(ctx, limes.SetTenantBudgetRequest{TenantID: tenant, Ceiling: limes.BudgetCeiling{Unlimited: true, UnitCode: limes.AbstractUnitCode}})
		return err
	}
	if _, err := u.PurgeOrphans(ctx, limes.PurgeOrphansRequest{}); err != nil {
		t.Fatal(err)
	}
	if err := setBudget(); !errors.Is(err, limes.ErrTenantErased) {
		t.Fatalf("SetTenantBudget with a record of the tenant left: err = %v, want ErrTenantErased", err)
	}
	dropRecord(u.records, capID)
	if _, err := u.PurgeOrphans(ctx, limes.PurgeOrphansRequest{}); err != nil {
		t.Fatal(err)
	}
	if err := setBudget(); err != nil {
		t.Fatalf("SetTenantBudget once no record of the tenant is left: %v", err)
	}
}
