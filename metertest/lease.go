package metertest

import (
	"errors"
	"sync"
	"testing"
	"time"
	"uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/internal/expect"
	"github.com/oleg-tkachuk/limes/meter"
)

// The amounts the lease checks lease and charge, within the fixture's
// ceilings: the child's 20, the root's 25, the tenant's 30.
const (
	leased      = 10 * one
	leaseCharge = 3 * one
	leaseTopUp  = 5 * one
)

// leaser is f's Leaser, skipping the check without one.
func (f fixture) leaser(t *testing.T) meter.Leaser {
	t.Helper()
	l, ok := f.Usage.(meter.Leaser)
	if !ok {
		t.Skip("Env.Usage is no meter.Leaser")
	}
	return l
}

// usageOf is id's counters, none when it has no row.
func (f fixture) usageOf(t *testing.T, id uuid.UUID) meter.Usage {
	t.Helper()
	u, err := f.Usage.GetUsage(f.Ctx, id)
	if errors.Is(err, meter.ErrUsageNotFound) {
		return meter.Usage{CapabilityID: id}
	}
	expect.Must(t, err == nil, "usage of %s: %v", id, err)
	return u
}

// tenantOf is the tenant's aggregate.
func (f fixture) tenantOf(t *testing.T) meter.TenantBudget {
	t.Helper()
	b, err := f.Usage.GetTenantBudget(f.Ctx, f.Tenant)
	expect.Must(t, err == nil, "tenant budget: %v", err)
	return b
}

// lease leases amount to subject.
func (f fixture) lease(t *testing.T, l meter.Leaser, subject limes.MeterSubject, amount limes.Nanos) meter.Lease {
	t.Helper()
	got, err := l.Lease(f.Ctx, meter.LeaseRequest{Subject: subject, Amount: amount})
	expect.Must(t, err == nil, "Lease %v: %v", amount, err)
	return got
}

// chargeOf charges amount to subject, refusing past a ceiling.
func (f fixture) chargeOf(subject limes.MeterSubject, amount limes.Nanos) (meter.ChargeReceipt, error) {
	return f.Usage.Charge(f.Ctx, meter.ChargeRequest{Subject: subject, Amount: amount}, nil)
}

