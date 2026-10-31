package metertest

import (
	"errors"
	"slices"
	"testing"
	"uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/internal/expect"
	"github.com/oleg-tkachuk/limes/meter"
)

// tenantLister is f's TenantReservationLister, skipping the check without
// one.
func (f fixture) tenantLister(t *testing.T) meter.TenantReservationLister {
	t.Helper()
	l, ok := f.Usage.(meter.TenantReservationLister)
	if !ok {
		t.Skip("Env.Usage is no meter.TenantReservationLister")
	}
	return l
}

// tenantHolds walks every page of tenant's holds, limit at a time, and
// returns them in the order they came.
func (f fixture) tenantHolds(t *testing.T, l meter.TenantReservationLister, limit int) []meter.Reservation {
	t.Helper()
	var all []meter.Reservation
	cursor := ""
	for pages := 0; ; pages++ {
		expect.Must(t, pages <= len(all)+1, "ListTenantReservations never returned its last page")
		page, err := l.ListTenantReservations(f.Ctx, meter.ListTenantReservationsRequest{TenantID: f.Tenant, Limit: limit, Cursor: cursor})
		expect.Must(t, err == nil, "ListTenantReservations: %v", err)
		expect.That(t, limit == 0 || len(page.Items) <= limit, "a page of %d holds, past its limit %d", len(page.Items), limit)
		all = append(all, page.Items...)
		if page.Next == "" {
			return all
		}
		cursor = page.Next
	}
}

// A tenant's holds are listed — its capabilities' and their children's,
// not another tenant's — soonest to expire first, ties by id, a page at a
// time, and their amounts sum to the tenant's ReservedAmount.
func checkListTenantReservations(t *testing.T, f fixture) {
	l := f.tenantLister(t)
	root, rootHold := f.heldCapability(t, uuid.Nil(), "")
	_, childHold := f.heldCapability(t, root, "")
	_, otherHold := f.heldCapability(t, uuid.Nil(), "")
	other, err := f.newTenant()
	expect.Must(t, err == nil, "NewTenant: %v", err)
	_, theirsHold := f.heldIn(t, other, uuid.Nil(), "")
	want := []uuid.UUID{rootHold, childHold, otherHold}
	for _, limit := range []int{0, 1} {
		got := f.tenantHolds(t, l, limit)
		ids := make([]uuid.UUID, 0, len(got))
		var sum limes.Nanos
		for _, r := range got {
			ids = append(ids, r.ID)
			sum += r.Amount
			expect.That(t, r.TenantID == f.Tenant, "a hold of tenant %s listed for %s", r.TenantID, f.Tenant)
		}
		expect.That(t, !slices.Contains(ids, theirsHold), "another tenant's hold listed")
		expect.That(t, len(ids) == len(want) && !slices.ContainsFunc(want, func(id uuid.UUID) bool { return !slices.Contains(ids, id) }),
			"limit %d listed %v, want %v", limit, ids, want)
		expect.That(t, slices.IsSortedFunc(got, func(a, b meter.Reservation) int {
			if c := a.ExpiresAt.Compare(b.ExpiresAt); c != 0 {
				return c
			}
			return slices.Compare(a.ID[:], b.ID[:])
		}), "limit %d listed the holds out of order: %v", limit, ids)
		budget, err := f.Usage.GetTenantBudget(f.Ctx, f.Tenant)
		expect.That(t, err == nil && budget.ReservedAmount == sum, "the listed holds sum to %v; the tenant holds %v (%v)", sum, budget.ReservedAmount, err)
	}
}

func checkListTenantReservationsRefuses(t *testing.T, f fixture) {
	l := f.tenantLister(t)
	for name, req := range map[string]meter.ListTenantReservationsRequest{
		"no tenant":        {},
		"a negative limit": {TenantID: f.Tenant, Limit: -1},
	} {
		_, err := l.ListTenantReservations(f.Ctx, req)
		expect.That(t, errors.Is(err, limes.ErrInvalidRequest), "%s: %v, want ErrInvalidRequest", name, err)
	}
	if !f.ScopesTenants {
		return
	}
	other, err := f.newTenant()
	expect.Must(t, err == nil, "NewTenant: %v", err)
	_, err = l.ListTenantReservations(f.Ctx, meter.ListTenantReservationsRequest{TenantID: other})
	expect.That(t, errors.Is(err, limes.ErrScopeMismatch), "a list scoped to another tenant: %v, want ErrScopeMismatch", err)
}
