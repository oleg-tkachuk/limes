package metertest

import (
	"errors"
	"sync"
	"testing"
	"uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/internal/expect"
	"github.com/oleg-tkachuk/limes/meter"
)

// The races of siblings: capabilities that share an ancestor or a tenant,
// each well within its own budget, spending at once against the ceiling
// they share. A store that locks the subject's own counters alone passes
// every check of one capability racing itself and lets siblings spend past
// their root; these are the checks it fails.
const (
	// siblingRacers is how many siblings race at once: more than the
	// shared ceiling admits, few enough that a store with a pool of one or
	// two connections queues them briefly.
	siblingRacers = 8
	// siblingCharge is what each sibling charges, holds or settles.
	siblingCharge = 3 * one
	// siblingCeiling is the ceiling the siblings share: room for
	// siblingsAdmitted charges and a remainder too small for another.
	siblingCeiling = 10 * one
	// siblingsAdmitted is how many of the racing siblings fit under it.
	siblingsAdmitted = int(siblingCeiling / siblingCharge)
	// siblingBudget is each sibling's own budget, which never binds.
	siblingBudget = 100 * one
	// siblingRoots is how many roots of one tenant the tenant race spreads
	// its siblings over.
	siblingRoots = 2
	// branches is how many children the grandchildren's race runs under,
	// branchCeiling the ceiling of each: tighter than the root's share of
	// one, looser than the root's whole.
	branches      = 2
	branchCeiling = 2 * siblingCharge
)

// race runs call for every racer at once, each released by one start so
// that they overlap, and returns which were admitted. A refusal is err
// matching refusal; any other error fails the check.
func race(t *testing.T, racers int, refusal error, call func(i int) error) []bool {
	t.Helper()
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		failures []error
	)
	start := make(chan struct{})
	admitted := make([]bool, racers)
	for i := range racers {
		wg.Go(func() {
			<-start
			err := call(i)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				admitted[i] = true
			case !errors.Is(err, refusal):
				failures = append(failures, err)
			}
		})
	}
	close(start)
	wg.Wait()
	expect.Must(t, len(failures) == 0, "racing calls failed other than by %v: %v", refusal, failures)
	return admitted
}

// count is how many of the racers were admitted.
func count(admitted []bool) int {
	n := 0
	for _, ok := range admitted {
		if ok {
			n++
		}
	}
	return n
}

// siblings records n capabilities under parent, each with siblingBudget as
// its own, and returns them as the subjects a verified capability gives.
func (f fixture) siblings(t *testing.T, parent uuid.UUID, n int) ([]uuid.UUID, []limes.MeterSubject) {
	t.Helper()
	ids := make([]uuid.UUID, n)
	subjects := make([]limes.MeterSubject, n)
	for i := range n {
		id, err := f.newCapability(parent, limes.BudgetCeiling{Max: siblingBudget})
		expect.Must(t, err == nil, "sibling %d: %v", i, err)
		ids[i] = id
		subjects[i] = newSubject(subjectSpec{capID: id, tenant: f.Tenant, budget: budgetOf(siblingBudget)})
	}
	return ids, subjects
}

// raceCharges charges siblingCharge for every subject at once and holds the
// store to the ceiling they share: exactly siblingsAdmitted admitted, the
// rest refused with refusal, and each spending its charge or nothing.
func (f fixture) raceCharges(t *testing.T, ids []uuid.UUID, subjects []limes.MeterSubject, refusal error) {
	t.Helper()
	admitted := race(t, len(subjects), refusal, func(i int) error {
		_, err := f.Usage.Charge(f.Ctx, meter.ChargeRequest{Subject: subjects[i], Amount: siblingCharge}, nil)
		return err
	})
	got := count(admitted)
	expect.That(t, got == siblingsAdmitted, "%d of %d sibling charges of %v admitted under a shared ceiling of %v, want %d",
		got, len(subjects), siblingCharge, siblingCeiling, siblingsAdmitted)
	for i, id := range ids {
		want := limes.Nanos(0)
		if admitted[i] {
			want = siblingCharge
		}
		spent := f.spent(t, id)
		expect.That(t, spent == want, "sibling %d spent %v, admitted %v; want %v", i, spent, admitted[i], want)
	}
}