// A lease holds its amount on the child, the root and the tenant at once; a
// charge under it spends the child's counters and the lease, and leaves the
// root's and the tenant's as the lease left them; settled, the root and the
// tenant are charged what it spent and hold nothing.
func checkLeaseStopsChargesAtTheLeaseholder(t *testing.T, f fixture) {
	l := f.leaser(t)
	child := f.childSubject()
	got := f.lease(t, l, child, leased)
	expect.That(t, got.Amount == leased && got.Spent == 0 && got.CapabilityID == f.child && got.ID != uuid.Nil(), "Lease = %+v, want %v leased to the child", got, leased)
	for _, id := range []uuid.UUID{f.child, f.root} {
		expect.That(t, f.usageOf(t, id).ReservedAmount == leased, "%s holds %v once leased, want %v", id, f.usageOf(t, id).ReservedAmount, leased)
	}
	expect.That(t, f.tenantOf(t).ReservedAmount == leased, "the tenant holds %v once leased, want %v", f.tenantOf(t).ReservedAmount, leased)
	_, err := f.chargeOf(child, leaseCharge)
	expect.Must(t, err == nil, "Charge under the lease: %v", err)
	c, r, b := f.usageOf(t, f.child), f.usageOf(t, f.root), f.tenantOf(t)
	expect.That(t, c.SpentAmount == leaseCharge && c.ReservedAmount == leased-leaseCharge, "the child after a charge under its lease = %+v, want %v spent and %v held", c, leaseCharge, leased-leaseCharge)
	expect.That(t, r.SpentAmount == 0 && r.ReservedAmount == leased, "the root after a charge under the child's lease = %+v, want nothing spent and the lease held", r)
	expect.That(t, b.SpentAmount == 0 && b.ReservedAmount == leased, "the tenant after a charge under a lease = %v spent, %v held; want nothing spent and the lease held", b.SpentAmount, b.ReservedAmount)
	open, err := l.GetLease(f.Ctx, f.child)
	expect.That(t, err == nil && open.Spent == leaseCharge && open.ID == got.ID, "GetLease = %+v, %v; want the lease, %v spent", open, err, leaseCharge)
	settled, err := l.SettleLease(f.Ctx, meter.SettleLeaseRequest{Subject: child})
	expect.Must(t, err == nil && settled.Spent == leaseCharge, "SettleLease = %+v, %v; want the lease, %v spent", settled, err, leaseCharge)
	for name, u := range map[string]meter.Usage{"the child": f.usageOf(t, f.child), "the root": f.usageOf(t, f.root)} {
		expect.That(t, u.SpentAmount == leaseCharge && u.ReservedAmount == 0, "%s once the lease settled = %+v, want %v spent and nothing held", name, u, leaseCharge)
	}
	b = f.tenantOf(t)
	expect.That(t, b.SpentAmount == leaseCharge && b.ReservedAmount == 0, "the tenant once the lease settled = %v spent, %v held; want %v and nothing", b.SpentAmount, b.ReservedAmount, leaseCharge)
	_, err = l.GetLease(f.Ctx, f.child)
	expect.That(t, errors.Is(err, meter.ErrLeaseNotFound), "GetLease once settled: %v, want ErrLeaseNotFound", err)
	_, err = l.SettleLease(f.Ctx, meter.SettleLeaseRequest{Subject: child})
	expect.That(t, errors.Is(err, meter.ErrLeaseNotFound), "SettleLease again: %v, want ErrLeaseNotFound", err)
}

// A lease is held to every ceiling a reservation of its amount is: past
// the child's it is refused, and moves nothing.
func checkLeaseIsHeldToEveryCeiling(t *testing.T, f fixture) {
	l := f.leaser(t)
	_, err := l.Lease(f.Ctx, meter.LeaseRequest{Subject: f.childSubject(), Amount: childBudget + one})
	expect.That(t, errors.Is(err, limes.ErrBudgetExceeded), "a lease past the child's budget: %v, want ErrBudgetExceeded", err)
	for _, id := range []uuid.UUID{f.child, f.root} {
		expect.That(t, f.usageOf(t, id).ReservedAmount == 0, "%s holds %v after a refused lease", id, f.usageOf(t, id).ReservedAmount)
	}
	_, err = l.GetLease(f.Ctx, f.child)
	expect.That(t, errors.Is(err, meter.ErrLeaseNotFound), "GetLease after a refused lease: %v, want ErrLeaseNotFound", err)
	for name, bad := range map[string]meter.LeaseRequest{
		"no amount":      {Subject: f.childSubject()},
		"a negative TTL": {Subject: f.childSubject(), Amount: one, TTL: -time.Second},
	} {
		_, err := l.Lease(f.Ctx, bad)
		expect.That(t, errors.Is(err, limes.ErrInvalidRequest), "Lease with %s: %v, want ErrInvalidRequest", name, err)
	}
}

// A charge past what the lease holds is refused with ErrLeaseExhausted, a
// kind of ErrBudgetExceeded, and charges nothing; topped up, the lease
// covers it.
func checkLeaseExhaustedAndToppedUp(t *testing.T, f fixture) {
	l := f.leaser(t)
	child := f.childSubject()
	f.lease(t, l, child, leased)
	_, err := f.chargeOf(child, leased)
	expect.Must(t, err == nil, "a charge of the whole lease: %v", err)
	_, err = f.chargeOf(child, one)
	expect.That(t, errors.Is(err, meter.ErrLeaseExhausted) && errors.Is(err, limes.ErrBudgetExceeded), "a charge past the lease: %v, want ErrLeaseExhausted, an ErrBudgetExceeded", err)
	expect.That(t, f.usageOf(t, f.child).SpentAmount == leased, "the child spent %v after a refused charge, want %v", f.usageOf(t, f.child).SpentAmount, leased)
	topped := f.lease(t, l, child, leaseTopUp)
	expect.That(t, topped.Amount == leased+leaseTopUp && topped.Spent == leased, "a top-up = %+v, want %v leased, %v spent", topped, leased+leaseTopUp, leased)
	_, err = f.chargeOf(child, one)
	expect.That(t, err == nil, "a charge once topped up: %v", err)
}

