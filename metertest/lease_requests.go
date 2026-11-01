package metertest

import (
	"errors"
	"math"
	"sync"
	"testing"
	"time"
	"uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/internal/expect"
	"github.com/oleg-tkachuk/limes/meter"
)

// The request ceilings the request-lease checks work under, and what they
// lease: a root of requestRoot requests, a child of requestChild under it,
// and a lease of requestLeased.
const (
	requestRoot   = 10
	requestChild  = 8
	requestLeased = 5
	requestTopUp  = 2
)

// requestChain is a root and a child under it with request ceilings of
// their own and no budget ceiling, for the request-lease checks.
type requestChain struct {
	root, child uuid.UUID
}

func (f fixture) newRequestChain(t *testing.T) requestChain {
	t.Helper()
	root, err := f.NewCapability(CapabilitySpec{Requests: limes.RequestCeiling{Max: requestRoot}, Budget: unlimitedBudget})
	expect.Must(t, err == nil, "root: %v", err)
	child, err := f.NewCapability(CapabilitySpec{Parent: root, Requests: limes.RequestCeiling{Max: requestChild}, Budget: unlimitedBudget})
	expect.Must(t, err == nil, "child: %v", err)
	return requestChain{root: root, child: child}
}

// requestSubject is id as a verified capability with a request ceiling of max
// would give it.
func (f fixture) requestSubject(id uuid.UUID, max int64) limes.MeterSubject {
	return newSubject(subjectSpec{capID: id, tenant: f.Tenant, requests: requestsOf(max)})
}

// leaseRequests leases n requests to subject.
func (f fixture) leaseRequests(t *testing.T, l meter.Leaser, subject limes.MeterSubject, n int64) meter.Lease {
	t.Helper()
	got, err := l.Lease(f.Ctx, meter.LeaseRequest{Subject: subject, Requests: n})
	expect.Must(t, err == nil, "Lease of %d requests: %v", n, err)
	return got
}

func (f fixture) bump(subject limes.MeterSubject) error {
	_, err := f.Usage.Bump(f.Ctx, meter.BumpRequest{Subject: subject})
	return err
}

// A lease of requests holds them on the child and the root at once; a bump
// under it counts on the child and the lease, and leaves the root as the
// lease left it; settled, the root counts what the lease made and holds
// nothing.
func checkRequestLeaseStopsBumpsAtTheLeaseholder(t *testing.T, f fixture) {
	l := f.leaser(t)
	c := f.newRequestChain(t)
	child := f.requestSubject(c.child, requestChild)
	got := f.leaseRequests(t, l, child, requestLeased)
	expect.That(t, got.Requests == requestLeased && got.RequestsSpent == 0 && got.Amount == 0, "Lease = %+v, want %d requests leased", got, requestLeased)
	for _, id := range []uuid.UUID{c.child, c.root} {
		u := f.usageOf(t, id)
		expect.That(t, u.ReservedRequests == requestLeased && u.RequestCount == 0, "%s once leased = %+v, want %d requests held", id, u, requestLeased)
	}
	r, err := f.Usage.Bump(f.Ctx, meter.BumpRequest{Subject: child})
	expect.Must(t, err == nil && r.RequestCount == 1, "Bump under the lease = %+v, %v; want the child's first", r, err)
	cu, ru := f.usageOf(t, c.child), f.usageOf(t, c.root)
	expect.That(t, cu.RequestCount == 1 && cu.ReservedRequests == requestLeased-1, "the child after a bump under its lease = %+v, want 1 made and %d held", cu, requestLeased-1)
	expect.That(t, ru.RequestCount == 0 && ru.ReservedRequests == requestLeased, "the root after a bump under the child's lease = %+v, want none made and the lease held", ru)
	open, err := l.GetLease(f.Ctx, c.child)
	expect.That(t, err == nil && open.RequestsSpent == 1 && open.RequestsRemaining() == requestLeased-1, "GetLease = %+v, %v; want 1 request made", open, err)
	settled, err := l.SettleLease(f.Ctx, meter.SettleLeaseRequest{Subject: child})
	expect.Must(t, err == nil && settled.RequestsSpent == 1, "SettleLease = %+v, %v; want 1 request made", settled, err)
	for name, u := range map[string]meter.Usage{"the child": f.usageOf(t, c.child), "the root": f.usageOf(t, c.root)} {
		expect.That(t, u.RequestCount == 1 && u.ReservedRequests == 0, "%s once the lease settled = %+v, want 1 request and nothing held", name, u)
	}
}