// checkSiblingCharges holds a store to locking every ancestor a charge
// counts on: siblings charging at once, each within its own budget, must
// not together spend past their root's ceiling.
func checkSiblingCharges(t *testing.T, f fixture) {
	root, err := f.newCapability(uuid.Nil(), limes.BudgetCeiling{Max: siblingCeiling})
	expect.Must(t, err == nil, "root: %v", err)
	ids, subjects := f.siblings(t, root, siblingRacers)
	f.raceCharges(t, ids, subjects, limes.ErrBudgetExceeded)
	want := limes.Nanos(siblingsAdmitted) * siblingCharge
	spent := f.spent(t, root)
	expect.That(t, spent == want, "the root spent %v, past its %v or short of what was admitted, want %v", spent, siblingCeiling, want)
	tenant := f.tenantSpent(t)
	expect.That(t, tenant == want, "the tenant spent %v, want %v", tenant, want)
}

// checkSiblingHolds holds a store to counting holds on every ancestor as it
// takes them: siblings reserving at once must not together hold past their
// root's ceiling; their holds, settled at once, must leave the root what
// was held and no more; and charges racing those settles must find no room.
func checkSiblingHolds(t *testing.T, f fixture) {
	root, err := f.newCapability(uuid.Nil(), limes.BudgetCeiling{Max: siblingCeiling})
	expect.Must(t, err == nil, "root: %v", err)
	_, subjects := f.siblings(t, root, siblingRacers)
	holds := make([]uuid.UUID, siblingRacers)
	admitted := race(t, siblingRacers, limes.ErrBudgetExceeded, func(i int) error {
		res, err := f.Usage.Reserve(f.Ctx, meter.ReserveRequest{Subject: subjects[i], Amount: siblingCharge})
		holds[i] = res.ID
		return err
	})
	got := count(admitted)
	expect.Must(t, got == siblingsAdmitted, "%d of %d sibling holds of %v admitted under a shared ceiling of %v, want %d",
		got, siblingRacers, siblingCharge, siblingCeiling, siblingsAdmitted)
	held := limes.Nanos(siblingsAdmitted) * siblingCharge
	r := f.usageOf(t, root)
	expect.Must(t, r.ReservedAmount == held && r.SpentAmount == 0, "the root once its children held = %+v, want %v held and nothing spent", r, held)
	// The admitted settle their holds while the refused charge: the root's
	// remainder fits none of the charges, so every one is refused.
	settled := race(t, siblingRacers, limes.ErrBudgetExceeded, func(i int) error {
		if admitted[i] {
			_, err := f.Usage.Settle(f.Ctx, meter.SettleRequest{Subject: subjects[i], ReservationID: holds[i], Amount: siblingCharge}, nil)
			return err
		}
		_, err := f.Usage.Charge(f.Ctx, meter.ChargeRequest{Subject: subjects[i], Amount: siblingCharge}, nil)
		return err
	})
	for i := range siblingRacers {
		expect.That(t, settled[i] == admitted[i], "sibling %d: held %v, then admitted %v while the holds settled", i, admitted[i], settled[i])
	}
	r, b := f.usageOf(t, root), f.tenantOf(t)
	expect.That(t, r.SpentAmount == held && r.ReservedAmount == 0, "the root once every hold settled = %+v, want %v spent and nothing held", r, held)
	expect.That(t, b.SpentAmount == held && b.ReservedAmount == 0, "the tenant once every hold settled = %v spent, %v held; want %v and nothing", b.SpentAmount, b.ReservedAmount, held)
}