// A capability whose ancestor holds a lease cannot lease: the ancestor's
// lease holds the chain's budget already.
func checkLeaseIsNotNested(t *testing.T, f fixture) {
	l := f.leaser(t)
	f.lease(t, l, newSubject(subjectSpec{capID: f.root, tenant: f.Tenant, budget: budgetOf(rootBudget)}), leased)
	_, err := l.Lease(f.Ctx, meter.LeaseRequest{Subject: f.childSubject(), Amount: one})
	expect.That(t, errors.Is(err, meter.ErrLeaseNested) && errors.Is(err, limes.ErrConflict), "a lease under a leased root: %v, want ErrLeaseNested, an ErrConflict", err)
}

// A capability delegated from the leaseholder charges under its lease: its
// own counters and the leaseholder's move, the lease's balance with them,
// and the root's do not.
func checkLeaseCoversDescendants(t *testing.T, f fixture) {
	l := f.leaser(t)
	f.lease(t, l, f.childSubject(), leased)
	grandchild, err := f.newCapability(f.child, unlimitedBudget)
	expect.Must(t, err == nil, "grandchild: %v", err)
	_, err = f.chargeOf(newSubject(subjectSpec{capID: grandchild, tenant: f.Tenant}), leaseCharge)
	expect.Must(t, err == nil, "a grandchild's charge under the child's lease: %v", err)
	expect.That(t, f.usageOf(t, grandchild).SpentAmount == leaseCharge, "the grandchild spent %v, want %v", f.usageOf(t, grandchild).SpentAmount, leaseCharge)
	c := f.usageOf(t, f.child)
	expect.That(t, c.SpentAmount == leaseCharge && c.ReservedAmount == leased-leaseCharge, "the leaseholder after its grandchild's charge = %+v", c)
	expect.That(t, f.usageOf(t, f.root).SpentAmount == 0, "the root spent %v under the child's lease, want nothing", f.usageOf(t, f.root).SpentAmount)
	open, err := l.GetLease(f.Ctx, f.child)
	expect.That(t, err == nil && open.Spent == leaseCharge, "the lease after the grandchild's charge = %+v, %v", open, err)
}

// A refund of a charge under an open lease goes back to the lease; one of
// a charge whose lease has been settled goes back to the chain the
// settlement charged.
func checkLeaseRefund(t *testing.T, f fixture) {
	l := f.leaser(t)
	child := f.childSubject()
	f.lease(t, l, child, leased)
	first, err := f.chargeOf(child, leaseCharge)
	expect.Must(t, err == nil, "first charge: %v", err)
	_, err = f.Usage.Refund(f.Ctx, meter.RefundRequest{ChargeID: first.ChargeID, Remaining: true})
	expect.Must(t, err == nil, "refund under the open lease: %v", err)
	open, err := l.GetLease(f.Ctx, f.child)
	expect.That(t, err == nil && open.Spent == 0, "the lease after a refund = %+v, %v; want nothing spent", open, err)
	c := f.usageOf(t, f.child)
	expect.That(t, c.SpentAmount == 0 && c.ReservedAmount == leased, "the child after a refund under its lease = %+v, want the lease held whole", c)
	second, err := f.chargeOf(child, leaseCharge)
	expect.Must(t, err == nil, "second charge: %v", err)
	_, err = l.SettleLease(f.Ctx, meter.SettleLeaseRequest{Subject: child})
	expect.Must(t, err == nil, "settle: %v", err)
	_, err = f.Usage.Refund(f.Ctx, meter.RefundRequest{ChargeID: second.ChargeID, Amount: one})
	expect.Must(t, err == nil, "refund once settled: %v", err)
	want := leaseCharge - one
	for name, got := range map[string]limes.Nanos{"the child": f.usageOf(t, f.child).SpentAmount, "the root": f.usageOf(t, f.root).SpentAmount, "the tenant": f.tenantOf(t).SpentAmount} {
		expect.That(t, got == want, "%s spent %v after a refund of a settled lease's charge, want %v", name, got, want)
	}
}