// A lease of requests is held to every request ceiling as that many bumps
// are: past the child's or the root's it is refused, and holds nothing.
func checkRequestLeaseIsHeldToEveryCeiling(t *testing.T, f fixture) {
	l := f.leaser(t)
	c := f.newRequestChain(t)
	// Past the root's, the child is given a ceiling of its own above it.
	for name, tc := range map[string]struct{ ceiling, n int64 }{
		"the child's": {requestChild, requestChild + 1},
		"the root's":  {requestRoot + 1, requestRoot + 1},
	} {
		_, err := l.Lease(f.Ctx, meter.LeaseRequest{Subject: f.requestSubject(c.child, tc.ceiling), Requests: tc.n})
		expect.That(t, errors.Is(err, limes.ErrRequestLimitExceeded), "a lease past %s ceiling: %v, want ErrRequestLimitExceeded", name, err)
	}
	for _, id := range []uuid.UUID{c.child, c.root} {
		expect.That(t, f.usageOf(t, id).ReservedRequests == 0, "%s holds %d requests after refused leases", id, f.usageOf(t, id).ReservedRequests)
	}
	_, err := l.Lease(f.Ctx, meter.LeaseRequest{Subject: f.requestSubject(c.child, requestChild), Requests: -1})
	expect.That(t, errors.Is(err, limes.ErrInvalidRequest), "a lease of negative requests: %v, want ErrInvalidRequest", err)
}

// The requests a lease holds count against the root for a capability that
// bumps through the whole chain beside it.
func checkRequestLeaseIsHeldAgainstASibling(t *testing.T, f fixture) {
	l := f.leaser(t)
	c := f.newRequestChain(t)
	sibling, err := f.NewCapability(CapabilitySpec{Parent: c.root, Requests: limes.RequestCeiling{Unlimited: true}, Budget: unlimitedBudget})
	expect.Must(t, err == nil, "sibling: %v", err)
	f.leaseRequests(t, l, f.requestSubject(c.child, requestChild), requestLeased)
	s := newSubject(subjectSpec{capID: sibling, tenant: f.Tenant})
	for i := range requestRoot - requestLeased {
		expect.Must(t, f.bump(s) == nil, "the sibling's bump %d within what the lease leaves", i)
	}
	err = f.bump(s)
	expect.That(t, errors.Is(err, limes.ErrRequestLimitExceeded), "a sibling's bump into the requests the lease holds: %v, want ErrRequestLimitExceeded", err)
}

// A bump past the requests the lease holds is refused with
// ErrLeaseRequestsExhausted, a kind of ErrRequestLimitExceeded, and counts
// nothing; topped up, the lease covers it.
func checkRequestLeaseIsExhaustedAndToppedUp(t *testing.T, f fixture) {
	l := f.leaser(t)
	c := f.newRequestChain(t)
	child := f.requestSubject(c.child, requestChild)
	f.leaseRequests(t, l, child, requestLeased)
	for range requestLeased {
		expect.Must(t, f.bump(child) == nil, "a bump within the lease")
	}
	err := f.bump(child)
	expect.That(t, errors.Is(err, meter.ErrLeaseRequestsExhausted) && errors.Is(err, limes.ErrRequestLimitExceeded), "a bump past the lease: %v, want ErrLeaseRequestsExhausted, an ErrRequestLimitExceeded", err)
	expect.That(t, f.usageOf(t, c.child).RequestCount == requestLeased, "the child made %d requests after a refused bump, want %d", f.usageOf(t, c.child).RequestCount, requestLeased)
	topped := f.leaseRequests(t, l, child, requestTopUp)
	expect.That(t, topped.Requests == requestLeased+requestTopUp && topped.RequestsSpent == requestLeased, "a top-up = %+v, want %d leased, %d made", topped, requestLeased+requestTopUp, requestLeased)
	expect.That(t, f.bump(child) == nil, "a bump once topped up")
}

