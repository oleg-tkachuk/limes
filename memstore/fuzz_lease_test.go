package memstore

import (
	"context"
	"errors"
	"testing"
	"time"
	"uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/meter"
)

// The tree FuzzLeases meters: a root, two children and a grandchild under
// the first, each with a budget and a request ceiling of its own, under a
// tenant budget below the sum of the children's. Indexes into leaseTree.
const (
	leaseRoot = iota
	leaseChildA
	leaseChildB
	leaseGrandchild
	leaseNodes
)

// leaseNode is one capability of the tree: its parent (an index, or -1 for
// the root) and its ceilings, in units and requests.
type leaseNode struct {
	parent          int
	budget, maxReqs int64
}

var leaseTree = [leaseNodes]leaseNode{
	leaseRoot:       {parent: -1, budget: 100, maxReqs: 40},
	leaseChildA:     {parent: leaseRoot, budget: 60, maxReqs: 25},
	leaseChildB:     {parent: leaseRoot, budget: 60, maxReqs: 25},
	leaseGrandchild: {parent: leaseChildA, budget: 30, maxReqs: 15},
}

// leaseTenantBudget is the tenant's ceiling, in units: below the children's
// sum, so the tenant's aggregate refuses too.
const leaseTenantBudget = 90

// The steps of a FuzzLeases script, a byte a step: the low bits pick the
// step, the next two the capability, the top three an amount of 1 to 8.
const (
	leaseStepCharge = iota
	leaseStepBump
	leaseStepLease
	leaseStepLeaseRequests
	leaseStepSettle
	leaseStepAdvance
	leaseStepReleaseExpired
	leaseStepChargeTwice
)

const (
	leaseStepBits   = 3
	leaseStepMask   = 1<<leaseStepBits - 1
	leaseNodeBits   = 2
	leaseNodeMask   = 1<<leaseNodeBits - 1
	leaseAmountMask = 1<<(8-leaseStepBits-leaseNodeBits) - 1
	// leaseFuzzMaxSteps bounds one script; with leaseFuzzAdvance a step
	// it stays well inside the tree's leaseFuzzLife.
	leaseFuzzMaxSteps = 256
	// leaseFuzzTTL is how long a lease lasts, leaseFuzzAdvance how far
	// leaseStepAdvance moves the clock: a few steps end a lease.
	leaseFuzzTTL     = 3 * time.Minute
	leaseFuzzAdvance = time.Minute
	leaseFuzzLife    = 24 * time.Hour
)