// A charge that records an overrun, and a reservation, go through the
// whole chain under a lease, as without one.
func checkLeaseIsBypassedByHoldsAndOverruns(t *testing.T, f fixture) {
	l := f.leaser(t)
	child := f.childSubject()
	f.lease(t, l, child, leased)
	_, err := f.Usage.Charge(f.Ctx, meter.ChargeRequest{Subject: child, Amount: one, Overrun: meter.OverrunPolicyRecord}, nil)
	expect.Must(t, err == nil, "a charge recording overruns: %v", err)
	expect.That(t, f.usageOf(t, f.root).SpentAmount == one, "the root spent %v by a charge recording overruns, want %v", f.usageOf(t, f.root).SpentAmount, one)
	held, err := f.Usage.Reserve(f.Ctx, meter.ReserveRequest{Subject: child, Amount: one})
	expect.Must(t, err == nil, "Reserve: %v", err)
	expect.That(t, f.usageOf(t, f.root).ReservedAmount == leased+one, "the root holds %v with the lease and a hold, want %v", f.usageOf(t, f.root).ReservedAmount, leased+one)
	expect.Must(t, f.Usage.Release(f.Ctx, meter.ReleaseRequest{Subject: child, ReservationID: held.ID}) == nil, "Release")
	open, err := l.GetLease(f.Ctx, f.child)
	expect.That(t, err == nil && open.Spent == 0, "the lease after charges around it = %+v, %v; want nothing spent", open, err)
}

// An expired lease covers no charge, which goes through the whole chain,
// and ReleaseExpired settles it.
func checkLeaseExpires(t *testing.T, f fixture) {
	l := f.leaser(t)
	if f.Advance == nil {
		t.Skip("Env.Advance not set")
	}
	const ttl = time.Minute
	child := f.childSubject()
	_, err := l.Lease(f.Ctx, meter.LeaseRequest{Subject: child, Amount: leased, TTL: ttl})
	expect.Must(t, err == nil, "Lease: %v", err)
	f.Advance(2 * ttl)
	_, err = f.chargeOf(child, one)
	expect.Must(t, err == nil, "a charge past the lease's end: %v", err)
	expect.That(t, f.usageOf(t, f.root).SpentAmount == one, "the root spent %v past the lease's end, want %v", f.usageOf(t, f.root).SpentAmount, one)
	res, err := f.Usage.ReleaseExpired(f.Ctx, meter.ReleaseExpiredRequest{})
	expect.Must(t, err == nil && res.SettledLeases == 1, "ReleaseExpired = %+v, %v; want the lease settled", res, err)
	expect.That(t, f.usageOf(t, f.root).ReservedAmount == 0 && f.tenantOf(t).ReservedAmount == 0, "the root or the tenant still holds the expired lease")
}