// A lease covers only what it holds: one of budget alone covers no bump,
// one of requests alone no charge — each goes through the whole chain.
func checkLeaseCoversOnlyWhatItHolds(t *testing.T, f fixture) {
	l := f.leaser(t)
	child := f.childSubject()
	f.lease(t, l, child, leased)
	expect.Must(t, f.bump(child) == nil, "a bump under a lease of budget")
	expect.That(t, f.usageOf(t, f.root).RequestCount == 1, "the root counted %d requests of a bump under a lease of budget alone, want 1", f.usageOf(t, f.root).RequestCount)
	_, err := l.SettleLease(f.Ctx, meter.SettleLeaseRequest{Subject: child})
	expect.Must(t, err == nil, "settle: %v", err)

	f.leaseRequests(t, l, child, requestLeased)
	_, err = f.chargeOf(child, one)
	expect.Must(t, err == nil, "a charge under a lease of requests: %v", err)
	expect.That(t, f.usageOf(t, f.root).SpentAmount == one && f.tenantOf(t).SpentAmount == one, "a charge under a lease of requests alone reached neither the root nor the tenant")
}

// A capability delegated from the leaseholder bumps under its lease: its
// own count and the leaseholder's move, the lease's with them, and the
// root's does not.
func checkRequestLeaseCoversDescendants(t *testing.T, f fixture) {
	l := f.leaser(t)
	c := f.newRequestChain(t)
	f.leaseRequests(t, l, f.requestSubject(c.child, requestChild), requestLeased)
	grandchild, err := f.NewCapability(CapabilitySpec{Parent: c.child, Requests: limes.RequestCeiling{Unlimited: true}, Budget: unlimitedBudget})
	expect.Must(t, err == nil, "grandchild: %v", err)
	expect.Must(t, f.bump(newSubject(subjectSpec{capID: grandchild, tenant: f.Tenant})) == nil, "the grandchild's bump")
	expect.That(t, f.usageOf(t, grandchild).RequestCount == 1 && f.usageOf(t, c.child).RequestCount == 1, "the grandchild's bump under its parent's lease was not counted on both")
	expect.That(t, f.usageOf(t, c.root).RequestCount == 0, "the root counted a bump under its child's lease")
	open, err := l.GetLease(f.Ctx, c.child)
	expect.That(t, err == nil && open.RequestsSpent == 1, "GetLease = %+v, %v; want the grandchild's request", open, err)
}

// A bump that counts a Biscuit copy with limits of its own — whose quota
// counts on every ancestor — goes through the whole chain under a lease.
func checkRequestLeaseIsBypassedByCopies(t *testing.T, f fixture) {
	l := f.leaser(t)
	c := f.newRequestChain(t)
	f.leaseRequests(t, l, f.requestSubject(c.child, requestChild), requestLeased)
	copies := []limes.CopyCeiling{{RevocationID: []byte("leased-copy"), Requests: &limes.RequestCeiling{Unlimited: true}}}
	expect.Must(t, f.bump(newSubject(subjectSpec{capID: c.child, tenant: f.Tenant, requests: requestsOf(requestChild), copies: copies})) == nil, "a bump of a copy")
	expect.That(t, f.usageOf(t, c.root).RequestCount == 1, "a copy's bump under a lease did not reach the root")
	open, err := l.GetLease(f.Ctx, c.child)
	expect.That(t, err == nil && open.RequestsSpent == 0, "the lease after a copy's bump = %+v, %v; want none made under it", open, err)
}