// FuzzLeases runs a script of charges, bumps, leases, settlements and
// expiries on a tree of capabilities and holds the store, after every step,
// to ADR-0017's promise that ceilings stay hard: no capability and not the
// tenant ever has more spent and reserved than its ceiling, nor more
// requests made and reserved than its request ceiling. Once every lease is
// settled, each capability has spent exactly what was charged to it and
// below it, and made exactly the requests bumped: no lease settled twice,
// none lost. A refusal is any kind of a ceiling or lease error; any other
// error fails.
func FuzzLeases(f *testing.F) {
	step := func(s, node, amount int) byte {
		return byte((amount-1)<<(leaseStepBits+leaseNodeBits) | node<<leaseStepBits | s)
	}
	// A lease of the grandchild, charges under it and past it, a settle.
	f.Add([]byte{step(leaseStepLease, leaseGrandchild, 8), step(leaseStepCharge, leaseGrandchild, 5), step(leaseStepCharge, leaseGrandchild, 5),
		step(leaseStepSettle, leaseGrandchild, 1), step(leaseStepCharge, leaseChildB, 8)})
	// Leases of both children, a sibling's charges against the reservation,
	// expiry and housekeeping.
	f.Add([]byte{step(leaseStepLease, leaseChildA, 8), step(leaseStepLease, leaseChildB, 8), step(leaseStepCharge, leaseRoot, 8),
		step(leaseStepAdvance, 0, 1), step(leaseStepAdvance, 0, 1), step(leaseStepAdvance, 0, 1), step(leaseStepAdvance, 0, 1), step(leaseStepReleaseExpired, 0, 1)})
	// Request leases, bumps under them and beside them, a nested lease.
	f.Add([]byte{step(leaseStepBump, leaseChildA, 1), step(leaseStepLeaseRequests, leaseChildA, 8), step(leaseStepBump, leaseGrandchild, 1),
		step(leaseStepLease, leaseGrandchild, 2), step(leaseStepBump, leaseChildB, 1), step(leaseStepChargeTwice, leaseGrandchild, 3)})
	f.Fuzz(func(t *testing.T, script []byte) {
		if len(script) > leaseFuzzMaxSteps {
			script = script[:leaseFuzzMaxSteps]
		}
		ctx := context.Background()
		now := time.Unix(1_800_000_000, 0)
		u := mustUsage(t, UsageStoreConfig{Records: newTestStore(), Now: func() time.Time { return now }})
		tenant := uuid.New()
		if _, err := u.SetTenantBudget(ctx, meter.SetTenantBudgetRequest{TenantID: tenant, Ceiling: limes.BudgetCeiling{Max: leaseTenantBudget * unit, UnitCode: defaultTestUnit}}); err != nil {
			t.Fatal(err)
		}
		subjects, ids := recordLeaseTree(ctx, t, u, tenant, now.Add(leaseFuzzLife))
		var charged, bumped [leaseNodes]int64
		for i, b := range script {
			node := int(b>>leaseStepBits) & leaseNodeMask
			amount := int64(b>>(leaseStepBits+leaseNodeBits))&leaseAmountMask + 1
			subject := subjects[node]
			var err error
			switch int(b & leaseStepMask) {
			case leaseStepCharge, leaseStepChargeTwice:
				req := meter.ChargeRequest{Subject: subject, Amount: limes.Nanos(amount) * unit, ExternalRef: uuid.New().String()}
				if _, err = u.Charge(ctx, req, nil); err == nil {
					charged[node] += amount
					if b&leaseStepMask == leaseStepChargeTwice {
						// A replay moves nothing.
						if r, err := u.Charge(ctx, req, nil); err != nil || !r.Replayed {
							t.Fatalf("step %d: replayed charge = %+v, %v; want a replay", i, r, err)
						}
					}
				}
			case leaseStepBump:
				if _, err = u.Bump(ctx, meter.BumpRequest{Subject: subject}); err == nil {
					bumped[node]++
				}
			case leaseStepLease:
				_, err = u.Lease(ctx, meter.LeaseRequest{Subject: subject, Amount: limes.Nanos(amount) * unit, TTL: leaseFuzzTTL})
			case leaseStepLeaseRequests:
				_, err = u.Lease(ctx, meter.LeaseRequest{Subject: subject, Requests: amount, TTL: leaseFuzzTTL})
			case leaseStepSettle:
				_, err = u.SettleLease(ctx, meter.SettleLeaseRequest{Subject: subject})
			case leaseStepAdvance:
				now = now.Add(leaseFuzzAdvance)
			case leaseStepReleaseExpired:
				_, err = u.ReleaseExpired(ctx, meter.ReleaseExpiredRequest{})
			}
			if err != nil && !isLeaseRefusal(err) {
				t.Fatalf("step %d: %v", i, err)
			}
			checkLeaseCeilings(ctx, t, u, tenant, ids, i)
		}
		for node := range leaseNodes {
			if _, err := u.SettleLease(ctx, meter.SettleLeaseRequest{Subject: subjects[node]}); err != nil && !errors.Is(err, meter.ErrLeaseNotFound) {
				t.Fatalf("settle %d: %v", node, err)
			}
		}
		for node := range leaseNodes {
			got := usageOf(ctx, t, u, ids[node])
			spent, made := subtree(charged, node), subtree(bumped, node)
			if got.SpentAmount != limes.Nanos(spent)*unit || got.ReservedAmount != 0 || got.RequestCount != made || got.ReservedRequests != 0 {
				t.Fatalf("capability %d settled: %+v; want %d spent, %d requests made, nothing reserved", node, got, spent, made)
			}
		}
	})
}

