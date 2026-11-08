package metertest

import (
	"errors"
	"testing"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/internal/expect"
	"github.com/oleg-tkachuk/limes/meter"
)

// topUpHeadroom is the most a top-up of the child's lease may add once
// leaseCharge of leased is spent: what the child's ceiling leaves past the
// lease it holds, whose spend counts on it.
const topUpHeadroom = childBudget - leased

// A top-up settles the open lease — what it spent charged to the root and
// the tenant — and opens a new one, under a new id, of what the old one had
// left and the top-up, held on the child, the root and the tenant.
func checkLeaseTopUpSettlesAndReopens(t *testing.T, f fixture) {
	l := f.leaser(t)
	child := f.childSubject()
	first := f.lease(t, l, child, leased)
	_, err := f.chargeOf(child, leaseCharge)
	expect.Must(t, err == nil, "charge under the lease: %v", err)
	topped := f.lease(t, l, child, leaseTopUp)
	want := leased - leaseCharge + leaseTopUp
	expect.That(t, topped.ID != first.ID, "a top-up kept the lease's id %s, want a new lease", first.ID)
	expect.That(t, topped.Amount == want && topped.Spent == 0, "a top-up = %+v, want %v leased and nothing spent", topped, want)
	open, err := l.GetLease(f.Ctx, f.child)
	expect.That(t, err == nil && open.ID == topped.ID, "GetLease after a top-up = %+v, %v; want the new lease", open, err)
	c, r, b := f.usageOf(t, f.child), f.usageOf(t, f.root), f.tenantOf(t)
	expect.That(t, c.SpentAmount == leaseCharge && c.ReservedAmount == want, "the child after a top-up = %+v, want %v spent and %v held", c, leaseCharge, want)
	expect.That(t, r.SpentAmount == leaseCharge && r.ReservedAmount == want, "the root after a top-up = %+v, want the settled %v spent and %v held", r, leaseCharge, want)
	expect.That(t, b.SpentAmount == leaseCharge && b.ReservedAmount == want, "the tenant after a top-up = %v spent, %v held; want the settled %v spent and %v held", b.SpentAmount, b.ReservedAmount, leaseCharge, want)
}

// A top-up is held to the ceilings as the open lease is: one up to what the
// child's ceiling leaves opens, one past it is refused and leaves the open
// lease as it was, unsettled.
func checkLeaseTopUpIsHeldToTheCeilings(t *testing.T, f fixture) {
	l := f.leaser(t)
	child := f.childSubject()
	first := f.lease(t, l, child, leased)
	_, err := f.chargeOf(child, leaseCharge)
	expect.Must(t, err == nil, "charge under the lease: %v", err)
	_, err = l.Lease(f.Ctx, meter.LeaseRequest{Subject: child, Amount: topUpHeadroom + one})
	expect.That(t, errors.Is(err, limes.ErrBudgetExceeded), "a top-up past the child's ceiling: %v, want ErrBudgetExceeded", err)
	open, err := l.GetLease(f.Ctx, f.child)
	expect.That(t, err == nil && open.ID == first.ID && open.Amount == leased && open.Spent == leaseCharge, "the lease after a refused top-up = %+v, %v; want it as it was", open, err)
	expect.That(t, f.usageOf(t, f.root).SpentAmount == 0, "a refused top-up settled the lease: the root spent %v", f.usageOf(t, f.root).SpentAmount)
	topped, err := l.Lease(f.Ctx, meter.LeaseRequest{Subject: child, Amount: topUpHeadroom})
	expect.That(t, err == nil && topped.Amount == leased-leaseCharge+topUpHeadroom, "a top-up up to the child's ceiling = %+v, %v", topped, err)
}

