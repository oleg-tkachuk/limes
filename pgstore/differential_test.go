//go:build differential

package pgstore

// A differential fuzz of the meter: one script of charges, bumps, leases,
// holds, settlements, resets, refunds, dropped counters and revocations
// runs on this store and on memstore side by side, and after every step the
// two must agree — on each call's outcome and on every counter, lease and
// revocation. meter.Meter and limes.Store are one contract with two
// implementations, so a difference is a bug in one of them, or a gap in the
// contract that lets them differ.
//
// Run with the differential tag (task test:pgstore:differential); it needs
// a running Docker, so the weekly fuzz workflow does not run it.

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
	"uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/internal/link"
	"github.com/oleg-tkachuk/limes/memstore"
	"github.com/oleg-tkachuk/limes/meter"
)

// The tree both stores meter: a root, two children and a grandchild under
// the first, each with ceilings of its own, under a tenant ceiling below
// the children's sum. Indexes into diffTree.
const (
	diffRoot = iota
	diffChildA
	diffChildB
	diffGrandchild
	diffNodes
)

// diffNode is one capability of the tree: its parent (an index, or -1 for
// the root) and its ceilings, in units and requests.
type diffNode struct {
	parent          int
	budget, maxReqs int64
}

var diffTree = [diffNodes]diffNode{
	diffRoot:       {parent: -1, budget: 100, maxReqs: 40},
	diffChildA:     {parent: diffRoot, budget: 60, maxReqs: 25},
	diffChildB:     {parent: diffRoot, budget: 60, maxReqs: 25},
	diffGrandchild: {parent: diffChildA, budget: 30, maxReqs: 15},
}

// diffTenantBudget is the tenant's ceiling, in units.
const diffTenantBudget = 90

// The steps of a script. A step is two bytes: the first picks the step,
// modulo diffSteps; the second's low bits the capability, its next three an
// amount of 1 to 8.
const (
	diffStepCharge = iota
	diffStepBump
	diffStepLease
	diffStepLeaseRequests
	diffStepSettleLease
	diffStepAdvance
	diffStepReleaseExpired
	diffStepReset
	diffStepDeleteUsage
	// diffStepRefund refunds what remains of a charge: the amount picks
	// which, counting back from the latest.
	diffStepRefund
	diffStepReserve
	// diffStepSettle settles a hold for the amount: the capability picks
	// which, counting back from the latest.
	diffStepSettle
	diffStepRevoke
	diffSteps
)

const (
	diffStepSize   = 2
	diffNodeBits   = 2
	diffNodeMask   = 1<<diffNodeBits - 1
	diffAmountBits = 3
	diffAmountMask = 1<<diffAmountBits - 1
	// diffMaxSteps bounds one script: each step is a few round trips.
	diffMaxSteps = 64
	// diffUnit is one unit in Nanos.
	diffUnit = limes.NanosPerUnit
	// diffTTL is how long a lease or a hold lasts, diffAdvance how far
	// diffStepAdvance moves the clock; diffLife how long the tree lives.
	diffTTL     = 3 * time.Minute
	diffAdvance = time.Minute
	diffLife    = 24 * time.Hour
)

// diffSide is one store under the script: its records and its meter, the
// context it is called with, and the ids its charges and holds were given.
type diffSide struct {
	name    string
	records limes.Store
	usage   interface {
		meter.UsageStore
		meter.Leaser
	}
	ctx     context.Context
	charges []uuid.UUID
	holds   []uuid.UUID
}

// diffSentinels are the outcomes the two stores must agree on, by name.
var diffSentinels = []struct {
	name string
	err  error
}{
	{"budget", limes.ErrBudgetExceeded},
	{"requests", limes.ErrRequestLimitExceeded},
	{"tenant budget", limes.ErrTenantBudgetExceeded},
	{"lease nested", meter.ErrLeaseNested},
	{"lease not found", meter.ErrLeaseNotFound},
	{"reservation not found", meter.ErrReservationNotFound},
	{"charge not found", meter.ErrChargeNotFound},
	{"not found", limes.ErrNotFound},
	{"expired", limes.ErrExpired},
	{"conflict", limes.ErrConflict},
	{"invalid request", limes.ErrInvalidRequest},
}