// recordLeaseTree records leaseTree under tenant, each capability living
// until expires, and returns their subjects and ids.
func recordLeaseTree(ctx context.Context, t *testing.T, u *UsageStore, tenant uuid.UUID, expires time.Time) ([leaseNodes]limes.MeterSubject, [leaseNodes]uuid.UUID) {
	t.Helper()
	var subjects [leaseNodes]limes.MeterSubject
	var ids [leaseNodes]uuid.UUID
	for i, n := range leaseTree {
		ids[i] = uuid.New()
		requests := limes.RequestCeiling{Max: n.maxReqs}
		budget := limes.BudgetCeiling{Max: limes.Nanos(n.budget) * unit, UnitCode: defaultTestUnit}
		c := limes.Capability{
			ID: ids[i], ExpiresAt: expires,
			Subject: limes.Principal{Type: limes.PrincipalTypeAgent, TenantID: tenant, Subject: "agent"},
			Caveats: limes.Caveats{Ops: []limes.Op{limes.OpGet}, Requests: requests, Budget: budget},
		}
		if n.parent >= 0 {
			c.ParentID = ids[n.parent]
		}
		if err := u.records.Insert(ctx, limes.InsertRequest{Capability: c, IssuedBy: limes.Principal{Subject: "issuer"}}); err != nil {
			t.Fatal(err)
		}
		subjects[i] = newSubject(subjectSpec{capID: ids[i], tenant: tenant, requests: &requests, budget: &budget})
	}
	return subjects, ids
}

// isLeaseRefusal reports whether err is a call refused within the rules: a
// ceiling, a lease's, or a lease asked of a capability that may not hold one.
func isLeaseRefusal(err error) bool {
	for _, refusal := range []error{limes.ErrBudgetExceeded, limes.ErrRequestLimitExceeded, limes.ErrTenantBudgetExceeded, meter.ErrLeaseNested, meter.ErrLeaseNotFound} {
		if errors.Is(err, refusal) {
			return true
		}
	}
	return false
}

// checkLeaseCeilings fails unless every capability of the tree and the
// tenant are within their ceilings, counting what is reserved.
func checkLeaseCeilings(ctx context.Context, t *testing.T, u *UsageStore, tenant uuid.UUID, ids [leaseNodes]uuid.UUID, step int) {
	t.Helper()
	for node, n := range leaseTree {
		got := usageOf(ctx, t, u, ids[node])
		if got.SpentAmount+got.ReservedAmount > limes.Nanos(n.budget)*unit || got.RequestCount+got.ReservedRequests > n.maxReqs {
			t.Fatalf("step %d: capability %d past its ceilings: %+v", step, node, got)
		}
	}
	b, err := u.GetTenantBudget(ctx, tenant)
	if err != nil {
		t.Fatal(err)
	}
	if b.SpentAmount+b.ReservedAmount > leaseTenantBudget*unit {
		t.Fatalf("step %d: tenant past its ceiling: %+v", step, b)
	}
}

// usageOf is a capability's counters, zero before it has any.
func usageOf(ctx context.Context, t *testing.T, u *UsageStore, id uuid.UUID) meter.Usage {
	t.Helper()
	got, err := u.GetUsage(ctx, id)
	if errors.Is(err, limes.ErrNotFound) {
		return meter.Usage{}
	}
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// subtree is what per holds of node and every capability under it.
func subtree(per [leaseNodes]int64, node int) int64 {
	sum := per[node]
	for child, n := range leaseTree {
		if n.parent == node {
			sum += subtree(per, child)
		}
	}
	return sum
}