// checkSiblingTenantCharges holds a store to locking the tenant aggregate a
// charge counts on: capabilities under different roots of one tenant,
// charging at once, must not together spend past the tenant's ceiling.
func checkSiblingTenantCharges(t *testing.T, f fixture) {
	_, err := f.Usage.SetTenantBudget(f.Ctx, meter.SetTenantBudgetRequest{
		TenantID: f.Tenant, Ceiling: limes.BudgetCeiling{Max: siblingCeiling, UnitCode: unit}, ExpectedVersion: f.tenantOf(t).ResourceVersion,
	})
	expect.Must(t, err == nil, "tenant ceiling: %v", err)
	var (
		ids      []uuid.UUID
		subjects []limes.MeterSubject
		roots    []uuid.UUID
	)
	for i := range siblingRoots {
		root, err := f.newCapability(uuid.Nil(), unlimitedBudget)
		expect.Must(t, err == nil, "root %d: %v", i, err)
		roots = append(roots, root)
		children, under := f.siblings(t, root, siblingRacers/siblingRoots)
		ids, subjects = append(ids, children...), append(subjects, under...)
	}
	f.raceCharges(t, ids, subjects, limes.ErrTenantBudgetExceeded)
	want := limes.Nanos(siblingsAdmitted) * siblingCharge
	var spent limes.Nanos
	for _, root := range roots {
		spent += f.spent(t, root)
	}
	expect.That(t, spent == want, "the roots spent %v together, want %v", spent, want)
	tenant := f.tenantSpent(t)
	expect.That(t, tenant == want, "the tenant spent %v, past its %v or short of what was admitted, want %v", tenant, siblingCeiling, want)
}

// checkGrandchildCharges holds a store to locking every counter between a
// charge and its root: grandchildren under two children, charging at once,
// must keep within each child's ceiling and together within the root's.
// Each child admits branchCeiling, and the root less than both together,
// so the root binds the total and a child what lands under it.
func checkGrandchildCharges(t *testing.T, f fixture) {
	root, err := f.newCapability(uuid.Nil(), limes.BudgetCeiling{Max: siblingCeiling})
	expect.Must(t, err == nil, "root: %v", err)
	var (
		ids      []uuid.UUID
		subjects []limes.MeterSubject
		children []uuid.UUID
	)
	for i := range branches {
		child, err := f.newCapability(root, limes.BudgetCeiling{Max: branchCeiling})
		expect.Must(t, err == nil, "child %d: %v", i, err)
		children = append(children, child)
		grandchildren, under := f.siblings(t, child, siblingRacers/branches)
		ids, subjects = append(ids, grandchildren...), append(subjects, under...)
	}
	f.raceCharges(t, ids, subjects, limes.ErrBudgetExceeded)
	want := limes.Nanos(siblingsAdmitted) * siblingCharge
	var total limes.Nanos
	for i, child := range children {
		spent := f.spent(t, child)
		expect.That(t, spent <= branchCeiling, "child %d spent %v, past its %v", i, spent, branchCeiling)
		total += spent
	}
	expect.That(t, total == want, "the children spent %v together, want %v", total, want)
	spent := f.spent(t, root)
	expect.That(t, spent == want, "the root spent %v, past its %v or short of what was admitted, want %v", spent, siblingCeiling, want)
}

// checkCounterSiblings holds Bump to locking every ancestor it counts on:
// siblings bumping at once, each with room of its own, must not together
// count past their root's request ceiling.
func checkCounterSiblings(t *testing.T, e CounterEnv) {
	root := e.record(t, uuid.Nil(), limes.RequestCeiling{Max: counterRequestLimit})
	specs := make([]subjectSpec, siblingRacers)
	for i := range specs {
		specs[i] = subjectSpec{capID: e.record(t, root, unlimitedRequests), requests: requestsOf(siblingRacers)}
	}
	admitted := race(t, siblingRacers, limes.ErrRequestLimitExceeded, func(i int) error {
		_, err := e.bump(e.Ctx, specs[i])
		return err
	})
	got := count(admitted)
	expect.That(t, got == counterRequestLimit, "%d of %d sibling bumps admitted under their root's ceiling of %d, want %d",
		got, siblingRacers, counterRequestLimit, counterRequestLimit)
}
