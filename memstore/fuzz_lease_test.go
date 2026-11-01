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

// The steps of a FuzzLeases script. A step is two bytes: the first picks
// the step, modulo leaseSteps; the second's low bits the capability and its
// next three an amount of 1 to 8.
const (
	leaseStepCharge = iota
	leaseStepBump
	leaseStepLease
	leaseStepLeaseRequests
	leaseStepSettle
	leaseStepAdvance
	leaseStepReleaseExpired
	leaseStepChargeTwice
	// leaseStepReset closes the tenant's accounting period.
	leaseStepReset
	// leaseStepDeleteUsage drops a capability's counters, restarting them.
	leaseStepDeleteUsage
	// leaseStepRefund refunds what remains of a charge: the amount picks
	// which, counting back from the latest.
	leaseStepRefund
	// leaseStepAudit settles every lease and holds each counter to the
	// model's, so a drift is caught at the step that made it.
	leaseStepAudit
	leaseSteps
)

const (
	leaseStepSize   = 2
	leaseNodeBits   = 2
	leaseNodeMask   = 1<<leaseNodeBits - 1
	leaseAmountBits = 3
	leaseAmountMask = 1<<leaseAmountBits - 1
	// leaseFuzzMaxSteps bounds one script; with leaseFuzzAdvance a step
	// it stays well inside the tree's leaseFuzzLife.
	leaseFuzzMaxSteps = 256
	// leaseFuzzTTL is how long a lease lasts, leaseFuzzAdvance how far
	// leaseStepAdvance moves the clock: a few steps end a lease.
	leaseFuzzTTL     = 3 * time.Minute
	leaseFuzzAdvance = time.Minute
	leaseFuzzLife    = 24 * time.Hour
)

// leaseCharge is a charge the model saw taken: its node, what is left of it
// to refund, and the counters it debited — each node's epoch and the
// tenant's period as they were when it was taken.
type leaseCharge struct {
	id     uuid.UUID
	node   int
	left   int64
	epochs [leaseNodes]int
	period int
}

// leaseModel is what the store must count, from the meter's contract
// alone: each capability's spend and requests since its counters last
// restarted (DeleteUsage), its own and every descendant's, and the
// tenant's spend in the current period. A refund gives back to the
// counters the charge debited, and only while they are the ones it
// debited: a counter restarted, or a period closed, since never counted it.
type leaseModel struct {
	spent, made [leaseNodes]int64
	epochs      [leaseNodes]int
	tenantSpent int64
	period      int
	charges     []leaseCharge
}

// chain is node and its ancestors, nearest first.
func chain(node int) []int {
	var nodes []int
	for n := node; n >= 0; n = leaseTree[n].parent {
		nodes = append(nodes, n)
	}
	return nodes
}

func (m *leaseModel) charge(id uuid.UUID, node int, amount int64) {
	for _, n := range chain(node) {
		m.spent[n] += amount
	}
	m.tenantSpent += amount
	m.charges = append(m.charges, leaseCharge{id: id, node: node, left: amount, epochs: m.epochs, period: m.period})
}

func (m *leaseModel) bump(node int) {
	for _, n := range chain(node) {
		m.made[n]++
	}
}

func (m *leaseModel) reset() {
	m.tenantSpent = 0
	m.period++
}

func (m *leaseModel) deleteUsage(node int) {
	m.spent[node], m.made[node] = 0, 0
	m.epochs[node]++
}

// refund gives back what is left of c to the counters that still hold it.
func (m *leaseModel) refund(c *leaseCharge) {
	for _, n := range chain(c.node) {
		if m.epochs[n] == c.epochs[n] {
			m.spent[n] -= c.left
		}
	}
	if m.period == c.period {
		m.tenantSpent -= c.left
	}
	c.left = 0
}