// outcome is err as the comparison reads it: none, a sentinel's name, or
// the error itself, which matches nothing on the other side.
func outcome(err error) string {
	if err == nil {
		return "ok"
	}
	for _, s := range diffSentinels {
		if errors.Is(err, s.err) {
			return s.name
		}
	}
	return "unexpected: " + err.Error()
}

func FuzzDifferential(f *testing.F) {
	step := func(s, node, amount int) []byte {
		return []byte{byte(s), byte((amount-1)<<diffNodeBits | node)}
	}
	script := func(steps ...[]byte) []byte {
		var b []byte
		for _, s := range steps {
			b = append(b, s...)
		}
		return b
	}
	f.Add(script(step(diffStepLease, diffGrandchild, 8), step(diffStepCharge, diffGrandchild, 5), step(diffStepCharge, diffGrandchild, 5),
		step(diffStepSettleLease, diffGrandchild, 1), step(diffStepRefund, 0, 1), step(diffStepReset, 0, 1), step(diffStepCharge, diffChildB, 8)))
	f.Add(script(step(diffStepReserve, diffChildA, 6), step(diffStepDeleteUsage, diffRoot, 1), step(diffStepSettle, 0, 7),
		step(diffStepBump, diffChildA, 1), step(diffStepLeaseRequests, diffChildB, 4), step(diffStepAdvance, 0, 1),
		step(diffStepAdvance, 0, 1), step(diffStepAdvance, 0, 1), step(diffStepReleaseExpired, 0, 1)))
	f.Add(script(step(diffStepCharge, diffGrandchild, 8), step(diffStepRevoke, diffChildA, 1), step(diffStepCharge, diffGrandchild, 2),
		step(diffStepLease, diffChildB, 3), step(diffStepDeleteUsage, diffChildB, 1), step(diffStepRefund, 0, 1)))
	f.Fuzz(func(t *testing.T, script []byte) {
		if len(script) > diffMaxSteps*diffStepSize {
			script = script[:diffMaxSteps*diffStepSize]
		}
		clock := newFakeClock()
		tenant := newTenant(t)
		sides := [2]*diffSide{pgSide(t, tenant, clock), memSide(t, clock)}
		subjects, ids := diffRecordTree(t, sides, tenant, clock.Now().Add(diffLife))
		for _, side := range sides {
			if _, err := side.usage.SetTenantBudget(side.ctx, meter.SetTenantBudgetRequest{
				TenantID: tenant, Ceiling: limes.BudgetCeiling{Max: diffTenantBudget * diffUnit, UnitCode: testUnit},
			}); err != nil {
				t.Fatalf("%s: tenant budget: %v", side.name, err)
			}
		}
		for i := 0; i+diffStepSize <= len(script); i += diffStepSize {
			at, arg := i/diffStepSize, int(script[i+1])
			node := arg & diffNodeMask
			amount := int64(arg>>diffNodeBits)&diffAmountMask + 1
			s := int(script[i]) % diffSteps
			if s == diffStepAdvance {
				clock.Advance(diffAdvance)
				continue
			}
			var outcomes [2]string
			for j, side := range sides {
				outcomes[j] = outcome(diffApply(side, s, subjects[node], ids[node], tenant, amount))
			}
			if outcomes[0] != outcomes[1] {
				t.Fatalf("step %d (%d on %d, %d): %s %s, %s %s", at, s, node, amount, sides[0].name, outcomes[0], sides[1].name, outcomes[1])
			}
			diffCompare(t, sides, ids, tenant, at)
		}
	})
}