// An expired lease covers no bump, which goes through the whole chain, and
// ReleaseExpired settles it, counting what it made on the root.
func checkRequestLeaseExpires(t *testing.T, f fixture) {
	l := f.leaser(t)
	if f.Advance == nil {
		t.Skip("Env.Advance not set")
	}
	const ttl = time.Minute
	c := f.newRequestChain(t)
	child := f.requestSubject(c.child, requestChild)
	_, err := l.Lease(f.Ctx, meter.LeaseRequest{Subject: child, Requests: requestLeased, TTL: ttl})
	expect.Must(t, err == nil, "Lease: %v", err)
	expect.Must(t, f.bump(child) == nil, "a bump under the lease")
	f.Advance(2 * ttl)
	expect.Must(t, f.bump(child) == nil, "a bump past the lease's end")
	expect.That(t, f.usageOf(t, c.root).RequestCount == 1, "the root counted %d past the lease's end, want 1", f.usageOf(t, c.root).RequestCount)
	res, err := f.Usage.ReleaseExpired(f.Ctx, meter.ReleaseExpiredRequest{})
	expect.Must(t, err == nil && res.SettledLeases == 1, "ReleaseExpired = %+v, %v; want the lease settled", res, err)
	r := f.usageOf(t, c.root)
	expect.That(t, r.RequestCount == 2 && r.ReservedRequests == 0, "the root once the expired lease settled = %+v, want both requests and nothing held", r)
}

// Children bumping under leases of their own at once, from one root, never
// make more requests than the root's ceiling between them; settled, the
// root counts exactly what they made and holds nothing.
func checkRequestLeaseConcurrency(t *testing.T, f fixture) {
	l := f.leaser(t)
	root, err := f.NewCapability(CapabilitySpec{Requests: limes.RequestCeiling{Max: requestRoot}, Budget: unlimitedBudget})
	expect.Must(t, err == nil, "root: %v", err)
	share := int64(requestRoot / leaseWorkers)
	var wg sync.WaitGroup
	made := make([]int64, leaseWorkers)
	subjects := make([]limes.MeterSubject, leaseWorkers)
	for i := range leaseWorkers {
		id, err := f.NewCapability(CapabilitySpec{Parent: root, Requests: limes.RequestCeiling{Unlimited: true}, Budget: unlimitedBudget})
		expect.Must(t, err == nil, "child %d: %v", i, err)
		subjects[i] = newSubject(subjectSpec{capID: id, tenant: f.Tenant})
		f.leaseRequests(t, l, subjects[i], share)
	}
	for i := range leaseWorkers {
		wg.Go(func() {
			for f.bump(subjects[i]) == nil {
				made[i]++
			}
		})
	}
	wg.Wait()
	var total int64
	for i, s := range subjects {
		total += made[i]
		_, err := l.SettleLease(f.Ctx, meter.SettleLeaseRequest{Subject: s})
		expect.Must(t, err == nil, "settle %d: %v", i, err)
	}
	r := f.usageOf(t, root)
	expect.That(t, total == share*leaseWorkers, "the children made %d requests under leases of %d each, want every one", total, share)
	expect.That(t, r.RequestCount == total && r.ReservedRequests == 0, "the root once every lease settled = %+v, want %d requests and nothing held", r, total)
}

// A lease of more requests than a counter can hold is refused, after a
// request made as before it: its total cannot be counted, so no ceiling
// may admit it, and nothing is held.
func checkRequestLeaseThatCannotBeCountedIsRefused(t *testing.T, f fixture) {
	l := f.leaser(t)
	c := f.newRequestChain(t)
	child := f.requestSubject(c.child, requestChild)
	expect.Must(t, f.bump(child) == nil, "first bump")
	_, err := l.Lease(f.Ctx, meter.LeaseRequest{Subject: child, Requests: math.MaxInt64})
	expect.That(t, errors.Is(err, limes.ErrRequestLimitExceeded), "Lease of MaxInt64 requests after one made: %v, want ErrRequestLimitExceeded", err)
	for _, id := range []uuid.UUID{c.child, c.root} {
		u := f.usageOf(t, id)
		expect.That(t, u.RequestCount == 1 && u.ReservedRequests == 0, "%s after the refused lease = %+v, want 1 made and none held", id, u)
	}
}