// FuzzLeases runs a script of charges, bumps, leases, settlements, expiries,
// period resets, refunds and dropped counters on a tree of capabilities and
// holds the store, after every step, to ADR-0017's promise that ceilings
// stay hard: no capability and not the tenant ever has more spent and
// reserved than its ceiling, nor more requests made and reserved than its
// request ceiling. Once every lease is settled, each counter holds exactly
// what leaseModel says it must: no lease settled twice, none lost, no
// refund given to a counter that never counted the charge. A refusal is any
// kind of a ceiling or lease error; any other error fails.
func FuzzLeases(f *testing.F) {
	step := func(s, node, amount int) []byte {
		return []byte{byte(s), byte((amount-1)<<leaseNodeBits | node)}
	}
	script := func(steps ...[]byte) []byte {
		var b []byte
		for _, s := range steps {
			b = append(b, s...)
		}
		return b
	}
	// A lease of the grandchild, charges under it and past it, a settle.
	f.Add(script(step(leaseStepLease, leaseGrandchild, 8), step(leaseStepCharge, leaseGrandchild, 5), step(leaseStepCharge, leaseGrandchild, 5),
		step(leaseStepSettle, leaseGrandchild, 1), step(leaseStepCharge, leaseChildB, 8)))
	// Leases of both children, a sibling's charges against the reservation,
	// expiry and housekeeping.
	f.Add(script(step(leaseStepLease, leaseChildA, 8), step(leaseStepLease, leaseChildB, 8), step(leaseStepCharge, leaseRoot, 8),
		step(leaseStepAdvance, 0, 1), step(leaseStepAdvance, 0, 1), step(leaseStepAdvance, 0, 1), step(leaseStepAdvance, 0, 1), step(leaseStepReleaseExpired, 0, 1)))
	// Request leases, bumps under them and beside them, a nested lease.
	f.Add(script(step(leaseStepBump, leaseChildA, 1), step(leaseStepLeaseRequests, leaseChildA, 8), step(leaseStepBump, leaseGrandchild, 1),
		step(leaseStepLease, leaseGrandchild, 2), step(leaseStepBump, leaseChildB, 1), step(leaseStepChargeTwice, leaseGrandchild, 3)))
	// A charge under a lease, a reset that settles it into the closed
	// period, a refund of it, and a charge of the new one.
	f.Add(script(step(leaseStepLease, leaseChildB, 8), step(leaseStepCharge, leaseChildB, 6), step(leaseStepReset, 0, 1),
		step(leaseStepRefund, leaseChildB, 1), step(leaseStepCharge, leaseChildA, 7), step(leaseStepAudit, 0, 1)))
	// A charge of the grandchild, its parent's counters dropped and charged
	// again, then the first charge refunded.
	f.Add(script(step(leaseStepCharge, leaseGrandchild, 8), step(leaseStepDeleteUsage, leaseChildA, 1), step(leaseStepCharge, leaseChildA, 4),
		step(leaseStepRefund, leaseGrandchild, 2), step(leaseStepAudit, 0, 1)))
	// A lease of a child, its root's counters dropped, a charge of the
	// child: the root counts it, restarted.
	f.Add(script(step(leaseStepLease, leaseChildB, 5), step(leaseStepDeleteUsage, leaseRoot, 1), step(leaseStepCharge, leaseChildB, 5)))
	f.Fuzz(func(t *testing.T, script []byte) {
		if len(script) > leaseFuzzMaxSteps*leaseStepSize {
			script = script[:leaseFuzzMaxSteps*leaseStepSize]
		}
		ctx := context.Background()
		now := time.Unix(1_800_000_000, 0)
		u := mustUsage(t, UsageStoreConfig{Records: newTestStore(), Now: func() time.Time { return now }})
		tenant := uuid.New()
		ceiling := limes.BudgetCeiling{Max: leaseTenantBudget * unit, UnitCode: defaultTestUnit}
		budget, err := u.SetTenantBudget(ctx, meter.SetTenantBudgetRequest{TenantID: tenant, Ceiling: ceiling})
		if err != nil {
			t.Fatal(err)
		}
		subjects, ids := recordLeaseTree(ctx, t, u, tenant, now.Add(leaseFuzzLife))
		var m leaseModel
		for i := 0; i+leaseStepSize <= len(script); i += leaseStepSize {
			at := i / leaseStepSize
			arg := script[i+1]
			node := int(arg) & leaseNodeMask
			amount := int64(arg>>leaseNodeBits)&leaseAmountMask + 1
			subject := subjects[node]
			var err error
			switch s := int(script[i]) % leaseSteps; s {
			case leaseStepCharge, leaseStepChargeTwice:
				req := meter.ChargeRequest{Subject: subject, Amount: limes.Nanos(amount) * unit, ExternalRef: uuid.New().String()}
				var r meter.ChargeReceipt
				if r, err = u.Charge(ctx, req, nil); err == nil {
					m.charge(r.ChargeID, node, amount)
					if s == leaseStepChargeTwice {
						// A replay moves nothing.
						if r, err := u.Charge(ctx, req, nil); err != nil || !r.Replayed {
							t.Fatalf("step %d: replayed charge = %+v, %v; want a replay", at, r, err)
						}
					}
				}
			case leaseStepBump:
				if _, err = u.Bump(ctx, meter.BumpRequest{Subject: subject}); err == nil {
					m.bump(node)
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
			case leaseStepReset:
				if budget, err = u.SetTenantBudget(ctx, meter.SetTenantBudgetRequest{
					TenantID: tenant, Ceiling: ceiling, ResetSpend: true, ExpectedVersion: budget.ResourceVersion,
				}); err != nil {
					t.Fatalf("step %d: reset: %v", at, err)
				}
				m.reset()
			case leaseStepDeleteUsage:
				if err = u.DeleteUsage(ctx, meter.DeleteUsageRequest{CapabilityID: ids[node]}); err != nil {
					t.Fatalf("step %d: DeleteUsage: %v", at, err)
				}
				m.deleteUsage(node)
			case leaseStepRefund:
				if len(m.charges) == 0 {
					break
				}
				c := &m.charges[len(m.charges)-1-int(amount-1)%len(m.charges)]
				r, err := u.Refund(ctx, meter.RefundRequest{ChargeID: c.id, Remaining: true})
				if err != nil || r.Amount != limes.Nanos(c.left)*unit {
					t.Fatalf("step %d: refund of %d left = %+v, %v", at, c.left, r, err)
				}
				m.refund(c)
			case leaseStepAudit:
				auditLeases(ctx, t, u, tenant, subjects, ids, &m, at)
			}
			if err != nil && !isLeaseRefusal(err) {
				t.Fatalf("step %d: %v", at, err)
			}
			checkLeaseCeilings(ctx, t, u, tenant, ids, at)
		}
		auditLeases(ctx, t, u, tenant, subjects, ids, &m, len(script)/leaseStepSize)
	})
}

// auditLeases settles every lease of the tree and fails unless each
// capability's counters and the tenant's spend are the model's, with
// nothing reserved.
func auditLeases(ctx context.Context, t *testing.T, u *UsageStore, tenant uuid.UUID, subjects [leaseNodes]limes.MeterSubject, ids [leaseNodes]uuid.UUID, m *leaseModel, step int) {
	t.Helper()
	for node := range leaseNodes {
		if _, err := u.SettleLease(ctx, meter.SettleLeaseRequest{Subject: subjects[node]}); err != nil && !errors.Is(err, meter.ErrLeaseNotFound) {
			t.Fatalf("step %d: settle %d: %v", step, node, err)
		}
	}
	for node := range leaseNodes {
		got := usageOf(ctx, t, u, ids[node])
		if got.SpentAmount != limes.Nanos(m.spent[node])*unit || got.ReservedAmount != 0 || got.RequestCount != m.made[node] || got.ReservedRequests != 0 {
			t.Fatalf("step %d: capability %d settled: %+v; want %d spent, %d requests made, nothing reserved", step, node, got, m.spent[node], m.made[node])
		}
	}
	b, err := u.GetTenantBudget(ctx, tenant)
	if err != nil {
		t.Fatal(err)
	}
	if b.SpentAmount != limes.Nanos(m.tenantSpent)*unit || b.ReservedAmount != 0 {
		t.Fatalf("step %d: tenant settled: %+v; want %d spent in the period, nothing reserved", step, b, m.tenantSpent)
	}
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