// A refund of a charge taken before a top-up goes back to the chain the
// top-up's settlement charged, and leaves the new lease's balance alone; one
// of a charge taken after it goes back to the new lease.
func checkLeaseTopUpRoutesRefunds(t *testing.T, f fixture) {
	l := f.leaser(t)
	child := f.childSubject()
	f.lease(t, l, child, leased)
	before, err := f.chargeOf(child, leaseCharge)
	expect.Must(t, err == nil, "charge under the first lease: %v", err)
	topped := f.lease(t, l, child, leaseTopUp)
	expect.Must(t, f.usageOf(t, f.root).SpentAmount == leaseCharge, "the root spent %v once the top-up settled the first lease, want %v", f.usageOf(t, f.root).SpentAmount, leaseCharge)
	_, err = f.Usage.Refund(f.Ctx, meter.RefundRequest{ChargeID: before.ChargeID, Remaining: true}, nil)
	expect.Must(t, err == nil, "refund of the charge before the top-up: %v", err)
	for name, got := range map[string]limes.Nanos{"the child": f.usageOf(t, f.child).SpentAmount, "the root": f.usageOf(t, f.root).SpentAmount, "the tenant": f.tenantOf(t).SpentAmount} {
		expect.That(t, got == 0, "%s spent %v after a refund of the charge the top-up settled, want nothing", name, got)
	}
	open, err := l.GetLease(f.Ctx, f.child)
	expect.That(t, err == nil && open.ID == topped.ID && open.Spent == 0 && open.Amount == topped.Amount, "the new lease after a refund of a charge before it = %+v, %v; want %+v untouched", open, err, topped)
	expect.That(t, f.usageOf(t, f.child).ReservedAmount == topped.Amount, "the child holds %v after the refund, want the new lease's %v", f.usageOf(t, f.child).ReservedAmount, topped.Amount)
	after, err := f.chargeOf(child, leaseCharge)
	expect.Must(t, err == nil, "charge under the new lease: %v", err)
	_, err = f.Usage.Refund(f.Ctx, meter.RefundRequest{ChargeID: after.ChargeID, Remaining: true}, nil)
	expect.Must(t, err == nil, "refund of the charge after the top-up: %v", err)
	open, err = l.GetLease(f.Ctx, f.child)
	expect.That(t, err == nil && open.ID == topped.ID && open.Spent == 0, "the new lease after a refund of its charge = %+v, %v; want nothing spent", open, err)
	c := f.usageOf(t, f.child)
	expect.That(t, c.SpentAmount == 0 && c.ReservedAmount == topped.Amount, "the child after a refund under the new lease = %+v, want the lease held whole", c)
	expect.That(t, f.usageOf(t, f.root).SpentAmount == 0 && f.tenantOf(t).SpentAmount == 0, "a refund under the new lease reached the root or the tenant")
}

// A capability delegated from the leaseholder goes on charging under its
// lease once topped up: the lease is found by the leaseholder, not by id,
// and what the descendant charged before the top-up was settled with it.
func checkLeaseTopUpKeepsCoveringDescendants(t *testing.T, f fixture) {
	l := f.leaser(t)
	f.lease(t, l, f.childSubject(), leased)
	grandchild, err := f.newCapability(f.child, unlimitedBudget)
	expect.Must(t, err == nil, "grandchild: %v", err)
	below := newSubject(subjectSpec{capID: grandchild, tenant: f.Tenant})
	_, err = f.chargeOf(below, leaseCharge)
	expect.Must(t, err == nil, "the grandchild's charge under the first lease: %v", err)
	topped := f.lease(t, l, f.childSubject(), leaseTopUp)
	_, err = f.chargeOf(below, leaseCharge)
	expect.Must(t, err == nil, "the grandchild's charge under the topped-up lease: %v", err)
	open, err := l.GetLease(f.Ctx, f.child)
	expect.That(t, err == nil && open.ID == topped.ID && open.Spent == leaseCharge, "the topped-up lease after the grandchild's charge = %+v, %v; want %v spent", open, err, leaseCharge)
	expect.That(t, f.usageOf(t, f.root).SpentAmount == leaseCharge, "the root spent %v, want only the settled %v", f.usageOf(t, f.root).SpentAmount, leaseCharge)
	expect.That(t, f.usageOf(t, grandchild).SpentAmount == 2*leaseCharge, "the grandchild spent %v, want %v", f.usageOf(t, grandchild).SpentAmount, 2*leaseCharge)
	_, err = l.Lease(f.Ctx, meter.LeaseRequest{Subject: below, Amount: one})
	expect.That(t, errors.Is(err, meter.ErrLeaseNested), "a lease of the grandchild under the topped-up lease: %v, want ErrLeaseNested", err)
}
