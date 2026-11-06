package metertest

import (
	"context"
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
	receipt, err := f.chargeOf(child, leaseCharge)
	expect.Must(t, err == nil, "Charge under the lease: %v", err)
	expect.That(t, receipt.SpentAmount == leaseCharge, "the receipt of a charge under the lease reads %v spent, want %v", receipt.SpentAmount, leaseCharge)
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
	advance := f.advanceExpiry()
	if advance == nil {
		t.Skip("Env.Advance and Env.AdvanceExpiry not set")
	}
	const ttl = time.Minute
	child := f.childSubject()
	_, err := l.Lease(f.Ctx, meter.LeaseRequest{Subject: child, Amount: leased, TTL: ttl})
	expect.Must(t, err == nil, "Lease: %v", err)
	advance(2 * ttl)
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

// An ancestor's counters dropped settle the leases below it first, as a
// reset settles the tenant's: a lease holds against its chain, and one left
// open past the restart would charge under a hold the ancestor no longer
// keeps, past the ancestor's ceiling.
func checkLeaseIsSettledBeforeAnAncestorsCountersGo(t *testing.T, f fixture) {
	l := f.leaser(t)
	child := f.childSubject()
	f.lease(t, l, child, leased)
	_, err := f.chargeOf(child, leaseCharge)
	expect.Must(t, err == nil, "charge: %v", err)
	expect.Must(t, f.Usage.DeleteUsage(f.Ctx, meter.DeleteUsageRequest{CapabilityID: f.root}) == nil, "DeleteUsage")
	_, err = l.GetLease(f.Ctx, f.child)
	expect.That(t, errors.Is(err, meter.ErrLeaseNotFound), "the child's lease once the root's counters went: %v, want ErrLeaseNotFound", err)
	expect.That(t, f.tenantOf(t).SpentAmount == leaseCharge && f.tenantOf(t).ReservedAmount == 0, "the tenant once the root's counters went = %+v, want %v spent and nothing held", f.tenantOf(t), leaseCharge)
	// The root, restarted, takes its whole ceiling; the child may then
	// spend nothing more of it.
	_, err = f.chargeOf(newSubject(subjectSpec{capID: f.root, tenant: f.Tenant, budget: budgetOf(rootBudget)}), rootBudget)
	expect.Must(t, err == nil, "the restarted root's whole ceiling: %v", err)
	_, err = f.chargeOf(child, one)
	expect.That(t, errors.Is(err, limes.ErrBudgetExceeded), "a child's charge past its restarted root's ceiling: %v, want ErrBudgetExceeded", err)
}

// A refund of a charge taken under a lease that DeleteUsage settled leaves
// the counters restarted since alone, as a refund of a charge taken without
// one does: the settlement charged the chain before they restarted, so the
// refund goes back to the counters that are still the ones it charged.
func checkLeaseRefundAfterRestart(t *testing.T, f fixture) {
	l := f.leaser(t)
	child := f.childSubject()
	charge := func(amount limes.Nanos) meter.ChargeReceipt {
		t.Helper()
		r, err := f.chargeOf(child, amount)
		expect.Must(t, err == nil, "charge: %v", err)
		return r
	}
	restart := func(id uuid.UUID) {
		t.Helper()
		expect.Must(t, f.Usage.DeleteUsage(f.Ctx, meter.DeleteUsageRequest{CapabilityID: id}) == nil, "DeleteUsage")
	}
	refund := func(r meter.ChargeReceipt) {
		t.Helper()
		got, err := f.Usage.Refund(f.Ctx, meter.RefundRequest{ChargeID: r.ChargeID, Remaining: true})
		expect.Must(t, err == nil && got.Amount == leaseCharge, "refund = %+v, %v; want %v", got, err, leaseCharge)
	}
	counted := func(when string, root, child, tenant limes.Nanos) {
		t.Helper()
		expect.That(t, f.spent(t, f.root) == root, "%s: root spent %v, want %v", when, f.spent(t, f.root), root)
		expect.That(t, f.spent(t, f.child) == child, "%s: child spent %v, want %v", when, f.spent(t, f.child), child)
		expect.That(t, f.tenantSpent(t) == tenant, "%s: tenant spent %v, want %v", when, f.tenantSpent(t), tenant)
		expect.That(t, f.tenantOf(t).ReservedAmount == 0, "%s: the tenant still holds a lease", when)
	}
	f.lease(t, l, child, leased)
	first := charge(leaseCharge)
	restart(f.root)
	charge(one)
	counted("the root restarted under its child's lease", one, leaseCharge+one, leaseCharge+one)
	refund(first)
	counted("a refund of a leased charge past the root's restart", one, one, one)
	f.lease(t, l, child, leased)
	second := charge(leaseCharge)
	restart(f.child)
	counted("the leaseholder restarted", one+leaseCharge, 0, one+leaseCharge)
	refund(second)
	counted("a refund of a leased charge past the leaseholder's restart", one, 0, one)
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

// A Biscuit copy leases nothing: what it charges and bumps goes through the
// whole chain, which a lease would hold beyond the copy's own ceilings, so
// a lease of one is refused, as a Reserve past those ceilings is, and
// nothing is held.
func checkLeaseOfACopyIsRefused(t *testing.T, f fixture) {
	l := f.leaser(t)
	copies := []limes.CopyCeiling{{RevocationID: []byte("leasing-copy"), Budget: &limes.BudgetCeiling{Max: one, UnitCode: unit}}}
	subject := newSubject(subjectSpec{capID: f.child, tenant: f.Tenant, budget: budgetOf(childBudget), copies: copies})
	_, err := l.Lease(f.Ctx, meter.LeaseRequest{Subject: subject, Amount: leased})
	expect.That(t, errors.Is(err, limes.ErrInvalidRequest), "Lease of a copy = %v, want ErrInvalidRequest", err)
	for _, id := range []uuid.UUID{f.child, f.root} {
		expect.That(t, f.usageOf(t, id).ReservedAmount == 0, "%s holds %v after a copy's refused lease, want nothing", id, f.usageOf(t, id).ReservedAmount)
	}
}

// The requests the narrowest leases and ceilings of the lease checks hold:
// the least lease of requests there is, and the request ceiling of a child
// bumping under its root's lease.
const (
	leastRequests  int64 = 1
	narrowRequests int64 = 1
)

// rootSubjectOf is the fixture's root as a verified capability would give
// it, with a request ceiling of requests.
func (f fixture) rootSubjectOf(requests int64) limes.MeterSubject {
	return newSubject(subjectSpec{capID: f.root, tenant: f.Tenant, budget: budgetOf(rootBudget), requests: requestsOf(requests)})
}

// childSubjectOf is the fixture's child with a request ceiling of requests.
func (f fixture) childSubjectOf(requests int64) limes.MeterSubject {
	return newSubject(subjectSpec{capID: f.child, tenant: f.Tenant, budget: budgetOf(childBudget), requests: requestsOf(requests)})
}

// A lease past its end covers nothing below it: a capability under the
// expired leaseholder charges through the whole chain, the tenant with it,
// and leases on its own.
func checkAnExpiredLeaseCoversNothingBelow(t *testing.T, f fixture) {
	l := f.leaser(t)
	advance := f.advanceExpiry()
	if advance == nil {
		t.Skip("Env.Advance and Env.AdvanceExpiry not set")
	}
	const ttl = time.Minute
	_, err := l.Lease(f.Ctx, meter.LeaseRequest{Subject: newSubject(subjectSpec{capID: f.root, tenant: f.Tenant, budget: budgetOf(rootBudget)}), Amount: leased, TTL: ttl})
	expect.Must(t, err == nil, "the root's Lease: %v", err)
	advance(2 * ttl)
	child := f.childSubject()
	_, err = f.chargeOf(child, leaseCharge)
	expect.Must(t, err == nil, "the child's charge past the root's lease's end: %v", err)
	expect.That(t, f.tenantOf(t).SpentAmount == leaseCharge, "the tenant spent %v of a charge past the root's lease's end, want %v", f.tenantOf(t).SpentAmount, leaseCharge)
	_, err = l.Lease(f.Ctx, meter.LeaseRequest{Subject: child, Amount: leased})
	expect.That(t, err == nil, "the child's lease under the root's expired one: %v", err)
}

// Under an ancestor's lease a capability is held to its own ceilings, as
// without one: past the child's budget, or its requests, a charge or a bump
// is refused though the root's lease has room for it.
func checkALeaseHoldsADescendantToItsOwnCeilings(t *testing.T, f fixture) {
	l := f.leaser(t)
	_, err := l.Lease(f.Ctx, meter.LeaseRequest{Subject: f.rootSubjectOf(requestRoot), Amount: rootBudget, Requests: requestRoot})
	expect.Must(t, err == nil, "the root's Lease: %v", err)
	child := f.childSubjectOf(narrowRequests)
	_, err = f.chargeOf(child, childBudget+one)
	expect.That(t, errors.Is(err, limes.ErrBudgetExceeded) && !errors.Is(err, meter.ErrLeaseExhausted), "a charge past the child's budget under the root's lease: %v, want ErrBudgetExceeded, not ErrLeaseExhausted", err)
	for range narrowRequests {
		expect.Must(t, f.bump(child) == nil, "a bump within the child's requests")
	}
	err = f.bump(child)
	expect.That(t, errors.Is(err, limes.ErrRequestLimitExceeded) && !errors.Is(err, meter.ErrLeaseRequestsExhausted), "a bump past the child's requests under the root's lease: %v, want ErrRequestLimitExceeded, not ErrLeaseRequestsExhausted", err)
	open, err := l.GetLease(f.Ctx, f.root)
	expect.That(t, err == nil && open.Spent == 0 && open.RequestsSpent == narrowRequests, "the root's lease after the refusals = %+v, %v; want nothing spent and %d requests made", open, err, narrowRequests)
}

// A lease of a capability's whole ceiling is its to spend: the ceiling
// admitted the lease, so every charge and bump under it is admitted.
func checkALeaseOfTheWholeCeilingIsSpendable(t *testing.T, f fixture) {
	l := f.leaser(t)
	child := f.childSubjectOf(requestChild)
	_, err := l.Lease(f.Ctx, meter.LeaseRequest{Subject: child, Amount: childBudget, Requests: requestChild})
	expect.Must(t, err == nil, "a Lease of the child's whole ceilings: %v", err)
	_, err = f.chargeOf(child, childBudget)
	expect.That(t, err == nil, "a charge of the whole lease: %v", err)
	for i := range requestChild {
		expect.That(t, f.bump(child) == nil, "bump %d of the whole lease", i)
	}
	open, err := l.GetLease(f.Ctx, f.child)
	expect.That(t, err == nil && open.Spent == childBudget && open.RequestsSpent == requestChild, "the lease spent whole = %+v, %v; want %v and %d requests spent", open, err, childBudget, requestChild)
}

// The least lease there is holds and covers as any other: a nano and a
// request leased are held on the chain, a charge of the nano and a bump go
// under it and stop at the leaseholder, which holds nothing once both are
// spent.
func checkTheLeastLeaseCovers(t *testing.T, f fixture) {
	l := f.leaser(t)
	child := f.childSubjectOf(requestChild)
	_, err := l.Lease(f.Ctx, meter.LeaseRequest{Subject: child, Amount: nano, Requests: leastRequests})
	expect.Must(t, err == nil, "a Lease of a nano and a request: %v", err)
	r := f.usageOf(t, f.root)
	expect.That(t, r.ReservedAmount == nano && r.ReservedRequests == leastRequests, "the root under the least lease = %+v, want a nano and a request held", r)
	_, err = f.chargeOf(child, nano)
	expect.Must(t, err == nil, "a charge of the nano leased: %v", err)
	expect.Must(t, f.bump(child) == nil, "a bump of the request leased")
	expect.That(t, f.tenantOf(t).SpentAmount == 0 && f.usageOf(t, f.root).RequestCount == 0, "a charge or a bump under the least lease went past the leaseholder")
	c := f.usageOf(t, f.child)
	expect.That(t, c.SpentAmount == nano && c.RequestCount == leastRequests && c.ReservedAmount == 0 && c.ReservedRequests == 0, "the leaseholder with its least lease spent = %+v, want a nano and a request spent and nothing held", c)
	open, err := l.GetLease(f.Ctx, f.child)
	expect.That(t, err == nil && open.Spent == nano && open.RequestsSpent == leastRequests, "the least lease spent = %+v, %v; want a nano and a request spent", open, err)
}

// A refund of a descendant's charge under an open lease goes back, once, to
// the descendant, the leaseholder and the lease's balance, and to nothing
// above; one of a charge taken before the descendant's counters restarted
// leaves the restarted counters alone.
func checkLeaseRefundOfADescendantsCharge(t *testing.T, f fixture) {
	l := f.leaser(t)
	f.lease(t, l, f.childSubject(), leased)
	grandchild, err := f.newCapability(f.child, unlimitedBudget)
	expect.Must(t, err == nil, "grandchild: %v", err)
	below := newSubject(subjectSpec{capID: grandchild, tenant: f.Tenant})
	charge := func(amount limes.Nanos) meter.ChargeReceipt {
		t.Helper()
		r, err := f.chargeOf(below, amount)
		expect.Must(t, err == nil, "the grandchild's charge: %v", err)
		return r
	}
	refund := func(r meter.ChargeReceipt) {
		t.Helper()
		_, err := f.Usage.Refund(f.Ctx, meter.RefundRequest{ChargeID: r.ChargeID, Remaining: true})
		expect.Must(t, err == nil, "refund: %v", err)
	}
	first, second := charge(leaseCharge), charge(leaseCharge)
	refund(first)
	expect.That(t, f.usageOf(t, grandchild).SpentAmount == leaseCharge, "the grandchild spent %v once one of its two charges was refunded, want %v", f.usageOf(t, grandchild).SpentAmount, leaseCharge)
	c := f.usageOf(t, f.child)
	expect.That(t, c.SpentAmount == leaseCharge && c.ReservedAmount == leased-leaseCharge, "the leaseholder after a refund of its grandchild's charge = %+v, want %v spent and %v held", c, leaseCharge, leased-leaseCharge)
	open, err := l.GetLease(f.Ctx, f.child)
	expect.That(t, err == nil && open.Spent == leaseCharge, "the lease after the refund = %+v, %v; want %v spent", open, err, leaseCharge)
	expect.That(t, f.usageOf(t, f.root).SpentAmount == 0 && f.tenantOf(t).SpentAmount == 0, "a refund under the child's lease reached the root or the tenant")
	expect.Must(t, f.Usage.DeleteUsage(f.Ctx, meter.DeleteUsageRequest{CapabilityID: grandchild}) == nil, "DeleteUsage of the grandchild")
	charge(one)
	refund(second)
	expect.That(t, f.usageOf(t, grandchild).SpentAmount == one, "the grandchild's restarted counters spent %v after a refund of a charge before the restart, want %v", f.usageOf(t, grandchild).SpentAmount, one)
	expect.That(t, f.usageOf(t, f.child).SpentAmount == one, "the leaseholder spent %v after the second refund, want %v", f.usageOf(t, f.child).SpentAmount, one)
}

// A refund of a charge whose lease was settled goes back to the chain the
// settlement charged, even once the capability has leased again: the new
// lease neither spent it nor takes it.
func checkLeaseRefundSkipsALeaseOpenedSince(t *testing.T, f fixture) {
	l := f.leaser(t)
	child := f.childSubject()
	f.lease(t, l, child, leased)
	charged, err := f.chargeOf(child, leaseCharge)
	expect.Must(t, err == nil, "charge: %v", err)
	_, err = l.SettleLease(f.Ctx, meter.SettleLeaseRequest{Subject: child})
	expect.Must(t, err == nil, "settle: %v", err)
	f.lease(t, l, child, leased)
	_, err = f.Usage.Refund(f.Ctx, meter.RefundRequest{ChargeID: charged.ChargeID, Remaining: true})
	expect.Must(t, err == nil, "refund: %v", err)
	for name, got := range map[string]limes.Nanos{"the child": f.usageOf(t, f.child).SpentAmount, "the root": f.usageOf(t, f.root).SpentAmount, "the tenant": f.tenantOf(t).SpentAmount} {
		expect.That(t, got == 0, "%s spent %v after a refund of the settled lease's charge, want nothing", name, got)
	}
	expect.That(t, f.usageOf(t, f.child).ReservedAmount == leased, "the child holds %v under its new lease after the refund, want %v", f.usageOf(t, f.child).ReservedAmount, leased)
	open, err := l.GetLease(f.Ctx, f.child)
	expect.That(t, err == nil && open.Spent == 0 && open.Amount == leased, "the new lease after the refund = %+v, %v; want %v leased and nothing spent", open, err, leased)
}

// A lease above a descendant's settles only what it holds itself: the
// ancestor keeps holding what the descendant's lease holds of it, budget
// and requests.
func checkALeaseAboveAnotherSettlesOnlyItsOwn(t *testing.T, f fixture) {
	l := f.leaser(t)
	_, err := l.Lease(f.Ctx, meter.LeaseRequest{Subject: f.childSubjectOf(requestChild), Amount: leased, Requests: requestLeased})
	expect.Must(t, err == nil, "the child's Lease: %v", err)
	root := f.rootSubjectOf(requestRoot)
	_, err = l.Lease(f.Ctx, meter.LeaseRequest{Subject: root, Amount: leased, Requests: requestLeased})
	expect.Must(t, err == nil, "the root's Lease above the child's: %v", err)
	_, err = f.chargeOf(root, leaseCharge)
	expect.Must(t, err == nil, "the root's charge under its lease: %v", err)
	expect.Must(t, f.bump(root) == nil, "the root's bump under its lease")
	_, err = l.SettleLease(f.Ctx, meter.SettleLeaseRequest{Subject: root})
	expect.Must(t, err == nil, "the root's SettleLease: %v", err)
	r := f.usageOf(t, f.root)
	expect.That(t, r.ReservedAmount == leased && r.ReservedRequests == requestLeased, "the root once its own lease settled = %+v, want the child's lease, %v and %d requests, still held", r, leased, requestLeased)
	expect.That(t, r.SpentAmount == leaseCharge && r.RequestCount == 1, "the root once its own lease settled = %+v, want %v and a request spent", r, leaseCharge)
}

// SettleLease settles only the lease of its subject's capability in the
// subject's tenant: the leaseholder named under another tenant finds none,
// and the lease stays open.
func checkLeaseIsSettledOnlyInItsTenant(t *testing.T, f fixture) {
	l := f.leaser(t)
	f.lease(t, l, f.childSubject(), leased)
	elsewhere := newSubject(subjectSpec{capID: f.child, tenant: uuid.New(), budget: budgetOf(childBudget)})
	_, err := l.SettleLease(f.Ctx, meter.SettleLeaseRequest{Subject: elsewhere})
	expect.That(t, errors.Is(err, meter.ErrLeaseNotFound) || errors.Is(err, limes.ErrScopeMismatch), "SettleLease of the child under another tenant: %v, want ErrLeaseNotFound or ErrScopeMismatch", err)
	open, err := l.GetLease(f.Ctx, f.child)
	expect.That(t, err == nil && open.Amount == leased, "the child's lease after a settle under another tenant = %+v, %v; want it open", open, err)
}

// An erase of the tenant's usage ends the tenant's leases and no other
// tenant's.
func checkEraseTenantUsageEndsOnlyItsLeases(t *testing.T, f fixture) {
	l := f.leaser(t)
	eraser, ok := f.Usage.(meter.TenantUsageEraser)
	if !ok {
		t.Skip("the UsageStore does not implement meter.TenantUsageEraser")
	}
	other, err := f.newTenant()
	expect.Must(t, err == nil, "NewTenant: %v", err)
	theirCap, err := f.NewCapability(CapabilitySpec{Tenant: other, Requests: limes.RequestCeiling{Unlimited: true}, Budget: unlimitedBudget})
	expect.Must(t, err == nil, "capability of another tenant: %v", err)
	theirCtx := f.tenantCtx(other)
	_, err = l.Lease(theirCtx, meter.LeaseRequest{Subject: newSubject(subjectSpec{capID: theirCap, tenant: other}), Amount: leased})
	expect.Must(t, err == nil, "another tenant's Lease: %v", err)
	f.lease(t, l, f.childSubject(), leased)
	expect.Must(t, eraser.EraseTenantUsage(f.Ctx, meter.EraseTenantUsageRequest{TenantID: f.Tenant}) == nil, "EraseTenantUsage")
	_, err = l.GetLease(f.Ctx, f.child)
	expect.That(t, errors.Is(err, meter.ErrLeaseNotFound), "the erased tenant's lease: %v, want ErrLeaseNotFound", err)
	theirs, err := l.GetLease(theirCtx, theirCap)
	expect.That(t, err == nil && theirs.Amount == leased, "another tenant's lease after the erase = %+v, %v; want it open", theirs, err)
}

// A charge under a lease runs onCharged as one without does: a callback
// that succeeds sees the charge through, one that fails is returned as it
// is, and the charge moves neither the counters nor the lease.
func checkALeasedChargeRunsOnCharged(t *testing.T, f fixture) {
	l := f.leaser(t)
	child := f.childSubject()
	f.lease(t, l, child, leased)
	var seen meter.ChargeReceipt
	r, err := f.Usage.Charge(f.Ctx, meter.ChargeRequest{Subject: child, Amount: leaseCharge}, func(_ context.Context, c meter.Charged) error {
		seen = c.Receipt
		return nil
	})
	expect.Must(t, err == nil && r.ChargeID != uuid.Nil() && seen.ChargeID == r.ChargeID, "a leased charge with a callback = %+v, %v; the callback saw %+v", r, err, seen)
	boom := errors.New("side effect failed")
	_, err = f.Usage.Charge(f.Ctx, meter.ChargeRequest{Subject: child, Amount: leaseCharge}, func(context.Context, meter.Charged) error { return boom })
	expect.That(t, errors.Is(err, boom), "a leased charge with a failing callback: err = %v, want the callback's", err)
	open, err := l.GetLease(f.Ctx, f.child)
	expect.That(t, err == nil && open.Spent == leaseCharge, "the lease after a failed callback = %+v, %v; want %v spent", open, err, leaseCharge)
	expect.That(t, f.usageOf(t, f.child).SpentAmount == leaseCharge, "the child spent %v after a failed callback, want %v", f.usageOf(t, f.child).SpentAmount, leaseCharge)
}
