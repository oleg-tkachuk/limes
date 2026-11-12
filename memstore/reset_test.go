package memstore

import (
	"context"
	"errors"
	"testing"
	"uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/meter"
)

// A reset that moves the tenant to another unit is checked against the
// tenant as its leases' settle leaves it, and settles nothing when refused:
// a hold outside the lease keeps the old unit counted, a lease alone does
// not.
func TestResetToAnotherUnitSettlesLeasesOnlyWhenItSucceeds(t *testing.T) {
	const (
		leased   = 4 * unit
		reserved = 2 * unit
	)
	for _, tc := range []struct {
		name string
		// hold is whether the tenant holds a reservation outside the lease.
		hold    bool
		wantErr error
	}{
		{name: "a hold outside the lease refuses it", hold: true, wantErr: limes.ErrUnitCodeMismatch},
		{name: "a lease alone does not", hold: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			u := newTestUsage(newTestStore())
			tenant := seedBudget(t, u, limes.BudgetCeiling{Unlimited: true, UnitCode: defaultTestUnit}, 0)
			leaseholder := recordedSubject(ctx, t, u, subjectSpec{capID: uuid.New(), tenant: tenant})
			if _, err := u.Lease(ctx, meter.LeaseRequest{Subject: leaseholder, Amount: leased}); err != nil {
				t.Fatal(err)
			}
			if tc.hold {
				other := recordedSubject(ctx, t, u, subjectSpec{capID: uuid.New(), tenant: tenant})
				if _, err := u.Reserve(ctx, meter.ReserveRequest{Subject: other, Amount: reserved}); err != nil {
					t.Fatal(err)
				}
			}
			before, err := u.GetTenantBudget(ctx, tenant)
			if err != nil {
				t.Fatal(err)
			}
			_, err = u.SetTenantBudget(ctx, meter.SetTenantBudgetRequest{
				TenantID: tenant, Ceiling: limes.BudgetCeiling{Unlimited: true, UnitCode: otherTestUnit},
				ResetSpend: true, ExpectedVersion: before.ResourceVersion,
			})
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("reset to %s: err = %v, want %v", otherTestUnit, err, tc.wantErr)
			}
			_, leaseErr := u.GetLease(ctx, leaseholder.CapabilityID())
			after, err := u.GetTenantBudget(ctx, tenant)
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantErr != nil {
				if leaseErr != nil {
					t.Errorf("GetLease after a refused reset: %v, want the lease still open", leaseErr)
				}
				if after.ResourceVersion != before.ResourceVersion || after.ReservedAmount != before.ReservedAmount || after.Ceiling.UnitCode != defaultTestUnit {
					t.Errorf("the tenant after a refused reset = version %d, %v held in %s; want version %d, %v held in %s",
						after.ResourceVersion, after.ReservedAmount, after.Ceiling.UnitCode, before.ResourceVersion, before.ReservedAmount, defaultTestUnit)
				}
				return
			}
			if !errors.Is(leaseErr, meter.ErrLeaseNotFound) {
				t.Errorf("GetLease after a reset: %v, want the lease settled", leaseErr)
			}
			if after.ReservedAmount != 0 || after.Ceiling.UnitCode != otherTestUnit {
				t.Errorf("the tenant after a reset = %v held in %s; want nothing held, in %s", after.ReservedAmount, after.Ceiling.UnitCode, otherTestUnit)
			}
		})
	}
}