// A leaseholder's counters dropped settle its lease first: the root and
// the tenant are charged what it spent and hold nothing.
func checkLeaseIsSettledBeforeItsCountersGo(t *testing.T, f fixture) {
	l := f.leaser(t)
	child := f.childSubject()
	f.lease(t, l, child, leased)
	_, err := f.chargeOf(child, leaseCharge)
	expect.Must(t, err == nil, "charge: %v", err)
	expect.Must(t, f.Usage.DeleteUsage(f.Ctx, meter.DeleteUsageRequest{CapabilityID: f.child}) == nil, "DeleteUsage")
	r := f.usageOf(t, f.root)
	expect.That(t, r.SpentAmount == leaseCharge && r.ReservedAmount == 0, "the root once its leased child's counters went = %+v, want %v spent and nothing held", r, leaseCharge)
	expect.That(t, f.tenantOf(t).ReservedAmount == 0, "the tenant still holds the lease of counters gone")
}

// leaseWorkers is how many children charge under leases of their own at
// once in checkLeaseConcurrency.
const leaseWorkers = 4

// Children charging under leases of their own at once, from one root,
// never spend past the root's ceiling between them; settled, the root and
// the tenant are charged exactly what they spent and hold nothing.
func checkLeaseConcurrency(t *testing.T, f fixture) {
	l := f.leaser(t)
	share := rootBudget / leaseWorkers
	var wg sync.WaitGroup
	spent := make([]limes.Nanos, leaseWorkers)
	subjects := make([]limes.MeterSubject, leaseWorkers)
	for i := range leaseWorkers {
		id, err := f.newCapability(f.root, unlimitedBudget)
		expect.Must(t, err == nil, "child %d: %v", i, err)
		subjects[i] = newSubject(subjectSpec{capID: id, tenant: f.Tenant})
		f.lease(t, l, subjects[i], share)
	}
	for i := range leaseWorkers {
		wg.Go(func() {
			for {
				if _, err := f.chargeOf(subjects[i], cent*10); err != nil {
					return
				}
				spent[i] += cent * 10
			}
		})
	}
	wg.Wait()
	var total limes.Nanos
	for i, s := range subjects {
		total += spent[i]
		_, err := l.SettleLease(f.Ctx, meter.SettleLeaseRequest{Subject: s})
		expect.Must(t, err == nil, "settle %d: %v", i, err)
	}
	r, b := f.usageOf(t, f.root), f.tenantOf(t)
	expect.That(t, total <= rootBudget, "the children spent %v under leases, past the root's %v", total, rootBudget)
	expect.That(t, r.SpentAmount == total && r.ReservedAmount == 0, "the root once every lease settled = %+v, want %v spent and nothing held", r, total)
	expect.That(t, b.SpentAmount == total && b.ReservedAmount == 0, "the tenant once every lease settled = %v spent, %v held; want %v and nothing", b.SpentAmount, b.ReservedAmount, total)
}

// A reset of the tenant's spend settles its leases first, into the period
// it closes.
func checkLeaseIsSettledByAReset(t *testing.T, f fixture) {
	l := f.leaser(t)
	child := f.childSubject()
	f.lease(t, l, child, leased)
	_, err := f.chargeOf(child, leaseCharge)
	expect.Must(t, err == nil, "charge: %v", err)
	b := f.tenantOf(t)
	_, err = f.Usage.SetTenantBudget(f.Ctx, meter.SetTenantBudgetRequest{
		TenantID: f.Tenant, Ceiling: limes.BudgetCeiling{Max: tenantBudget, UnitCode: unit}, ResetSpend: true, ExpectedVersion: b.ResourceVersion,
	})
	expect.Must(t, err == nil, "reset: %v", err)
	_, err = l.GetLease(f.Ctx, f.child)
	expect.That(t, errors.Is(err, meter.ErrLeaseNotFound), "GetLease after a reset: %v, want the lease settled", err)
	b = f.tenantOf(t)
	expect.That(t, b.SpentAmount == 0 && b.ReservedAmount == 0, "the tenant after a reset = %v spent, %v held; want a new period holding nothing", b.SpentAmount, b.ReservedAmount)
	expect.That(t, f.usageOf(t, f.root).SpentAmount == leaseCharge, "the root spent %v once the lease settled by the reset, want %v", f.usageOf(t, f.root).SpentAmount, leaseCharge)
}