// pgSide is this store over app, for tenant, on clock.
func pgSide(t *testing.T, tenant uuid.UUID, clock *fakeClock) *diffSide {
	t.Helper()
	records, err := NewStore(Config{DB: app, CheckTenant: checkTenant, Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	usage, err := NewUsageStore(Config{DB: app, Leases: true, Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	return &diffSide{name: "pgstore", records: records, usage: usage, ctx: WithTenant(context.Background(), tenant)}
}

// memSide is a memstore on clock.
func memSide(t *testing.T, clock *fakeClock) *diffSide {
	t.Helper()
	records, err := memstore.NewStore(memstore.StoreConfig{Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	usage, err := memstore.NewUsageStore(memstore.UsageStoreConfig{Records: records, Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	return &diffSide{name: "memstore", records: records, usage: usage, ctx: context.Background()}
}

// diffRecordTree records diffTree in every side under tenant, each
// capability living until expires, and returns their subjects and ids.
func diffRecordTree(t *testing.T, sides [2]*diffSide, tenant uuid.UUID, expires time.Time) ([diffNodes]limes.MeterSubject, [diffNodes]uuid.UUID) {
	t.Helper()
	var subjects [diffNodes]limes.MeterSubject
	var ids [diffNodes]uuid.UUID
	expires = expires.Truncate(time.Second)
	for i, n := range diffTree {
		ids[i] = uuid.New()
		requests := limes.RequestCeiling{Max: n.maxReqs}
		budget := limes.BudgetCeiling{Max: limes.Nanos(n.budget) * diffUnit, UnitCode: testUnit}
		c := limes.Capability{
			ID: ids[i], ExpiresAt: expires,
			Subject: limes.Principal{Type: limes.PrincipalTypeAgent, TenantID: tenant, Subject: "agent"},
			Caveats: limes.Caveats{Ops: []limes.Op{limes.OpGet}, Requests: requests, Budget: budget},
		}
		if n.parent >= 0 {
			c.ParentID = ids[n.parent]
		}
		for _, side := range sides {
			if err := side.records.Insert(side.ctx, limes.InsertRequest{Capability: c, IssuedBy: limes.Principal{Subject: "issuer"}}); err != nil {
				t.Fatalf("%s: record %d: %v", side.name, i, err)
			}
		}
		subjects[i] = link.NewMeterSubject(link.MeterSubjectFields{
			CapabilityID: ids[i], TenantID: tenant, Requests: requests, Budget: budget,
		}).(limes.MeterSubject)
	}
	return subjects, ids
}

// diffApply runs step s on side and returns its error.
func diffApply(side *diffSide, s int, subject limes.MeterSubject, id, tenant uuid.UUID, amount int64) error {
	ctx, u := side.ctx, side.usage
	nanos := limes.Nanos(amount) * diffUnit
	switch s {
	case diffStepCharge:
		r, err := u.Charge(ctx, meter.ChargeRequest{Subject: subject, Amount: nanos}, nil)
		if err == nil {
			side.charges = append(side.charges, r.ChargeID)
		}
		return err
	case diffStepBump:
		_, err := u.Bump(ctx, meter.BumpRequest{Subject: subject})
		return err
	case diffStepLease:
		_, err := u.Lease(ctx, meter.LeaseRequest{Subject: subject, Amount: nanos, TTL: diffTTL})
		return err
	case diffStepLeaseRequests:
		_, err := u.Lease(ctx, meter.LeaseRequest{Subject: subject, Requests: amount, TTL: diffTTL})
		return err
	case diffStepSettleLease:
		_, err := u.SettleLease(ctx, meter.SettleLeaseRequest{Subject: subject})
		return err
	case diffStepReleaseExpired:
		_, err := u.ReleaseExpired(ctx, meter.ReleaseExpiredRequest{})
		return err
	case diffStepReset:
		b, err := u.GetTenantBudget(ctx, tenant)
		if err != nil {
			return err
		}
		_, err = u.SetTenantBudget(ctx, meter.SetTenantBudgetRequest{
			TenantID: tenant, Ceiling: b.Ceiling, ResetSpend: true, ExpectedVersion: b.ResourceVersion,
		})
		return err
	case diffStepDeleteUsage:
		return u.DeleteUsage(ctx, meter.DeleteUsageRequest{CapabilityID: id})
	case diffStepRefund:
		if len(side.charges) == 0 {
			return nil
		}
		c := side.charges[len(side.charges)-1-int(amount-1)%len(side.charges)]
		_, err := u.Refund(ctx, meter.RefundRequest{ChargeID: c, Remaining: true})
		return err
	case diffStepReserve:
		r, err := u.Reserve(ctx, meter.ReserveRequest{Subject: subject, Amount: nanos, TTL: diffTTL})
		if err == nil {
			side.holds = append(side.holds, r.ID)
		}
		return err
	case diffStepSettle:
		if len(side.holds) == 0 {
			return nil
		}
		h := side.holds[len(side.holds)-1]
		side.holds = side.holds[:len(side.holds)-1]
		r, err := u.Settle(ctx, meter.SettleRequest{Subject: subject, ReservationID: h, Amount: nanos}, nil)
		if err == nil && !r.Replayed {
			side.charges = append(side.charges, r.ChargeID)
		}
		return err
	case diffStepRevoke:
		_, err := side.records.Revoke(ctx, limes.RevokeRequest{CapabilityID: id})
		return err
	}
	return fmt.Errorf("no step %d", s)
}

// diffState is what one side holds of one capability, as compared.
type diffState struct {
	Spent, Reserved              limes.Nanos
	Requests, ReservedRequests   int64
	Leased                       bool
	LeaseAmount, LeaseSpent      limes.Nanos
	LeaseRequests, LeaseReqSpent int64
	Revoked                      bool
}

// diffCompare fails unless both sides hold the same of every capability
// and of the tenant.
func diffCompare(t *testing.T, sides [2]*diffSide, ids [diffNodes]uuid.UUID, tenant uuid.UUID, at int) {
	t.Helper()
	for n, id := range ids {
		var states [2]diffState
		for j, side := range sides {
			states[j] = diffStateOf(t, side, id)
		}
		if states[0] != states[1] {
			t.Fatalf("step %d: capability %d: %s %+v, %s %+v", at, n, sides[0].name, states[0], sides[1].name, states[1])
		}
	}
	var budgets [2][2]limes.Nanos
	for j, side := range sides {
		b, err := side.usage.GetTenantBudget(side.ctx, tenant)
		if err != nil {
			t.Fatalf("step %d: %s: tenant: %v", at, side.name, err)
		}
		budgets[j] = [2]limes.Nanos{b.SpentAmount, b.ReservedAmount}
	}
	if budgets[0] != budgets[1] {
		t.Fatalf("step %d: tenant spent and held: %s %v, %s %v", at, sides[0].name, budgets[0], sides[1].name, budgets[1])
	}
}

// diffStateOf reads side's state of id: no counters read as zero ones.
func diffStateOf(t *testing.T, side *diffSide, id uuid.UUID) diffState {
	t.Helper()
	var st diffState
	u, err := side.usage.GetUsage(side.ctx, id)
	switch {
	case errors.Is(err, meter.ErrUsageNotFound):
	case err != nil:
		t.Fatalf("%s: usage of %v: %v", side.name, id, err)
	default:
		st.Spent, st.Reserved, st.Requests, st.ReservedRequests = u.SpentAmount, u.ReservedAmount, u.RequestCount, u.ReservedRequests
	}
	l, err := side.usage.GetLease(side.ctx, id)
	switch {
	case errors.Is(err, meter.ErrLeaseNotFound):
	case err != nil:
		t.Fatalf("%s: lease of %v: %v", side.name, id, err)
	default:
		st.Leased, st.LeaseAmount, st.LeaseSpent, st.LeaseRequests, st.LeaseReqSpent = true, l.Amount, l.Spent, l.Requests, l.RequestsSpent
	}
	if st.Revoked, err = side.records.IsRevoked(side.ctx, id); err != nil {
		t.Fatalf("%s: IsRevoked(%v): %v", side.name, id, err)
	}
	return st
}
