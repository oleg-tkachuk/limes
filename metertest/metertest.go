// Package metertest checks a limes.UsageStore against the module's contract
// — its Meter, tenant budgets and housekeeping — the way testing/fstest
// checks an fs.FS. A UsageStore is the consumer's to write,
// and most of its contract lives in request fields — a store that ignores
// ExternalRef or Overrun still compiles, and then charges a repeated cost
// twice. Run it from the store's own tests:
//
//	func TestMeterContract(t *testing.T) {
//		metertest.Run(t, func(t *testing.T) metertest.Env[pgx.Tx] { … })
//	}
//
// The checks cover what a charge does at its ceilings and when it arrives
// again: rejection that moves nothing — by a Biscuit copy's, the capability's,
// an ancestor's or the tenant's ceiling, by a failed onCharged, under
// concurrent charges — idempotency on ExternalRef and on a settled
// reservation, also once the capability is gone, ChargeByRef, OverrunRecord
// past every ceiling, refunds, holds, their listing and their expiry, request
// counts, copy counters and CopyUsage, units, counters that never pass
// MaxNanos, a capability not on record, DeleteUsage and PurgeOrphans, the
// tenant aggregate, its version guard and its listing, and the validation of
// the request. They need a real ledger, not a fake.
//
// Several checks call the store from many goroutines at once. Give the Env a
// connection pool of one or two connections: a call that takes a second
// connection while it holds its transaction then deadlocks here, not under a
// production load. A check that does not finish within Env.CheckTimeout stops
// the test binary with every goroutine's stack, naming the check.
package metertest

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"math"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/internal/fields"
	"github.com/oleg-tkachuk/limes/internal/link"
	"github.com/oleg-tkachuk/limes/internal/watchdog"
)

// DefaultCheckTimeout bounds a check whose Env sets no CheckTimeout.
const DefaultCheckTimeout = watchdog.DefaultLimit

// Env is one store under test, fresh for each check.
type Env[TX any] struct {
	// Ctx is the context every call for Tenant runs under — scoped to it, if
	// the store needs that. A write for a tenant from NewTenant runs under
	// TenantCtx, and ListTenantBudgets under ListCtx, when the Env sets them.
	Ctx context.Context
	// Usage is the store under test, with no usage recorded.
	Usage limes.UsageStore[TX]
	// Tenant is a tenant the store can charge, with no ceiling yet.
	Tenant uuid.UUID
	// NewCapability records a capability of Tenant as spec describes it, so
	// the Meter finds its lineage and its ceilings, and returns its id — or
	// the error the store's Insert returned, which the checks match by its
	// sentinel.
	NewCapability func(spec CapabilitySpec) (uuid.UUID, error)
	// CheckTimeout bounds one check, setup aside: past it the suite panics
	// with every goroutine's stack, naming the check, rather than leave a
	// deadlocked store to go test's -timeout. Optional: without it,
	// DefaultCheckTimeout.
	CheckTimeout time.Duration
	// Advance moves the store's clock forward by d. Optional: without it,
	// the checks of reservation expiry are skipped.
	Advance func(d time.Duration)
	// DeleteCapability removes a capability's record, as Store.Purge does
	// once it has expired. Optional: without it, the check of PurgeOrphans
	// is skipped.
	//
	// Some checks remove an ancestor whose children stay on record, which
	// Purge never does: a store whose records refer to their parent (a
	// foreign key) unhooks the children first, leaving each a root, as its
	// lineage reads once the ancestor is gone. pgstore's own test does it
	// in one statement: UPDATE the children's parent to none, then DELETE.
	DeleteCapability func(id uuid.UUID) error
	// NewTenant creates another tenant the store can charge, for the checks
	// that need more than one. Optional: without it, a random id, which a
	// store that refers to its tenants refuses.
	NewTenant func() (uuid.UUID, error)
	// TenantCtx is the context a write for tenant — one NewTenant created —
	// runs under, when the store scopes each tenant's rows; Tenant's own
	// calls run under Ctx. Optional: without it, Ctx.
	TenantCtx func(tenant uuid.UUID) context.Context
	// ListCtx is the context ListTenantBudgets runs under: a read across
	// tenants. Optional: without it, Ctx.
	ListCtx context.Context
	// Ledger reads the store's charges ledger. Optional: without it, the
	// checks of limes.LedgerReader are skipped.
	Ledger limes.LedgerReader
	// Now reads the store's clock, which stands still while a check runs
	// but for Advance. Optional: without it, the checks of the ledger's
	// time buckets are skipped.
	Now func() time.Time
	// ScopesTenants reports that the store keeps each tenant's rows apart
	// by the call's context, TenantCtx giving each tenant's: a call whose
	// request names one tenant, made under another tenant's context, is
	// refused with limes.ErrScopeMismatch and changes nothing. Optional:
	// without it, the checks of that are skipped.
	ScopesTenants bool
}

// CapabilitySpec is a capability Env.NewCapability records: its parent and
// the ceilings of its caveats.
type CapabilitySpec struct {
	// Parent is the capability it was delegated from; uuid.Nil for a root.
	Parent uuid.UUID
	// Tenant is the tenant it is issued in, one NewTenant created; uuid.Nil
	// for Env.Tenant.
	Tenant uuid.UUID
	// Requests and Budget are its caveats' ceilings.
	Requests limes.RequestCeiling
	Budget   limes.BudgetCeiling
}

// newCapability records a capability under parent with the given budget and
// no request ceiling.
func (e Env[TX]) newCapability(parent uuid.UUID, budget limes.BudgetCeiling) (uuid.UUID, error) {
	return e.NewCapability(CapabilitySpec{Parent: parent, Requests: limes.RequestCeiling{Unlimited: true}, Budget: budget})
}

// newTenant is another tenant, from NewTenant when the Env has one.
func (e Env[TX]) newTenant() (uuid.UUID, error) {
	if e.NewTenant == nil {
		return uuid.New(), nil
	}
	return e.NewTenant()
}

// tenantCtx is the context a write for tenant runs under.
func (e Env[TX]) tenantCtx(tenant uuid.UUID) context.Context {
	if e.TenantCtx == nil {
		return e.Ctx
	}
	return e.TenantCtx(tenant)
}

// listCtx is the context a read across tenants runs under.
func (e Env[TX]) listCtx() context.Context {
	if e.ListCtx == nil {
		return e.Ctx
	}
	return e.ListCtx
}

// The ceilings every check works under: a root, a child narrower than it,
// and a tenant aggregate between the two sums.
const (
	// one is one unit; cent a hundredth of one.
	one  limes.Nanos = limes.NanosPerUnit
	cent             = one / 100

	rootBudget   = 25 * one
	childBudget  = 20 * one
	tenantBudget = 30 * one
	// unit is the currency every amount is in; otherUnit is one it is not.
	unit      = limes.DefaultUnitCode
	otherUnit = "EUR"
	// noUtilization stands for a tenant with no ceiling where a
	// percentage is compared; no percentage equals it.
	noUtilization = -1
	// periodLength is how far ahead the accounting window of the
	// PeriodEnd check closes.
	periodLength = 30 * 24 * time.Hour
	// tiedTenants is how many tenants of the listing check spend nothing
	// under a ceiling, and so share one utilization.
	tiedTenants = 2
	// concurrentDeliveries is how many times one cost arrives at once.
	concurrentDeliveries = 20
)

// fixture is an Env with a root, a child under it, and the tenant's ceiling set.
type fixture[TX any] struct {
	Env[TX]
	root, child uuid.UUID
}

func newFixture[TX any](t *testing.T, setup func(t *testing.T) Env[TX]) fixture[TX] {
	t.Helper()
	env := setup(t)
	f := fixture[TX]{Env: env}
	var err error
	if f.root, err = env.newCapability(uuid.Nil, limes.BudgetCeiling{Max: rootBudget}); err != nil {
		t.Fatalf("root capability: %v", err)
	}
	if f.child, err = env.newCapability(f.root, limes.BudgetCeiling{Max: childBudget}); err != nil {
		t.Fatalf("child capability: %v", err)
	}
	if _, err := env.Usage.SetTenantBudget(env.Ctx, limes.SetTenantBudgetRequest{
		TenantID: env.Tenant, Ceiling: limes.BudgetCeiling{Max: tenantBudget}, UnitCode: unit,
	}); err != nil {
		t.Fatalf("tenant budget: %v", err)
	}
	return f
}

// unlimitedBudget is a capability's budget when the check needs none.
var unlimitedBudget = limes.BudgetCeiling{Unlimited: true}

// subjectSpec is a meter subject's parts; zero fields take the fixture's.
type subjectSpec struct {
	capID  uuid.UUID
	tenant uuid.UUID
	// requests and budget are the subject's ceilings; nil, a check that
	// sets none, is Unlimited.
	requests *limes.RequestCeiling
	budget   *limes.BudgetCeiling
	unit     string
	copies   []limes.CopyCeiling
}

// newSubject is the MeterSubject a verified capability with spec's parts
// would give: the suite checks a store against ceilings no issuer signed.
func newSubject(spec subjectSpec) limes.MeterSubject {
	if spec.unit == "" {
		spec.unit = unit
	}
	return link.NewMeterSubject(link.MeterSubjectFields{
		CapabilityID: spec.capID, TenantID: spec.tenant,
		Requests: spec.requestCeiling(), Budget: spec.budgetCeiling(), UnitCode: spec.unit, Copies: spec.copies,
	}).(limes.MeterSubject)
}

func (spec subjectSpec) requestCeiling() limes.RequestCeiling {
	if spec.requests == nil {
		return limes.RequestCeiling{Unlimited: true}
	}
	return *spec.requests
}

func (spec subjectSpec) budgetCeiling() limes.BudgetCeiling {
	if spec.budget == nil {
		return limes.BudgetCeiling{Unlimited: true}
	}
	return *spec.budget
}

// budgetOf and requestsOf are ceilings of n, for a spec to set.
func budgetOf(n limes.Nanos) *limes.BudgetCeiling { return &limes.BudgetCeiling{Max: n} }
func requestsOf(n int64) *limes.RequestCeiling    { return &limes.RequestCeiling{Max: n} }

// childSubject is the fixture's child as a verified capability would give it.
func (f fixture[TX]) childSubject() limes.MeterSubject {
	return newSubject(subjectSpec{capID: f.child, tenant: f.Tenant, budget: budgetOf(childBudget)})
}

// charge charges req against the fixture's child, its ceiling, tenant and
// unit, unless the request's subject names another capability or ceiling.
func (f fixture[TX]) charge(req limes.ChargeRequest, onCharged func(context.Context, TX) error) (limes.ChargeReceipt, error) {
	s := req.Subject
	requests, budget := s.Requests(), s.Budget()
	spec := subjectSpec{capID: s.CapabilityID(), tenant: f.Tenant, requests: &requests, budget: &budget, copies: s.Copies()}
	// A subject naming no capability is the child's, under the child's
	// budget and no request ceiling, with whatever copies it names.
	if spec.capID == uuid.Nil {
		spec.capID = f.child
		spec.budget = budgetOf(childBudget)
		spec.requests = nil
	}
	req.Subject = newSubject(spec)
	return f.Usage.Charge(f.Ctx, req, onCharged)
}

func (f fixture[TX]) spent(t *testing.T, id uuid.UUID) limes.Nanos {
	t.Helper()
	u, err := f.Usage.GetUsage(f.Ctx, id)
	if errors.Is(err, limes.ErrUsageNotFound) {
		return 0
	}
	if err != nil {
		t.Fatalf("usage of %s: %v", id, err)
	}
	return u.SpentAmount
}

func (f fixture[TX]) tenantSpent(t *testing.T) limes.Nanos {
	t.Helper()
	b, err := f.Usage.GetTenantBudget(f.Ctx, f.Tenant)
	if err != nil {
		t.Fatalf("tenant budget: %v", err)
	}
	return b.SpentAmount
}

// Run runs every check as a subtest, each on a fresh Env from setup.
func Run[TX any](t *testing.T, setup func(t *testing.T) Env[TX]) {
	t.Helper()
	checks := []struct {
		name string
		run  func(*testing.T, fixture[TX])
	}{
		{"RejectionMovesNothing", checkRejectionMovesNothing[TX]},
		{"ExternalRefChargesOnce", checkExternalRefChargesOnce[TX]},
		{"ExternalRefChargesOnceConcurrently", checkExternalRefConcurrent[TX]},
		{"ChargeByRef", checkChargeByRef[TX]},
		{"MalformedRequestsAreRefused", checkMalformed[TX]},
		{"OverrunRecordCrossesEveryCeiling", checkOverrunRecord[TX]},
		{"SettleReplaysAndRecordsAnOverrun", checkSettle[TX]},
		{"SettleOfNoReservationIsNotFound", checkSettleNil[TX]},
		{"SettleOfAnotherCapabilitysHoldIsNotFound", checkSettleOwner[TX]},
		{"SettleWithinTheHoldPassesACrossedCeiling", checkSettleWithinHold[TX]},
		{"RefundOfAClosedPeriodLeavesTheTenantPeriod", checkRefundAcrossReset[TX]},
		{"EveryCounterKeepsOneUnit", checkOneUnit[TX]},
		{"AnAncestorCountsInTheUnitOfItsChildren", checkAncestorUnit[TX]},
		{"DeleteReleasesHolds", checkDeleteReleasesHolds[TX]},
		{"AReplayIsAnsweredOnceTheCapabilityIsGone", checkReplayAfterRemoval[TX]},
		{"AnAncestorAndATenantCeilingAreExact", checkExactCeilings[TX]},
		{"AHoldAloneCountsOnItsTenant", checkHoldCreatesTenant[TX]},
		{"ASettleReplayCommitsNothingAgain", checkSettleReplay[TX]},
		{"ReleasingAHoldReturnsItToTheCopy", checkReleaseReturnsCopy[TX]},
		{"ARefusedBumpCountsNothing", checkRefusedBump[TX]},
		{"HoldsCountTowardsTheCounterRange", checkRangeCountsHolds[TX]},
		{"ARefundOfExactlyWhatIsLeftRefundsIt", checkRefundExactRemainder[TX]},
		{"ASettleWritesNoRowForARemovedAncestor", checkSettleAfterRemovedAncestor[TX]},
		{"ASettleCountsOnAnAncestorWhoseRowWasDropped", checkSettleAfterAncestorDropped[TX]},
		{"ConcurrentChargesKeepTheCeiling", checkConcurrentCeiling[TX]},
		{"AnAncestorCeilingBoundsItsChildren", checkAncestorCeiling[TX]},
		{"TheTenantCeilingBoundsEveryCapability", checkTenantCeiling[TX]},
		{"AFailedCallbackMovesNothing", checkCallbackRollback[TX]},
		{"BumpCountsTheChain", checkBump[TX]},
		{"RefundReturnsToEveryCounter", checkRefund[TX]},
		{"AHoldCountsAgainstCharges", checkHoldBlocksCharge[TX]},
		{"AnExpiredHoldIsReleased", checkExpiry[TX]},
		{"ListTenantBudgetsPagesMostAtRiskFirst", checkListTenantBudgets[TX]},
		{"OverrunRecordCrossesACopyBudget", checkCopyOverrun[TX]},
		{"ACopyCountsForItsCapabilityAndGoesWithIt", checkCopyCountersGo[TX]},
		{"CopyUsageRefusesAnInvalidRequest", checkCopyUsageRefusesInvalid[TX]},
		{"TenantBudgetReadsAgree", checkTenantBudgetReads[TX]},
		{"ReservationsReadBack", checkReservations[TX]},
		{"SumsExactly", checkSumsExactly[TX]},
		{"ReservationsPage", checkReservationsPage[TX]},
		{"AHoldWithNoTTLLastsTheDefault", checkDefaultReservationTTL[TX]},
		{"ReservationsTiedOnExpiryPageInIDOrder", checkReservationsTied[TX]},
		{"HousekeepingRefusesANegativeLimit", checkHousekeepingLimits[TX]},
		{"PurgeOrphansReleasesWhatARemovedCapabilityHeld", checkPurgeOrphans[TX]},
		{"AChainAtMaxDepthTakesAChildOnceItsTopIsRemoved", checkLineageAfterRemoval[TX]},
		{"TenantBudgetKeepsItsPeriodEnd", checkPeriodEnd[TX]},
		{"ACallScopedToAnotherTenantIsRefused", checkScopeMismatch[TX]},
		{"EraseTenantUsageDropsEveryRowOfTheTenant", checkEraseTenantUsage[TX]},
		{"SumChargesGroupsAndPagesTheLedger", checkSumCharges[TX]},
		{"SumChargesCutsBucketsAtUTCBoundaries", checkSumChargeBuckets[TX]},
		{"SumChargesOrdersByAmountNotDigits", checkSumChargesOrderByAmount[TX]},
		{"ResetSpendAloneRollsThePeriodStart", checkPeriodStart[TX]},
		{"TheZeroSubjectIsRefused", checkZeroSubject[TX]},
		{"AnAncestorRequestCeilingBoundsBump", checkAncestorRequests[TX]},
		{"ACopyRequestCeilingBoundsBump", checkCopyRequests[TX]},
		{"AHoldCountsAgainstACopyBudget", checkCopyHold[TX]},
		{"AStaleVersionIsRefused", checkStaleVersion[TX]},
		{"ACapabilityNotOnRecordIsNotMetered", checkNotOnRecord[TX]},
		{"ACounterNeverPassesMaxNanos", checkCounterOverflow[TX]},
		{"ReleaseReturnsTheHoldToEveryCounter", checkRelease[TX]},
		{"AResetKeepsTheHolds", checkResetKeepsHolds[TX]},
		{"AnAncestorCountsItsDescendantsHolds", checkDescendantHolds[TX]},
		{"SettleAboveTheHoldCountsTheHoldOnce", checkSettleAboveHold[TX]},
		{"ANestedCopySettlesOnlyItsOwnHold", checkNestedCopySettle[TX]},
		{"TheUnitStaysWhileHoldsAreOpen", checkUnitWithHolds[TX]},
		{"EveryCounterOnTheChainKeepsItsUnit", checkChainUnits[TX]},
		{"ListTenantBudgetsRanksUnclamped", checkUnclampedRanking[TX]},
		{"AThresholdIsMetExactly", checkThresholdBoundary[TX]},
		{"ListTenantBudgetsRanksExactly", checkExactRanking[TX]},
		{"HousekeepingKeepsItsLimit", checkHousekeepingBatches[TX]},
		{"CopiesFollowSettleAndRefund", checkCopySettleRefund[TX]},
		{"AZeroCeilingAllowsNothing", checkZeroCeilings[TX]},
	}
	for _, c := range checks {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, setup)
			watchdog.Run(c.name, f.CheckTimeout, func() { c.run(t, f) })
		})
	}
	// Checks that need the tenant as setup leaves it, with no ceiling yet.
	bare := []struct {
		name string
		run  func(*testing.T, Env[TX])
	}{
		{"TenantAggregateCountsBeforeACeiling", checkTenantCountsBeforeACeiling[TX]},
	}
	for _, c := range bare {
		t.Run(c.name, func(t *testing.T) {
			env := setup(t)
			watchdog.Run(c.name, env.CheckTimeout, func() { c.run(t, env) })
		})
	}
}

// checkTenantCountsBeforeACeiling holds a store to counting the tenant
// aggregate from the first charge and hold: a ceiling set afterwards must see
// them, or the spend before it is free and the holds before it are not held.
func checkTenantCountsBeforeACeiling[TX any](t *testing.T, env Env[TX]) {
	const (
		spent   = 5 * one
		held    = 8 * one
		ceiling = 10 * one
	)
	id, err := env.newCapability(uuid.Nil, unlimitedBudget)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.Usage.Charge(env.Ctx, limes.ChargeRequest{
		Subject: newSubject(subjectSpec{capID: id, tenant: env.Tenant, unit: unit}),
		Amount:  spent,
	}, nil); err != nil {
		t.Fatalf("charge: %v", err)
	}
	reserve := func() error {
		_, err := env.Usage.Reserve(env.Ctx, limes.ReserveRequest{
			Subject: newSubject(subjectSpec{capID: id, tenant: env.Tenant, unit: unit}),
			Amount:  held,
		})
		return err
	}
	if err := reserve(); err != nil {
		t.Fatalf("reserve: %v", err)
	}
	b, err := env.Usage.GetTenantBudget(env.Ctx, env.Tenant)
	if err != nil || b.SpentAmount != spent || b.ReservedAmount != held || !b.Ceiling.Unlimited || b.ResourceVersion != 0 {
		t.Fatalf("GetTenantBudget before any ceiling = %+v, %v; want %v spent, %v held, no ceiling, version 0", b, err, spent, held)
	}
	set, err := env.Usage.SetTenantBudget(env.Ctx, limes.SetTenantBudgetRequest{
		TenantID: env.Tenant, Ceiling: limes.BudgetCeiling{Max: ceiling}, UnitCode: unit,
	})
	if err != nil || set.SpentAmount != spent || set.ReservedAmount != held {
		t.Fatalf("SetTenantBudget = %+v, %v; want the ceiling over %v spent and %v held", set, err, spent, held)
	}
	if err := reserve(); !errors.Is(err, limes.ErrTenantBudgetExceeded) {
		t.Errorf("a hold past the new ceiling: err = %v, want ErrTenantBudgetExceeded", err)
	}
	if _, err := env.Usage.Charge(env.Ctx, limes.ChargeRequest{
		Subject: newSubject(subjectSpec{capID: id, tenant: env.Tenant, unit: unit}),
		Amount:  one,
	}, nil); !errors.Is(err, limes.ErrTenantBudgetExceeded) {
		t.Errorf("a charge past the new ceiling: err = %v, want ErrTenantBudgetExceeded", err)
	}
}

func checkRejectionMovesNothing[TX any](t *testing.T, f fixture[TX]) {
	ran := false
	r, err := f.charge(limes.ChargeRequest{Amount: childBudget + one}, func(context.Context, TX) error { ran = true; return nil })
	if !errors.Is(err, limes.ErrBudgetExceeded) {
		t.Fatalf("charge past the ceiling: err = %v, want ErrBudgetExceeded", err)
	}
	if r.TenantID != f.Tenant || r.UnitCode != unit {
		t.Errorf("a refusal's receipt names tenant %s in %q, want %s in %q", r.TenantID, r.UnitCode, f.Tenant, unit)
	}
	if ran || f.spent(t, f.child) != 0 || f.spent(t, f.root) != 0 || f.tenantSpent(t) != 0 {
		t.Fatalf("a rejected charge moved something (callback ran: %v)", ran)
	}
}

func checkExternalRefChargesOnce[TX any](t *testing.T, f fixture[TX]) {
	fanOuts := 0
	onCharged := func(context.Context, TX) error { fanOuts++; return nil }
	first, err := f.charge(limes.ChargeRequest{Amount: 2 * one, ExternalRef: "call-1"}, onCharged)
	if err != nil || first.Replayed {
		t.Fatalf("first charge = %+v, %v", first, err)
	}
	again, err := f.charge(limes.ChargeRequest{Amount: 2 * one, ExternalRef: "call-1"}, onCharged)
	if err != nil || !again.Replayed || again.ChargeID != first.ChargeID || again.SpentAmount != 2*one {
		t.Fatalf("repeated charge = %+v, %v; want a replay of %s at spend 2", again, err, first.ChargeID)
	}
	// The same name for a different cost is the caller's bug; replaying the
	// first receipt would hide it.
	if r, err := f.charge(limes.ChargeRequest{Amount: 5 * one, ExternalRef: "call-1"}, onCharged); !errors.Is(err, limes.ErrExternalRefConflict) {
		t.Fatalf("the ref again for another amount = %+v, %v; want ErrExternalRefConflict", r, err)
	}
	// So is the same name for the same amount in another unit or for
	// another tenant: it names another cost all the same.
	for name, subject := range map[string]limes.MeterSubject{
		"in another unit":    newSubject(subjectSpec{capID: f.child, tenant: f.Tenant, budget: budgetOf(childBudget), unit: otherUnit}),
		"for another tenant": newSubject(subjectSpec{capID: f.child, tenant: uuid.New(), budget: budgetOf(childBudget)}),
	} {
		want := limes.ErrExternalRefConflict
		// A store that scopes tenants refuses the call before it reads
		// the ledger: Ctx is not that tenant's.
		if f.ScopesTenants && subject.TenantID() != f.Tenant {
			want = limes.ErrScopeMismatch
		}
		if r, err := f.Usage.Charge(f.Ctx, limes.ChargeRequest{Subject: subject, Amount: 2 * one, ExternalRef: "call-1"}, onCharged); !errors.Is(err, want) {
			t.Errorf("the ref again %s = %+v, %v; want %v", name, r, err, want)
		}
	}
	if fanOuts != 1 {
		t.Errorf("onCharged ran %d times, want once", fanOuts)
	}
	// The name is per capability, and no name never deduplicates.
	if r, err := f.charge(limes.ChargeRequest{Subject: newSubject(subjectSpec{capID: f.root, budget: budgetOf(rootBudget)}), Amount: one, ExternalRef: "call-1"}, nil); err != nil || r.Replayed {
		t.Errorf("same ref on another capability = %+v, %v; want a fresh charge", r, err)
	}
	for range 2 {
		if r, err := f.charge(limes.ChargeRequest{Amount: one}, nil); err != nil || r.Replayed {
			t.Errorf("charge without a ref = %+v, %v; want a fresh charge", r, err)
		}
	}
	if got := f.spent(t, f.child); got != 4*one {
		t.Errorf("child spent %v, want 2 + 1 + 1", got)
	}
}

func checkExternalRefConcurrent[TX any](t *testing.T, f fixture[TX]) {
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		fresh   int
		charges = map[uuid.UUID]bool{}
	)
	for range concurrentDeliveries {
		wg.Go(func() {
			r, err := f.charge(limes.ChargeRequest{Amount: 2 * one, ExternalRef: "call-1"}, nil)
			if err != nil {
				t.Errorf("charge: %v", err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			charges[r.ChargeID] = true
			if !r.Replayed {
				fresh++
			}
		})
	}
	wg.Wait()
	if fresh != 1 || len(charges) != 1 {
		t.Fatalf("%d fresh charges over %d charge ids, want one of each", fresh, len(charges))
	}
	if got := f.spent(t, f.root); got != 2*one {
		t.Errorf("root spent %v, want 2", got)
	}
}

func checkChargeByRef[TX any](t *testing.T, f fixture[TX]) {
	if _, err := f.Usage.ChargeByRef(f.Ctx, limes.ChargeByRefRequest{CapabilityID: f.child, ExternalRef: "call-1"}); !errors.Is(err, limes.ErrChargeNotFound) {
		t.Fatalf("before any charge: err = %v, want ErrChargeNotFound", err)
	}
	const actor = "reporter"
	r, err := f.charge(limes.ChargeRequest{Amount: 3 * one, ExternalRef: "call-1", Op: limes.OpSearch, Actor: actor}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Usage.Refund(f.Ctx, limes.RefundRequest{ChargeID: r.ChargeID, Amount: one}); err != nil {
		t.Fatal(err)
	}
	got, err := f.Usage.ChargeByRef(f.Ctx, limes.ChargeByRefRequest{CapabilityID: f.child, ExternalRef: "call-1"})
	if err != nil || got.ID != r.ChargeID || got.CapabilityID != f.child || got.Amount != 3*one ||
		got.RefundedAmount != one || got.ExternalRef != "call-1" || got.UnitCode != unit || got.Overrun ||
		got.TenantID != f.Tenant || got.Op != limes.OpSearch || got.Actor != actor || got.At.IsZero() {
		t.Fatalf("ChargeByRef = %+v, %v; want charge %s of 3 with 1 refunded, for %s by %s in %s", got, err, r.ChargeID, limes.OpSearch, actor, f.Tenant)
	}
	if byID, err := f.Usage.GetCharge(f.Ctx, r.ChargeID); err != nil || !reflect.DeepEqual(byID, got) {
		t.Errorf("GetCharge = %+v, %v; want what ChargeByRef read, %+v", byID, err, got)
	}
	if _, err := f.Usage.GetCharge(f.Ctx, uuid.New()); !errors.Is(err, limes.ErrChargeNotFound) {
		t.Errorf("GetCharge of an unknown id: err = %v, want ErrChargeNotFound", err)
	}
	if _, err := f.Usage.ChargeByRef(f.Ctx, limes.ChargeByRefRequest{CapabilityID: f.root, ExternalRef: "call-1"}); !errors.Is(err, limes.ErrChargeNotFound) {
		t.Errorf("the ref on another capability: err = %v, want ErrChargeNotFound", err)
	}
	if _, err := f.Usage.ChargeByRef(f.Ctx, limes.ChargeByRefRequest{CapabilityID: f.child, ExternalRef: ""}); !errors.Is(err, limes.ErrChargeNotFound) {
		t.Errorf("an empty ref: err = %v, want ErrChargeNotFound", err)
	}
}

func checkMalformed[TX any](t *testing.T, f fixture[TX]) {
	for name, req := range map[string]limes.ChargeRequest{
		"ref too long":      {ExternalRef: strings.Repeat("x", limes.MaxExternalRefBytes+1)},
		"ref not printable": {ExternalRef: "call\n1"},
		"unknown overrun":   {Overrun: limes.OverrunRecord + 1},
	} {
		req.Amount = 1
		if _, err := f.charge(req, nil); !errors.Is(err, limes.ErrInvalidRequest) {
			t.Errorf("%s: err = %v, want ErrInvalidRequest", name, err)
		}
	}
	if _, err := f.Usage.Settle(f.Ctx, limes.SettleRequest{
		Subject: f.childSubject(), ReservationID: uuid.New(), Amount: one, Overrun: limes.OverrunRecord + 1,
	}, nil); !errors.Is(err, limes.ErrInvalidRequest) {
		t.Errorf("settle with an unknown overrun: err = %v, want ErrInvalidRequest", err)
	}
	if _, err := f.Usage.Reserve(f.Ctx, limes.ReserveRequest{
		Subject: f.childSubject(),
		Amount:  one,
		TTL:     -time.Minute,
	}); !errors.Is(err, limes.ErrInvalidRequest) {
		t.Errorf("reserve with a negative TTL: err = %v, want ErrInvalidRequest", err)
	}
	// A malformed amount or unit is a malformed request, whatever else it
	// matches.
	for name, amount := range map[string]limes.Nanos{"negative": -one, "past MaxNanos": limes.MaxNanos + 1} {
		if _, err := f.charge(limes.ChargeRequest{Amount: amount}, nil); !errors.Is(err, limes.ErrInvalidAmount) || !errors.Is(err, limes.ErrInvalidRequest) {
			t.Errorf("a charge of a %s amount: err = %v, want ErrInvalidAmount and ErrInvalidRequest", name, err)
		}
		if _, err := f.Usage.Reserve(f.Ctx, limes.ReserveRequest{Subject: f.childSubject(), Amount: amount}); !errors.Is(err, limes.ErrInvalidRequest) {
			t.Errorf("a hold of a %s amount: err = %v, want ErrInvalidRequest", name, err)
		}
	}
	// A settle of a malformed amount, below its hold or past MaxNanos: below
	// the hold no ceiling is asked, so only the amount's own check stops a
	// negative one lowering every counter.
	held, err := f.Usage.Reserve(f.Ctx, limes.ReserveRequest{Subject: f.childSubject(), Amount: one})
	if err != nil {
		t.Fatal(err)
	}
	for name, amount := range map[string]limes.Nanos{"negative": -one, "past MaxNanos": limes.MaxNanos + 1} {
		_, err := f.Usage.Settle(f.Ctx, limes.SettleRequest{Subject: f.childSubject(), ReservationID: held.ID, Amount: amount}, nil)
		if !errors.Is(err, limes.ErrInvalidAmount) {
			t.Errorf("a settle of a %s amount: err = %v, want ErrInvalidAmount", name, err)
		}
	}
	for name, req := range map[string]limes.SetTenantBudgetRequest{
		"in a unit that is not one": {TenantID: f.Tenant, Ceiling: limes.BudgetCeiling{Max: one}, UnitCode: "usd"},
		"for no tenant":             {Ceiling: limes.BudgetCeiling{Max: one}},
	} {
		if _, err := f.Usage.SetTenantBudget(f.Ctx, req); !errors.Is(err, limes.ErrInvalidRequest) {
			t.Errorf("a tenant budget %s: err = %v, want ErrInvalidRequest", name, err)
		}
	}
	if f.spent(t, f.child) != 0 {
		t.Error("a refused request moved spend")
	}
}

// checkCounterOverflow holds a store to refusing, even past every ceiling,
// a charge that would take a counter past MaxNanos: each amount is valid,
// their sum has no room.
func checkCounterOverflow[TX any](t *testing.T, f fixture[TX]) {
	id, err := f.newCapability(uuid.Nil, unlimitedBudget)
	if err != nil {
		t.Fatal(err)
	}
	subject := newSubject(subjectSpec{capID: id, tenant: f.Tenant})
	if _, err := f.charge(limes.ChargeRequest{Subject: subject, Amount: limes.MaxNanos, Overrun: limes.OverrunRecord}, nil); err != nil {
		t.Fatalf("a charge of MaxNanos: %v", err)
	}
	if _, err := f.charge(limes.ChargeRequest{Subject: subject, Amount: 1, Overrun: limes.OverrunRecord}, nil); !errors.Is(err, limes.ErrCounterOverflow) || errors.Is(err, limes.ErrInvalidRequest) {
		t.Errorf("a nano past MaxNanos on the counter: err = %v, want ErrCounterOverflow and not ErrInvalidRequest", err)
	}
	if got := f.spent(t, id); got != limes.MaxNanos {
		t.Errorf("spent %s after the overflow was refused, want %s", got, limes.MaxNanos)
	}
	// Each counter on its own: a fresh capability under no full one meets
	// only the tenant's full counter, and once the tenant is reset, a fresh
	// child meets only its parent's.
	fresh, err := f.newCapability(uuid.Nil, unlimitedBudget)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.charge(limes.ChargeRequest{Subject: newSubject(subjectSpec{capID: fresh}), Amount: 1, Overrun: limes.OverrunRecord}, nil); !errors.Is(err, limes.ErrCounterOverflow) {
		t.Errorf("a nano past MaxNanos on the tenant alone: err = %v, want ErrCounterOverflow", err)
	}
	b, err := f.Usage.GetTenantBudget(f.Ctx, f.Tenant)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Usage.SetTenantBudget(f.Ctx, limes.SetTenantBudgetRequest{
		TenantID: f.Tenant, Ceiling: limes.BudgetCeiling{Unlimited: true}, UnitCode: unit, ResetSpend: true, ExpectedVersion: b.ResourceVersion,
	}); err != nil {
		t.Fatal(err)
	}
	child, err := f.newCapability(id, unlimitedBudget)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.charge(limes.ChargeRequest{Subject: newSubject(subjectSpec{capID: child}), Amount: 1, Overrun: limes.OverrunRecord}, nil); !errors.Is(err, limes.ErrCounterOverflow) {
		t.Errorf("a nano past MaxNanos on the parent alone: err = %v, want ErrCounterOverflow", err)
	}
}

func checkOverrunRecord[TX any](t *testing.T, f fixture[TX]) {
	// 40 crosses the child's 20, the root's 25 and the tenant's 30.
	const amount = 40 * one
	if _, err := f.charge(limes.ChargeRequest{Amount: amount}, nil); err == nil {
		t.Fatal("a rejecting charge past every ceiling was accepted")
	}
	r, err := f.charge(limes.ChargeRequest{Amount: amount, Overrun: limes.OverrunRecord}, nil)
	if err != nil || !r.Overrun || r.Replayed || r.SpentAmount != amount {
		t.Fatalf("recording charge = %+v, %v; want %v spent, overrun", r, err, amount)
	}
	if got := f.spent(t, f.root); got != amount {
		t.Errorf("root spent %v, want %v", got, amount)
	}
	if got := f.tenantSpent(t); got != amount {
		t.Errorf("tenant spent %v, want %v", got, amount)
	}
	if _, err := f.charge(limes.ChargeRequest{Amount: cent}, nil); !errors.Is(err, limes.ErrBudgetExceeded) {
		t.Errorf("after an overrun a rejecting charge: err = %v, want ErrBudgetExceeded", err)
	}
	if _, err := f.Usage.Reserve(f.Ctx, limes.ReserveRequest{
		Subject: f.childSubject(),
		Amount:  cent,
	}); !errors.Is(err, limes.ErrBudgetExceeded) {
		t.Errorf("after an overrun a reservation: err = %v, want ErrBudgetExceeded", err)
	}
}

func checkSettle[TX any](t *testing.T, f fixture[TX]) {
	res, err := f.Usage.Reserve(f.Ctx, limes.ReserveRequest{
		Subject: f.childSubject(),
		Amount:  10 * one,
	})
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	settle := func(amount limes.Nanos, p limes.OverrunPolicy) (limes.ChargeReceipt, error) {
		return f.Usage.Settle(f.Ctx, limes.SettleRequest{
			Subject:       f.childSubject(),
			ReservationID: res.ID,
			Amount:        amount,
			Overrun:       p,
		}, nil)
	}
	if _, err := settle(24*one, limes.OverrunReject); !errors.Is(err, limes.ErrBudgetExceeded) {
		t.Fatalf("rejecting settle past the ceiling: err = %v, want ErrBudgetExceeded", err)
	}
	r, err := settle(24*one, limes.OverrunRecord)
	if err != nil || !r.Overrun || r.SpentAmount != 24*one {
		t.Fatalf("recording settle = %+v, %v; want 24 spent, overrun", r, err)
	}
	if u, err := f.Usage.GetUsage(f.Ctx, f.root); err != nil || u.ReservedAmount != 0 {
		t.Errorf("root after settling = %+v, %v; want nothing held", u, err)
	}
	again, err := settle(one, limes.OverrunReject)
	if err != nil || !again.Replayed || !again.Overrun || again.ChargeID != r.ChargeID || again.SpentAmount != 24*one {
		t.Errorf("settling again = %+v, %v; want a replay of %s", again, err, r.ChargeID)
	}
	if err := f.Usage.Release(f.Ctx, limes.ReleaseRequest{ReservationID: res.ID}); err != nil {
		t.Errorf("releasing a settled reservation: %v", err)
	}
	other, err := f.Usage.Reserve(f.Ctx, limes.ReserveRequest{
		Subject: newSubject(subjectSpec{capID: f.root, tenant: f.Tenant, budget: budgetOf(rootBudget), unit: unit}),
		Amount:  0,
	})
	if err != nil {
		t.Fatalf("reserve nothing: %v", err)
	}
	if err := f.Usage.Release(f.Ctx, limes.ReleaseRequest{ReservationID: other.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Usage.Settle(f.Ctx, limes.SettleRequest{Subject: newSubject(subjectSpec{capID: f.root, tenant: f.Tenant, budget: budgetOf(rootBudget)}), ReservationID: other.ID, Amount: 0}, nil); !errors.Is(err, limes.ErrReservationNotFound) {
		t.Errorf("settling a released reservation: err = %v, want ErrReservationNotFound", err)
	}
}

// checkSettleNil holds a store to keeping direct charges apart from settled
// reservations: a direct charge has no reservation, and a store that files it
// under the zero id would replay it to a Settle that names none.
func checkSettleNil[TX any](t *testing.T, f fixture[TX]) {
	if _, err := f.charge(limes.ChargeRequest{Amount: one}, nil); err != nil {
		t.Fatal(err)
	}
	r, err := f.Usage.Settle(f.Ctx, limes.SettleRequest{Subject: f.childSubject(), ReservationID: uuid.Nil, Amount: one}, nil)
	if !errors.Is(err, limes.ErrReservationNotFound) {
		t.Fatalf("Settle of the zero reservation id = %+v, %v; want ErrReservationNotFound", r, err)
	}
	if got := f.spent(t, f.child); got != one {
		t.Errorf("child spent %v after a refused settle, want 1", got)
	}
}

// checkSettleOwner holds a store to settling a reservation only for the
// capability that made it: a reservation id is no bearer credential, so a
// subject naming another capability finds no such hold, before the settle
// and after it, and the hold stays where it was.
func checkSettleOwner[TX any](t *testing.T, f fixture[TX]) {
	res, err := f.Usage.Reserve(f.Ctx, limes.ReserveRequest{Subject: f.childSubject(), Amount: 2 * one})
	if err != nil {
		t.Fatal(err)
	}
	stranger := newSubject(subjectSpec{capID: f.root, tenant: f.Tenant, budget: budgetOf(rootBudget)})
	if _, err := f.Usage.Settle(f.Ctx, limes.SettleRequest{Subject: stranger, ReservationID: res.ID, Amount: one}, nil); !errors.Is(err, limes.ErrReservationNotFound) {
		t.Errorf("settling the child's hold as the root: err = %v, want ErrReservationNotFound", err)
	}
	if u, err := f.Usage.GetUsage(f.Ctx, f.child); err != nil || u.ReservedAmount != 2*one || u.SpentAmount != 0 {
		t.Errorf("child after a refused settle = %+v, %v; want 2 held, nothing spent", u, err)
	}
	if _, err := f.Usage.Settle(f.Ctx, limes.SettleRequest{Subject: f.childSubject(), ReservationID: res.ID, Amount: one}, nil); err != nil {
		t.Fatalf("settling as the owner: %v", err)
	}
	if r, err := f.Usage.Settle(f.Ctx, limes.SettleRequest{Subject: stranger, ReservationID: res.ID, Amount: one}, nil); !errors.Is(err, limes.ErrReservationNotFound) {
		t.Errorf("replaying the settle as the root = %+v, %v; want ErrReservationNotFound", r, err)
	}
}

// checkRefundAcrossReset holds a store to keeping accounting periods apart: a
// charge counted before the tenant's spend was reset is not in the new
// period's spend, so refunding it must not take it out of that spend.
func checkRefundAcrossReset[TX any](t *testing.T, f fixture[TX]) {
	const amount = 9 * one
	before, err := f.charge(limes.ChargeRequest{Amount: amount}, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.Usage.GetTenantBudget(f.Ctx, f.Tenant)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Usage.SetTenantBudget(f.Ctx, limes.SetTenantBudgetRequest{
		TenantID: f.Tenant, Ceiling: limes.BudgetCeiling{Max: tenantBudget}, UnitCode: unit,
		ResetSpend: true, ExpectedVersion: b.ResourceVersion,
	}); err != nil {
		t.Fatalf("reset: %v", err)
	}
	after, err := f.charge(limes.ChargeRequest{Amount: amount}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if r, err := f.Usage.Refund(f.Ctx, limes.RefundRequest{ChargeID: before.ChargeID, Remaining: true}); err != nil || r.Amount != amount {
		t.Fatalf("refund of the earlier period's charge = %+v, %v; want %v", r, err, amount)
	}
	if got := f.tenantSpent(t); got != amount {
		t.Errorf("tenant spent %v in the new period, want %v: the refund came out of a period that never counted it", got, amount)
	}
	if got := f.spent(t, f.child); got != amount {
		t.Errorf("child spent %v, want %v: the refund still returns to the capability", got, amount)
	}
	// A charge of the new period returns to it.
	if _, err := f.Usage.Refund(f.Ctx, limes.RefundRequest{ChargeID: after.ChargeID, Remaining: true}); err != nil {
		t.Fatal(err)
	}
	if got := f.tenantSpent(t); got != 0 {
		t.Errorf("tenant spent %v after refunding the new period's charge, want 0", got)
	}
}

// checkAncestorUnit holds a store to giving an ancestor counted only through
// a child — by a charge or by a hold — the unit the child counts in, as
// GetUsage reads it back.
func checkAncestorUnit[TX any](t *testing.T, f fixture[TX]) {
	tree := func() (parent, child uuid.UUID) {
		t.Helper()
		parent, err := f.newCapability(uuid.Nil, unlimitedBudget)
		if err != nil {
			t.Fatal(err)
		}
		child, err = f.newCapability(parent, unlimitedBudget)
		if err != nil {
			t.Fatal(err)
		}
		return parent, child
	}
	charged, chargedChild := tree()
	if _, err := f.Usage.Charge(f.Ctx, limes.ChargeRequest{Subject: newSubject(subjectSpec{capID: chargedChild, tenant: f.Tenant, unit: unit}), Amount: one}, nil); err != nil {
		t.Fatal(err)
	}
	held, heldChild := tree()
	if _, err := f.Usage.Reserve(f.Ctx, limes.ReserveRequest{Subject: newSubject(subjectSpec{capID: heldChild, tenant: f.Tenant, unit: unit}), Amount: one}); err != nil {
		t.Fatal(err)
	}
	for name, id := range map[string]uuid.UUID{"charged": charged, "held": held} {
		u, err := f.Usage.GetUsage(f.Ctx, id)
		if err != nil || u.UnitCode != unit {
			t.Errorf("the parent of a %s child: usage %+v, %v; want counted in %q", name, u, err, unit)
		}
	}
}

// checkOneUnit holds a store to counting each counter in one unit: an amount
// in another is not comparable with what is there, and adding it would sum
// two currencies as one.
func checkOneUnit[TX any](t *testing.T, f fixture[TX]) {
	if _, err := f.charge(limes.ChargeRequest{Amount: one}, nil); err != nil {
		t.Fatal(err)
	}
	other := limes.ChargeRequest{Subject: newSubject(subjectSpec{capID: f.child, tenant: f.Tenant, budget: budgetOf(childBudget), unit: otherUnit}), Amount: one}
	if _, err := f.Usage.Charge(f.Ctx, other, nil); !errors.Is(err, limes.ErrUnitCodeMismatch) {
		t.Errorf("a charge in %s on a %s counter: err = %v, want ErrUnitCodeMismatch", otherUnit, unit, err)
	}
	if _, err := f.Usage.Reserve(f.Ctx, limes.ReserveRequest{
		Subject: newSubject(subjectSpec{capID: f.child, tenant: f.Tenant, budget: budgetOf(childBudget), unit: otherUnit}),
		Amount:  one,
	}); !errors.Is(err, limes.ErrUnitCodeMismatch) {
		t.Errorf("a hold in %s on a %s counter: err = %v, want ErrUnitCodeMismatch", otherUnit, unit, err)
	}
	// A fresh capability has no unit of its own yet, but its tenant does.
	fresh, err := f.newCapability(uuid.Nil, unlimitedBudget)
	if err != nil {
		t.Fatal(err)
	}
	other.Subject = newSubject(subjectSpec{capID: fresh, tenant: f.Tenant, unit: otherUnit})
	if _, err := f.Usage.Charge(f.Ctx, other, nil); !errors.Is(err, limes.ErrUnitCodeMismatch) {
		t.Errorf("a charge in %s under a %s tenant: err = %v, want ErrUnitCodeMismatch", otherUnit, unit, err)
	}
	if f.spent(t, f.child) != one || f.spent(t, fresh) != 0 || f.tenantSpent(t) != one {
		t.Error("a refused charge in another unit moved spend")
	}
	b, err := f.Usage.GetTenantBudget(f.Ctx, f.Tenant)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Usage.SetTenantBudget(f.Ctx, limes.SetTenantBudgetRequest{
		TenantID: f.Tenant, Ceiling: limes.BudgetCeiling{Max: tenantBudget}, UnitCode: otherUnit, ExpectedVersion: b.ResourceVersion,
	}); !errors.Is(err, limes.ErrUnitCodeMismatch) {
		t.Errorf("re-denominating a tenant with %s spent: err = %v, want ErrUnitCodeMismatch", unit, err)
	}
}

// checkSettleAfterAncestorDropped holds Settle to the lineage as it stands:
// an ancestor whose row DeleteUsage dropped after the hold was made no
// longer holds it, so the settled amount must fit its ceiling whole, and is
// counted on it once it does.
func checkSettleAfterAncestorDropped[TX any](t *testing.T, f fixture[TX]) {
	// The root holds 8, spends 18 once its row is dropped, and then holds
	// nothing of the hold: settling all 8 would take it to 26, past its 25;
	// settling 7 takes it to exactly 25.
	const held, rootSpend, admitted = 8 * one, 18 * one, 7 * one
	r, err := f.Usage.Reserve(f.Ctx, limes.ReserveRequest{Subject: f.childSubject(), Amount: held})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Usage.DeleteUsage(f.Ctx, limes.DeleteUsageRequest{CapabilityID: f.root}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Usage.Charge(f.Ctx, limes.ChargeRequest{
		Subject: newSubject(subjectSpec{capID: f.root, tenant: f.Tenant, budget: budgetOf(rootBudget)}), Amount: rootSpend,
	}, nil); err != nil {
		t.Fatal(err)
	}
	settle := func(amount limes.Nanos) error {
		_, err := f.Usage.Settle(f.Ctx, limes.SettleRequest{Subject: f.childSubject(), ReservationID: r.ID, Amount: amount}, nil)
		return err
	}
	if err := settle(held); !errors.Is(err, limes.ErrBudgetExceeded) {
		t.Errorf("a settle within its hold but past the root's ceiling, the root's row dropped: err = %v, want ErrBudgetExceeded", err)
	}
	if err := settle(admitted); err != nil {
		t.Fatalf("a settle within the root's ceiling: %v", err)
	}
	if got := f.spent(t, f.root); got != rootSpend+admitted {
		t.Errorf("root spent %v after the settle, want %v counted on it", got, rootSpend+admitted)
	}
}

// checkExactCeilings holds a store to an ancestor's and a tenant's ceiling
// to the nano: a charge reaching one exactly is admitted, a nano more is not.
func checkExactCeilings[TX any](t *testing.T, f fixture[TX]) {
	const nano limes.Nanos = 1
	under, err := f.newCapability(f.root, unlimitedBudget)
	if err != nil {
		t.Fatal(err)
	}
	charge := func(capID uuid.UUID, amount limes.Nanos) error {
		_, err := f.Usage.Charge(f.Ctx, limes.ChargeRequest{Subject: newSubject(subjectSpec{capID: capID, tenant: f.Tenant}), Amount: amount}, nil)
		return err
	}
	if err := charge(under, rootBudget); err != nil {
		t.Errorf("a charge taking the root to its ceiling exactly: %v", err)
	}
	if err := charge(under, nano); !errors.Is(err, limes.ErrBudgetExceeded) {
		t.Errorf("a nano past the root's ceiling: err = %v, want ErrBudgetExceeded", err)
	}
	other, err := f.newCapability(uuid.Nil, unlimitedBudget)
	if err != nil {
		t.Fatal(err)
	}
	if err := charge(other, tenantBudget-rootBudget); err != nil {
		t.Errorf("a charge taking the tenant to its ceiling exactly: %v", err)
	}
	if err := charge(other, nano); !errors.Is(err, limes.ErrTenantBudgetExceeded) {
		t.Errorf("a nano past the tenant's ceiling: err = %v, want ErrTenantBudgetExceeded", err)
	}
}

// checkHoldCreatesTenant holds a store to counting a hold on a tenant whose
// first act it is: a ceiling set afterwards must see it.
func checkHoldCreatesTenant[TX any](t *testing.T, f fixture[TX]) {
	tenant, err := f.newTenant()
	if err != nil {
		t.Fatal(err)
	}
	id, err := f.NewCapability(CapabilitySpec{Tenant: tenant, Requests: limes.RequestCeiling{Unlimited: true}, Budget: unlimitedBudget})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Usage.Reserve(f.tenantCtx(tenant), limes.ReserveRequest{Subject: newSubject(subjectSpec{capID: id, tenant: tenant}), Amount: one}); err != nil {
		t.Fatal(err)
	}
	b, err := f.Usage.GetTenantBudget(f.tenantCtx(tenant), tenant)
	if err != nil || b.ReservedAmount != one {
		t.Errorf("a tenant whose first act is a hold: %+v, %v; want %v held", b, err, one)
	}
}

// checkSettleReplay holds a store to a settle replayed: the same receipt,
// and onCharged not run again, so a side effect committed with the first is
// not committed twice.
func checkSettleReplay[TX any](t *testing.T, f fixture[TX]) {
	r, err := f.Usage.Reserve(f.Ctx, limes.ReserveRequest{Subject: f.childSubject(), Amount: 2 * one, Op: "llm", Actor: "agent"})
	if err != nil {
		t.Fatal(err)
	}
	req := limes.SettleRequest{Subject: f.childSubject(), ReservationID: r.ID, Amount: one}
	first, err := f.Usage.Settle(f.Ctx, req, nil)
	if err != nil {
		t.Fatal(err)
	}
	ran := false
	again, err := f.Usage.Settle(f.Ctx, req, func(context.Context, TX) error { ran = true; return nil })
	if err != nil || !again.Replayed || again.ChargeID != first.ChargeID || again.SpentAmount != first.SpentAmount {
		t.Errorf("a replayed settle = %+v, %v; want the first's receipt %+v, replayed", again, err, first)
	}
	if ran {
		t.Error("a replayed settle ran onCharged again")
	}
	charge, err := f.Usage.GetCharge(f.Ctx, first.ChargeID)
	if err != nil || charge.Op != "llm" || charge.Actor != "agent" {
		t.Errorf("the settled charge = %+v, %v; want the hold's op and actor", charge, err)
	}
}

// checkReleaseReturnsCopy holds a store to returning a hold to the copy it
// was made under, by Release and by ReleaseExpired: a hold left on the copy
// blocks it for good.
func checkReleaseReturnsCopy[TX any](t *testing.T, f fixture[TX]) {
	reader, ok := f.Usage.(limes.CopyUsageReader)
	if !ok {
		return // a Meter that does not read copies back is checked no further
	}
	const ttl = time.Minute
	for name, release := range map[string]func(id uuid.UUID) error{
		"Release": func(id uuid.UUID) error {
			return f.Usage.Release(f.Ctx, limes.ReleaseRequest{ReservationID: id})
		},
		"ReleaseExpired": func(uuid.UUID) error {
			f.Advance(2 * ttl)
			_, err := f.Usage.ReleaseExpired(f.Ctx, limes.ReleaseExpiredRequest{})
			return err
		},
	} {
		if name == "ReleaseExpired" && f.Advance == nil {
			continue
		}
		copyID := []byte("copy-" + name)
		copies := []limes.CopyCeiling{{RevocationID: copyID, Budget: &limes.BudgetCeiling{Max: childBudget}}}
		r, err := f.Usage.Reserve(f.Ctx, limes.ReserveRequest{Subject: newSubject(subjectSpec{capID: f.child, tenant: f.Tenant, copies: copies}), Amount: one, TTL: ttl})
		if err != nil {
			t.Fatal(err)
		}
		if err := release(r.ID); err != nil {
			t.Fatal(err)
		}
		if got := f.copyUsage(t, reader, copyID); len(got) != 1 || got[0].ReservedAmount != 0 {
			t.Errorf("the copy after %s = %+v; want nothing held", name, got)
		}
	}
}

// checkRefusedBump holds a store to a Bump refused by a copy's request
// ceiling moving no counter.
func checkRefusedBump[TX any](t *testing.T, f fixture[TX]) {
	frozen := []limes.CopyCeiling{{RevocationID: []byte("frozen"), Requests: &limes.RequestCeiling{}}}
	if _, err := f.Usage.Bump(f.Ctx, limes.BumpRequest{Subject: newSubject(subjectSpec{capID: f.child, tenant: f.Tenant, copies: frozen})}); !errors.Is(err, limes.ErrRequestLimitExceeded) {
		t.Fatalf("a bump past a frozen copy: err = %v, want ErrRequestLimitExceeded", err)
	}
	for name, id := range map[string]uuid.UUID{"capability": f.child, "root": f.root} {
		u, err := f.Usage.GetUsage(f.Ctx, id)
		if err != nil && !errors.Is(err, limes.ErrUsageNotFound) {
			t.Fatal(err)
		}
		if u.RequestCount != 0 {
			t.Errorf("the %s counted %d requests after a refused bump, want 0", name, u.RequestCount)
		}
	}
}

// checkRangeCountsHolds holds a store to the counter range with what is held
// counted: holds summing past MaxNanos would leave a counter it cannot
// keep, so the one that would is ErrCounterOverflow.
func checkRangeCountsHolds[TX any](t *testing.T, f fixture[TX]) {
	tenant, err := f.newTenant()
	if err != nil {
		t.Fatal(err)
	}
	id, err := f.NewCapability(CapabilitySpec{Tenant: tenant, Requests: limes.RequestCeiling{Unlimited: true}, Budget: unlimitedBudget})
	if err != nil {
		t.Fatal(err)
	}
	reserve := func(amount limes.Nanos) error {
		_, err := f.Usage.Reserve(f.tenantCtx(tenant), limes.ReserveRequest{Subject: newSubject(subjectSpec{capID: id, tenant: tenant}), Amount: amount})
		return err
	}
	if err := reserve(limes.MaxNanos); err != nil {
		t.Fatal(err)
	}
	if err := reserve(1); !errors.Is(err, limes.ErrCounterOverflow) {
		t.Errorf("a hold past MaxNanos held: err = %v, want ErrCounterOverflow", err)
	}
}

// checkRefundExactRemainder holds a store to a refund of an Amount exactly
// what is left of the charge: it is refunded whole, not refused.
func checkRefundExactRemainder[TX any](t *testing.T, f fixture[TX]) {
	const charged, first = 3 * one, one
	receipt, err := f.charge(limes.ChargeRequest{Amount: charged}, nil)
	if err != nil {
		t.Fatal(err)
	}
	refund := func(amount limes.Nanos) (limes.RefundReceipt, error) {
		return f.Usage.Refund(f.Ctx, limes.RefundRequest{ChargeID: receipt.ChargeID, Amount: amount})
	}
	if _, err := refund(first); err != nil {
		t.Fatal(err)
	}
	got, err := refund(charged - first)
	if err != nil || got.Amount != charged-first {
		t.Errorf("a refund of exactly what is left = %+v, %v; want %v refunded", got, err, charged-first)
	}
	if spent := f.spent(t, f.child); spent != 0 {
		t.Errorf("spent %v after refunding the whole charge, want 0", spent)
	}
}

// checkSettleAfterRemovedAncestor holds a store to writing no row for an
// ancestor that PurgeOrphans has dropped: a hold settled afterwards counts
// on the lineage still on record, and the removed one stays gone.
func checkSettleAfterRemovedAncestor[TX any](t *testing.T, f fixture[TX]) {
	if f.DeleteCapability == nil {
		t.Skip("Env.DeleteCapability not set")
	}
	r, err := f.Usage.Reserve(f.Ctx, limes.ReserveRequest{Subject: f.childSubject(), Amount: one})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.DeleteCapability(f.root); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Usage.PurgeOrphans(f.Ctx, limes.PurgeOrphansRequest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Usage.Settle(f.Ctx, limes.SettleRequest{Subject: f.childSubject(), ReservationID: r.ID, Amount: one}, nil); err != nil {
		t.Fatal(err)
	}
	if u, err := f.Usage.GetUsage(f.Ctx, f.root); !errors.Is(err, limes.ErrUsageNotFound) {
		t.Errorf("the removed root after a settle: %+v, %v; want no row", u, err)
	}
}

// checkReplayAfterRemoval holds a store to answering a replay the same way
// for a charge and a settle once the capability is off record: each gets the
// receipt it was given, Replayed, and moves nothing.
func checkReplayAfterRemoval[TX any](t *testing.T, f fixture[TX]) {
	if f.DeleteCapability == nil {
		t.Skip("Env.DeleteCapability not set")
	}
	const ref = "cost-1"
	charge := limes.ChargeRequest{Subject: f.childSubject(), Amount: one, ExternalRef: ref}
	charged, err := f.Usage.Charge(f.Ctx, charge, nil)
	if err != nil {
		t.Fatal(err)
	}
	r, err := f.Usage.Reserve(f.Ctx, limes.ReserveRequest{Subject: f.childSubject(), Amount: one})
	if err != nil {
		t.Fatal(err)
	}
	settle := limes.SettleRequest{Subject: f.childSubject(), ReservationID: r.ID, Amount: one}
	settled, err := f.Usage.Settle(f.Ctx, settle, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.DeleteCapability(f.child); err != nil {
		t.Fatal(err)
	}
	if got, err := f.Usage.Charge(f.Ctx, charge, nil); err != nil || !got.Replayed || got.ChargeID != charged.ChargeID {
		t.Errorf("a charge replayed after its capability is gone = %+v, %v; want the first receipt, replayed", got, err)
	}
	if got, err := f.Usage.Settle(f.Ctx, settle, nil); err != nil || !got.Replayed || got.ChargeID != settled.ChargeID {
		t.Errorf("a settle replayed after its capability is gone = %+v, %v; want the first receipt, replayed", got, err)
	}
}

// checkDeleteReleasesHolds holds a store to releasing what a reaped
// capability held: nothing can settle or release a hold of a capability that
// is gone, so a hold left behind counts against its ancestors and its tenant
// until the store is rebuilt.
func checkDeleteReleasesHolds[TX any](t *testing.T, f fixture[TX]) {
	res, err := f.Usage.Reserve(f.Ctx, limes.ReserveRequest{
		Subject: f.childSubject(),
		Amount:  3 * one,
	})
	if err != nil {
		t.Fatal(err)
	}
	// A capability with no row is no error: there is nothing to drop.
	if err := f.Usage.DeleteUsage(f.Ctx, limes.DeleteUsageRequest{CapabilityID: uuid.New()}); err != nil {
		t.Errorf("DeleteUsage of a capability with no row: %v", err)
	}
	if err := f.Usage.DeleteUsage(f.Ctx, limes.DeleteUsageRequest{CapabilityID: f.child}); err != nil {
		t.Fatalf("DeleteUsage: %v", err)
	}
	if _, err := f.Usage.GetReservation(f.Ctx, res.ID); !errors.Is(err, limes.ErrReservationNotFound) {
		t.Errorf("the deleted capability's hold: err = %v, want ErrReservationNotFound", err)
	}
	if u, err := f.Usage.GetUsage(f.Ctx, f.root); err != nil || u.ReservedAmount != 0 {
		t.Errorf("root after deleting its child = %+v, %v; want nothing held", u, err)
	}
	if b, err := f.Usage.GetTenantBudget(f.Ctx, f.Tenant); err != nil || b.ReservedAmount != 0 {
		t.Errorf("tenant after deleting the child = %+v, %v; want nothing held", b, err)
	}
}

// GetTenantBudget, SetTenantBudget and ListTenantBudgets read one row; each
// returns all of it.
func checkTenantBudgetReads[TX any](t *testing.T, f fixture[TX]) {
	const held = 3 * one
	if _, err := f.Usage.Reserve(f.Ctx, limes.ReserveRequest{
		Subject: f.childSubject(),
		Amount:  held,
	}); err != nil {
		t.Fatal(err)
	}
	got, err := f.Usage.GetTenantBudget(f.Ctx, f.Tenant)
	if err != nil || got.ReservedAmount != held || got.ResourceVersion == 0 {
		t.Fatalf("GetTenantBudget = %+v, %v; want %v held and a version", got, err, held)
	}
	set, err := f.Usage.SetTenantBudget(f.Ctx, limes.SetTenantBudgetRequest{
		TenantID: f.Tenant, Ceiling: limes.BudgetCeiling{Max: tenantBudget}, UnitCode: unit, ExpectedVersion: got.ResourceVersion,
	})
	// The version is an opaque token: a write changes it, and it is never 0
	// once a ceiling is set — nothing says by how much it moves.
	if err != nil || set.ReservedAmount != held || set.ResourceVersion == got.ResourceVersion || set.ResourceVersion == 0 {
		t.Errorf("SetTenantBudget = %+v, %v; want %v held and a new version", set, err, held)
	}
	// Nothing is spent and the period is open-ended; every other field holds
	// a value, so one the store does not keep reads back unset.
	open := []string{"Ceiling.Unlimited", "SpentAmount", "PeriodEnd"}
	if unset := fields.Unset(set); !slices.Equal(unset, open) {
		t.Errorf("SetTenantBudget = %+v: %v unset, want only %v", set, unset, open)
	}
	if read, err := f.Usage.GetTenantBudget(f.Ctx, f.Tenant); err != nil {
		t.Errorf("GetTenantBudget after a set: %v", err)
	} else if diff := sameTenantBudget(read, set); diff != "" {
		t.Errorf("GetTenantBudget = %+v, SetTenantBudget returned %+v: %s", read, set, diff)
	}
	for _, ceiling := range []limes.Nanos{-1, limes.MaxNanos + 1} {
		if _, err := f.Usage.SetTenantBudget(f.Ctx, limes.SetTenantBudgetRequest{
			TenantID: f.Tenant, Ceiling: limes.BudgetCeiling{Max: ceiling}, UnitCode: unit, ExpectedVersion: set.ResourceVersion,
		}); !errors.Is(err, limes.ErrInvalidAmount) {
			t.Errorf("SetTenantBudget(%s): err = %v, want ErrInvalidAmount", ceiling, err)
		}
	}
	listedPage, err := f.Usage.ListTenantBudgets(f.listCtx(), limes.ListTenantBudgetsRequest{})
	listed := listedPage.Items
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(listed, func(s limes.TenantBudget) bool { return s.TenantID == f.Tenant })
	if i < 0 {
		t.Fatalf("ListTenantBudgets left out the tenant: %+v", listed)
	}
	if diff := sameTenantBudget(listed[i], set); diff != "" {
		t.Errorf("listed budget = %+v, SetTenantBudget returned %+v: %s", listed[i], set, diff)
	}
}

// sameReservation names how got differs from want, or returns "". Every
// field counts, so one the store does not keep is a difference; the expiry
// compares as an instant, whatever its zone.
func sameReservation(got, want limes.Reservation) string {
	if !got.ExpiresAt.Equal(want.ExpiresAt) {
		return "expiry differs"
	}
	got.ExpiresAt = want.ExpiresAt
	if !reflect.DeepEqual(got, want) {
		return "fields differ"
	}
	return ""
}

// sameTenantBudget names how got differs from want, or returns "". Every
// field counts; the times compare as instants.
func sameTenantBudget(got, want limes.TenantBudget) string {
	for _, f := range []struct {
		name      string
		got, want time.Time
	}{
		{"PeriodStart", got.PeriodStart, want.PeriodStart},
		{"PeriodEnd", got.PeriodEnd, want.PeriodEnd},
		{"UpdatedAt", got.UpdatedAt, want.UpdatedAt},
	} {
		if !f.got.Equal(f.want) {
			return f.name + " differs"
		}
	}
	got.PeriodStart, got.PeriodEnd, got.UpdatedAt = want.PeriodStart, want.PeriodEnd, want.UpdatedAt
	if !reflect.DeepEqual(got, want) {
		return "fields differ"
	}
	return ""
}

// sameCeiling reports whether two optional ceilings are both absent or
// both present and equal.
func sameCeiling[T comparable](a, b *T) bool {
	return (a == nil) == (b == nil) && (a == nil || *a == *b)
}

// Reserve, GetReservation and ListReservations read one hold the same way,
// and a settled or released hold is gone from both.
func checkReservations[TX any](t *testing.T, f fixture[TX]) {
	const (
		held   = 2 * one
		sooner = time.Minute
		later  = 2 * time.Minute
		// copyLimit holds every reservation below, through the one copy.
		copyLimit = 10
	)
	copies := []limes.CopyCeiling{{
		RevocationID: []byte("copy-1"), Requests: &limes.RequestCeiling{Max: copyLimit}, Budget: &limes.BudgetCeiling{Max: copyLimit * one},
	}}
	reserve := func(capID uuid.UUID, ceiling limes.Nanos, ttl time.Duration) limes.Reservation {
		t.Helper()
		r, err := f.Usage.Reserve(f.Ctx, limes.ReserveRequest{
			Subject: newSubject(subjectSpec{capID: capID, tenant: f.Tenant, budget: budgetOf(ceiling), unit: unit, copies: copies}),
			Amount:  held,
			TTL:     ttl,
			Op:      "llm",
			Actor:   "agent",
		})
		if err != nil {
			t.Fatalf("reserve: %v", err)
		}
		return r
	}
	first := reserve(f.child, childBudget, sooner)
	second := reserve(f.child, childBudget, later)
	onRoot := reserve(f.root, rootBudget, sooner)
	if first.CapabilityID != f.child || first.TenantID != f.Tenant || first.Amount != held ||
		first.Op != "llm" || first.Actor != "agent" || len(first.Copies) != 1 || !sameCeiling(first.Copies[0].Budget, copies[0].Budget) {
		t.Errorf("Reserve returned %+v; want the whole hold, copies with their budgets alone", first)
	}
	// The copy's budget has a Max; every other field of this hold holds a
	// value, so one the store does not keep reads back unset.
	if unset, want := fields.Unset(first), []string{"Copies[0].Budget.Unlimited"}; !slices.Equal(unset, want) {
		t.Errorf("Reserve returned %+v with %v unset, want only %v", first, unset, want)
	}

	got, err := f.Usage.GetReservation(f.Ctx, first.ID)
	if err != nil {
		t.Fatalf("GetReservation: %v", err)
	}
	if diff := sameReservation(got, first); diff != "" {
		t.Errorf("GetReservation = %+v, Reserve returned %+v: %s", got, first, diff)
	}
	list := func(capID uuid.UUID) []uuid.UUID {
		t.Helper()
		rs, err := f.Usage.ListReservations(f.Ctx, limes.ListReservationsRequest{CapabilityID: capID})
		if err != nil {
			t.Fatalf("ListReservations: %v", err)
		}
		ids := []uuid.UUID{}
		for _, r := range rs.Items {
			ids = append(ids, r.ID)
		}
		return ids
	}
	if got := list(f.child); !slices.Equal(got, []uuid.UUID{first.ID, second.ID}) {
		t.Errorf("the child's holds = %v, want %v soonest first", got, []uuid.UUID{first.ID, second.ID})
	}
	if got := list(f.root); !slices.Equal(got, []uuid.UUID{onRoot.ID}) {
		t.Errorf("the root's holds = %v, want only its own %v", got, onRoot.ID)
	}

	// A hold is settled by the copy that made it: not by the capability
	// without it, nor under another copy, whose own limits it would shed.
	other := []limes.CopyCeiling{{RevocationID: []byte("copy-2")}}
	for name, c := range map[string][]limes.CopyCeiling{"no copy": nil, "another copy": other} {
		subject := newSubject(subjectSpec{capID: f.child, tenant: f.Tenant, budget: budgetOf(childBudget), unit: unit, copies: c})
		if _, err := f.Usage.Settle(f.Ctx, limes.SettleRequest{Subject: subject, ReservationID: first.ID, Amount: held}, nil); !errors.Is(err, limes.ErrReservationNotFound) {
			t.Errorf("a copy's hold settled under %s: err = %v, want ErrReservationNotFound", name, err)
		}
	}
	byCopy := newSubject(subjectSpec{capID: f.child, tenant: f.Tenant, budget: budgetOf(childBudget), unit: unit, copies: copies})
	if _, err := f.Usage.Settle(f.Ctx, limes.SettleRequest{Subject: byCopy, ReservationID: first.ID, Amount: held}, nil); err != nil {
		t.Fatal(err)
	}
	// Settled, the hold's receipt reads back to its copy alone.
	if r, err := f.Usage.Settle(f.Ctx, limes.SettleRequest{Subject: byCopy, ReservationID: first.ID, Amount: held}, nil); err != nil || !r.Replayed {
		t.Errorf("the copy settling its hold again = %+v, %v; want the replay", r, err)
	}
	another := newSubject(subjectSpec{capID: f.child, tenant: f.Tenant, budget: budgetOf(childBudget), unit: unit, copies: other})
	if _, err := f.Usage.Settle(f.Ctx, limes.SettleRequest{Subject: another, ReservationID: first.ID, Amount: held}, nil); !errors.Is(err, limes.ErrReservationNotFound) {
		t.Errorf("another copy settling a settled hold: err = %v, want ErrReservationNotFound, not the receipt", err)
	}
	if err := f.Usage.Release(f.Ctx, limes.ReleaseRequest{ReservationID: second.ID}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []uuid.UUID{first.ID, second.ID, uuid.New()} {
		if _, err := f.Usage.GetReservation(f.Ctx, id); !errors.Is(err, limes.ErrReservationNotFound) {
			t.Errorf("GetReservation %s: err = %v, want ErrReservationNotFound", id, err)
		}
	}
	if got := list(f.child); len(got) != 0 {
		t.Errorf("holds left after settling and releasing: %v", got)
	}
}

// checkCopyUsageRefusesInvalid holds a CopyUsageReader to the request's
// bounds: no copy, more than MaxCopyUsageIDs, or an empty id is refused.
func checkCopyUsageRefusesInvalid[TX any](t *testing.T, f fixture[TX]) {
	reader, ok := f.Usage.(limes.CopyUsageReader)
	if !ok {
		return // a Meter that does not read copies back is checked no further
	}
	tooMany := make([][]byte, limes.MaxCopyUsageIDs+1)
	for i := range tooMany {
		tooMany[i] = []byte(strconv.Itoa(i))
	}
	for name, ids := range map[string][][]byte{
		"no id":    nil,
		"too many": tooMany,
		"empty id": {[]byte("copy-1"), {}},
	} {
		if _, err := reader.CopyUsage(f.Ctx, limes.CopyUsageRequest{RevocationIDs: ids}); !errors.Is(err, limes.ErrInvalidRequest) {
			t.Errorf("CopyUsage with %s: err = %v, want ErrInvalidRequest", name, err)
		}
	}
}

// checkCopyCountersGo holds a CopyUsageReader to naming the capability a
// copy counts for, and to dropping the copy's counters with the
// capability's usage: by DeleteUsage, and by PurgeOrphans once the
// capability is off record.
func checkCopyCountersGo[TX any](t *testing.T, f fixture[TX]) {
	reader, ok := f.Usage.(limes.CopyUsageReader)
	if !ok {
		return // a Meter that does not read copies back is checked no further
	}
	// requests is each copy's ceiling: the one request it is counted with.
	const requests = 1
	counted := func(capID uuid.UUID, copyID []byte) []limes.CopyUsage {
		t.Helper()
		copies := []limes.CopyCeiling{{RevocationID: copyID, Requests: &limes.RequestCeiling{Max: requests}}}
		if _, err := f.Usage.Bump(f.Ctx, limes.BumpRequest{Subject: newSubject(subjectSpec{capID: capID, tenant: f.Tenant, copies: copies})}); err != nil {
			t.Fatal(err)
		}
		return f.copyUsage(t, reader, copyID)
	}
	deleted := []byte("copy-deleted")
	if got := counted(f.child, deleted); len(got) != 1 || got[0].CapabilityID != f.child {
		t.Fatalf("copy usage = %+v; want one copy counting for %s", got, f.child)
	}
	if err := f.Usage.DeleteUsage(f.Ctx, limes.DeleteUsageRequest{CapabilityID: f.child}); err != nil {
		t.Fatal(err)
	}
	if got := f.copyUsage(t, reader, deleted); len(got) != 0 {
		t.Errorf("copy usage after DeleteUsage of its capability = %+v; want none", got)
	}

	if f.DeleteCapability == nil {
		return // PurgeOrphans needs a capability off record
	}
	orphan, err := f.newCapability(uuid.Nil, unlimitedBudget)
	if err != nil {
		t.Fatal(err)
	}
	purged := []byte("copy-purged")
	counted(orphan, purged)
	if err := f.DeleteCapability(orphan); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Usage.PurgeOrphans(f.Ctx, limes.PurgeOrphansRequest{}); err != nil {
		t.Fatal(err)
	}
	if got := f.copyUsage(t, reader, purged); len(got) != 0 {
		t.Errorf("copy usage after PurgeOrphans of its capability = %+v; want none", got)
	}
}

// copyUsage is what reader holds for the copy copyID.
func (f fixture[TX]) copyUsage(t *testing.T, reader limes.CopyUsageReader, copyID []byte) []limes.CopyUsage {
	t.Helper()
	res, err := reader.CopyUsage(f.Ctx, limes.CopyUsageRequest{RevocationIDs: [][]byte{copyID}})
	if err != nil {
		t.Fatal(err)
	}
	return res.Copies
}

func checkCopyOverrun[TX any](t *testing.T, f fixture[TX]) {
	copyID := []byte("copy-1")
	copies := []limes.CopyCeiling{{RevocationID: copyID, Budget: &limes.BudgetCeiling{Max: one}}}
	if _, err := f.charge(limes.ChargeRequest{Subject: newSubject(subjectSpec{copies: copies}), Amount: 2 * one}, nil); !errors.Is(err, limes.ErrBudgetExceeded) {
		t.Fatalf("rejecting charge past the copy's budget: err = %v, want ErrBudgetExceeded", err)
	}
	r, err := f.charge(limes.ChargeRequest{Subject: newSubject(subjectSpec{copies: copies}), Amount: 2 * one, Overrun: limes.OverrunRecord}, nil)
	if err != nil || !r.Overrun {
		t.Fatalf("recording charge = %+v, %v; want an overrun", r, err)
	}
	reader, ok := f.Usage.(limes.CopyUsageReader)
	if !ok {
		return // a Meter that does not read copies back is checked no further
	}
	res, err := reader.CopyUsage(f.Ctx, limes.CopyUsageRequest{RevocationIDs: [][]byte{copyID}})
	got := res.Copies
	if err != nil || len(got) != 1 || got[0].SpentAmount != 2*one {
		t.Errorf("copy usage = %+v, %v; want 2 spent", got, err)
	}
}

// checkSumsExactly holds a store to exact amounts: a tenth and two tenths fit
// under a ceiling of three tenths with not a nano to spare, and seven tenths
// add up to seven tenths — the sums a float64 gets wrong.
func checkSumsExactly[TX any](t *testing.T, f fixture[TX]) {
	tenth, threeTenths := one/10, 3*one/10
	id, err := f.newCapability(uuid.Nil, limes.BudgetCeiling{Max: threeTenths})
	if err != nil {
		t.Fatal(err)
	}
	for _, amount := range []limes.Nanos{tenth, 2 * tenth} {
		if _, err := f.charge(limes.ChargeRequest{Subject: newSubject(subjectSpec{capID: id, budget: budgetOf(threeTenths)}), Amount: amount}, nil); err != nil {
			t.Fatalf("charging %s under a ceiling of %s: %v", amount, threeTenths, err)
		}
	}
	if got := f.spent(t, id); got != threeTenths {
		t.Errorf("spent %s, want exactly %s", got, threeTenths)
	}
	if _, err := f.charge(limes.ChargeRequest{Subject: newSubject(subjectSpec{capID: id, budget: budgetOf(threeTenths)}), Amount: 1}, nil); !errors.Is(err, limes.ErrBudgetExceeded) {
		t.Errorf("a nano past the ceiling: err = %v, want ErrBudgetExceeded", err)
	}
	for range 7 {
		if _, err := f.charge(limes.ChargeRequest{Subject: newSubject(subjectSpec{capID: f.root, budget: budgetOf(rootBudget)}), Amount: tenth}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if got := f.spent(t, f.root); got != 7*tenth {
		t.Errorf("seven tenths spent as %s", got)
	}
}

// checkConcurrentCeiling holds a store to checking and moving a ceiling as
// one step: charges racing to the last of a budget must not all see room for
// themselves. A read-then-write without a lock is the bug this catches.
func checkConcurrentCeiling[TX any](t *testing.T, f fixture[TX]) {
	var (
		wg       sync.WaitGroup
		accepted atomic.Int64
	)
	for range concurrentDeliveries {
		wg.Go(func() {
			_, err := f.charge(limes.ChargeRequest{Amount: 2 * one}, nil)
			switch {
			case err == nil:
				accepted.Add(1)
			case !errors.Is(err, limes.ErrBudgetExceeded):
				t.Errorf("charge: %v", err)
			}
		})
	}
	wg.Wait()
	// 20 fits ten charges of 2, and twenty arrive.
	if got, want := accepted.Load(), int64(childBudget/(2*one)); got != want {
		t.Errorf("%d charges of 2 accepted under a ceiling of 20, want %d", got, want)
	}
	if got := f.spent(t, f.child); got != childBudget {
		t.Errorf("child spent %v, want exactly its ceiling %v", got, childBudget)
	}
}

// checkAncestorCeiling holds a store to reading each ancestor's ceiling from
// its record: two children each within their own budget must not sum past
// their parent's.
func checkAncestorCeiling[TX any](t *testing.T, f fixture[TX]) {
	sibling, err := f.newCapability(f.root, limes.BudgetCeiling{Max: childBudget})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.charge(limes.ChargeRequest{Amount: 15 * one}, nil); err != nil {
		t.Fatal(err)
	}
	// 15 + 15 is within each child's 20, past the root's 25.
	_, err = f.charge(limes.ChargeRequest{Subject: newSubject(subjectSpec{capID: sibling}), Amount: 15 * one}, nil)
	if !errors.Is(err, limes.ErrBudgetExceeded) {
		t.Fatalf("a sibling's charge past the root's ceiling: err = %v, want ErrBudgetExceeded", err)
	}
	if f.spent(t, sibling) != 0 || f.spent(t, f.root) != 15*one {
		t.Error("a charge refused by an ancestor's ceiling moved spend")
	}
}

// checkAncestorRequests holds Bump to an ancestor's request ceiling: a child
// with none of its own still stops at its root's.
func checkAncestorRequests[TX any](t *testing.T, f fixture[TX]) {
	const limit = 1
	root, err := f.NewCapability(CapabilitySpec{Requests: limes.RequestCeiling{Max: limit}, Budget: unlimitedBudget})
	if err != nil {
		t.Fatal(err)
	}
	child, err := f.newCapability(root, unlimitedBudget)
	if err != nil {
		t.Fatal(err)
	}
	bump := func() error {
		_, err := f.Usage.Bump(f.Ctx, limes.BumpRequest{Subject: newSubject(subjectSpec{capID: child, tenant: f.Tenant})})
		return err
	}
	for range limit {
		if err := bump(); err != nil {
			t.Fatalf("a bump within the root's ceiling: %v", err)
		}
	}
	if err := bump(); !errors.Is(err, limes.ErrRequestLimitExceeded) {
		t.Errorf("a bump past the root's ceiling: err = %v, want ErrRequestLimitExceeded", err)
	}
}

// checkCopyRequests holds Bump to a copy's request ceiling, which binds the
// copy while the capability's own allows more.
func checkCopyRequests[TX any](t *testing.T, f fixture[TX]) {
	const limit = 1
	copies := []limes.CopyCeiling{{RevocationID: []byte("copy-requests"), Requests: &limes.RequestCeiling{Max: limit}}}
	bump := func() error {
		_, err := f.Usage.Bump(f.Ctx, limes.BumpRequest{Subject: newSubject(subjectSpec{capID: f.child, tenant: f.Tenant, copies: copies})})
		return err
	}
	for range limit {
		if err := bump(); err != nil {
			t.Fatalf("a bump within the copy's ceiling: %v", err)
		}
	}
	if err := bump(); !errors.Is(err, limes.ErrRequestLimitExceeded) {
		t.Errorf("a bump past the copy's ceiling: err = %v, want ErrRequestLimitExceeded", err)
	}
}

// checkCopyHold holds a store to counting a hold against the copy it was
// made under, as against every other ceiling.
func checkCopyHold[TX any](t *testing.T, f fixture[TX]) {
	copies := []limes.CopyCeiling{{RevocationID: []byte("copy-hold"), Budget: &limes.BudgetCeiling{Max: one}}}
	subject := newSubject(subjectSpec{capID: f.child, tenant: f.Tenant, budget: budgetOf(childBudget), copies: copies})
	if _, err := f.Usage.Reserve(f.Ctx, limes.ReserveRequest{Subject: subject, Amount: one}); err != nil {
		t.Fatalf("a hold of the copy's whole budget: %v", err)
	}
	if _, err := f.charge(limes.ChargeRequest{Subject: subject, Amount: cent}, nil); !errors.Is(err, limes.ErrBudgetExceeded) {
		t.Errorf("a charge past the copy's budget with the hold: err = %v, want ErrBudgetExceeded", err)
	}
}

// checkStaleVersion holds SetTenantBudget to its optimistic-concurrency
// guard: a write carrying a version another write has moved past is
// refused, and leaves the ceiling as it was.
func checkStaleVersion[TX any](t *testing.T, f fixture[TX]) {
	b, err := f.Usage.GetTenantBudget(f.Ctx, f.Tenant)
	if err != nil {
		t.Fatal(err)
	}
	set := func(max limes.Nanos) error {
		_, err := f.Usage.SetTenantBudget(f.Ctx, limes.SetTenantBudgetRequest{
			TenantID: f.Tenant, Ceiling: limes.BudgetCeiling{Max: max}, UnitCode: unit, ExpectedVersion: b.ResourceVersion,
		})
		return err
	}
	if err := set(tenantBudget + one); err != nil {
		t.Fatalf("a write at the current version: %v", err)
	}
	if err := set(tenantBudget + 2*one); !errors.Is(err, limes.ErrTenantBudgetVersionMismatch) {
		t.Errorf("a write at a stale version: err = %v, want ErrTenantBudgetVersionMismatch", err)
	}
	if got, err := f.Usage.GetTenantBudget(f.Ctx, f.Tenant); err != nil || got.Ceiling != (limes.BudgetCeiling{Max: tenantBudget + one}) {
		t.Errorf("after a stale write the ceiling = %v, %v; want %s", got.Ceiling, err, tenantBudget+one)
	}
}

// checkZeroCeilings holds a store to a ceiling of 0 allowing nothing — on
// the subject's requests and budget, on a hold, on an ancestor and on the
// tenant — rather than standing for no limit, which only Unlimited does.
func checkZeroCeilings[TX any](t *testing.T, f fixture[TX]) {
	zero := newSubject(subjectSpec{capID: f.child, tenant: f.Tenant, requests: requestsOf(0), budget: budgetOf(0)})
	if _, err := f.Usage.Bump(f.Ctx, limes.BumpRequest{Subject: zero}); !errors.Is(err, limes.ErrRequestLimitExceeded) {
		t.Errorf("a bump under a request ceiling of 0: err = %v, want ErrRequestLimitExceeded", err)
	}
	if _, err := f.charge(limes.ChargeRequest{Subject: zero, Amount: cent}, nil); !errors.Is(err, limes.ErrBudgetExceeded) {
		t.Errorf("a charge under a budget of 0: err = %v, want ErrBudgetExceeded", err)
	}
	if _, err := f.Usage.Reserve(f.Ctx, limes.ReserveRequest{Subject: zero, Amount: cent}); !errors.Is(err, limes.ErrBudgetExceeded) {
		t.Errorf("a hold under a budget of 0: err = %v, want ErrBudgetExceeded", err)
	}

	frozen, err := f.newCapability(uuid.Nil, limes.BudgetCeiling{})
	if err != nil {
		t.Fatal(err)
	}
	under, err := f.newCapability(frozen, unlimitedBudget)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.charge(limes.ChargeRequest{Subject: newSubject(subjectSpec{capID: under}), Amount: cent}, nil); !errors.Is(err, limes.ErrBudgetExceeded) {
		t.Errorf("a charge under an ancestor's budget of 0: err = %v, want ErrBudgetExceeded", err)
	}
	if f.spent(t, f.child) != 0 || f.spent(t, under) != 0 || f.tenantSpent(t) != 0 {
		t.Error("a charge refused by a ceiling of 0 moved spend")
	}

	b, err := f.Usage.GetTenantBudget(f.Ctx, f.Tenant)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Usage.SetTenantBudget(f.Ctx, limes.SetTenantBudgetRequest{TenantID: f.Tenant, UnitCode: unit, ExpectedVersion: b.ResourceVersion}); err != nil {
		t.Fatalf("tenant ceiling of 0: %v", err)
	}
	if _, err := f.charge(limes.ChargeRequest{Subject: newSubject(subjectSpec{capID: f.root}), Amount: cent}, nil); !errors.Is(err, limes.ErrTenantBudgetExceeded) {
		t.Errorf("a charge under a tenant ceiling of 0: err = %v, want ErrTenantBudgetExceeded", err)
	}
}

// checkRelease holds Release to returning a hold to every counter it
// counted against: the capability's, each ancestor's and the tenant's.
func checkRelease[TX any](t *testing.T, f fixture[TX]) {
	r, err := f.Usage.Reserve(f.Ctx, limes.ReserveRequest{Subject: f.childSubject(), Amount: 15 * one})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Usage.Release(f.Ctx, limes.ReleaseRequest{ReservationID: r.ID}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []uuid.UUID{f.child, f.root} {
		if u, err := f.Usage.GetUsage(f.Ctx, id); err == nil && u.ReservedAmount != 0 {
			t.Errorf("%s still holds %s after Release", id, u.ReservedAmount)
		}
	}
	if b, err := f.Usage.GetTenantBudget(f.Ctx, f.Tenant); err != nil || b.ReservedAmount != 0 {
		t.Errorf("the tenant still holds %s after Release, %v", b.ReservedAmount, err)
	}
	if _, err := f.charge(limes.ChargeRequest{Amount: 10 * one}, nil); err != nil {
		t.Errorf("a charge the released hold made room for: %v", err)
	}
}

// checkResetKeepsHolds holds a store to ResetSpend resetting the spend only:
// a hold still open counts against the tenant's new period.
func checkResetKeepsHolds[TX any](t *testing.T, f fixture[TX]) {
	const held = 10 * one
	if _, err := f.Usage.Reserve(f.Ctx, limes.ReserveRequest{Subject: f.childSubject(), Amount: held}); err != nil {
		t.Fatal(err)
	}
	b, err := f.Usage.GetTenantBudget(f.Ctx, f.Tenant)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Usage.SetTenantBudget(f.Ctx, limes.SetTenantBudgetRequest{
		TenantID: f.Tenant, Ceiling: limes.BudgetCeiling{Max: tenantBudget}, UnitCode: unit, ResetSpend: true, ExpectedVersion: b.ResourceVersion,
	}); err != nil {
		t.Fatal(err)
	}
	if b, err := f.Usage.GetTenantBudget(f.Ctx, f.Tenant); err != nil || b.ReservedAmount != held {
		t.Errorf("after a reset the tenant holds %s, %v; want %s", b.ReservedAmount, err, held)
	}
	other, err := f.newCapability(uuid.Nil, unlimitedBudget)
	if err != nil {
		t.Fatal(err)
	}
	// 10 held + 25 is past the tenant's 30.
	if _, err := f.charge(limes.ChargeRequest{Subject: newSubject(subjectSpec{capID: other}), Amount: 25 * one}, nil); !errors.Is(err, limes.ErrTenantBudgetExceeded) {
		t.Errorf("a charge past the tenant's ceiling with the hold: err = %v, want ErrTenantBudgetExceeded", err)
	}
}

// checkDescendantHolds holds an ancestor's ceiling to counting what its
// descendants hold, not only what they spent.
func checkDescendantHolds[TX any](t *testing.T, f fixture[TX]) {
	sibling, err := f.newCapability(f.root, limes.BudgetCeiling{Max: childBudget})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Usage.Reserve(f.Ctx, limes.ReserveRequest{Subject: f.childSubject(), Amount: 15 * one}); err != nil {
		t.Fatal(err)
	}
	// 15 held by the child + 15 is past the root's 25.
	_, err = f.charge(limes.ChargeRequest{Subject: newSubject(subjectSpec{capID: sibling, budget: budgetOf(childBudget)}), Amount: 15 * one}, nil)
	if !errors.Is(err, limes.ErrBudgetExceeded) {
		t.Errorf("a sibling's charge past the root's ceiling with the child's hold: err = %v, want ErrBudgetExceeded", err)
	}
}

// checkSettleAboveHold holds Settle to counting the hold once: settling 20
// of a 19 hold adds 1 to every counter, so it fits a ceiling of 20 — the
// capability's, a copy's — with nothing to spare.
func checkSettleAboveHold[TX any](t *testing.T, f fixture[TX]) {
	copies := []limes.CopyCeiling{{RevocationID: []byte("settle-copy"), Budget: &limes.BudgetCeiling{Max: childBudget}}}
	subject := newSubject(subjectSpec{capID: f.child, tenant: f.Tenant, budget: budgetOf(childBudget), copies: copies})
	r, err := f.Usage.Reserve(f.Ctx, limes.ReserveRequest{Subject: subject, Amount: childBudget - one})
	if err != nil {
		t.Fatal(err)
	}
	got, err := f.Usage.Settle(f.Ctx, limes.SettleRequest{Subject: subject, ReservationID: r.ID, Amount: childBudget}, nil)
	if err != nil || got.Overrun {
		t.Fatalf("settling the whole ceiling over a hold just under it = %+v, %v; want accepted, no overrun", got, err)
	}
	if spent := f.spent(t, f.child); spent != childBudget {
		t.Errorf("the child spent %s, want %s", spent, childBudget)
	}
	for _, id := range []uuid.UUID{f.child, f.root} {
		if u, err := f.Usage.GetUsage(f.Ctx, id); err != nil || u.ReservedAmount != 0 {
			t.Errorf("%s holds %v after the settle, %v; want nothing", id, u.ReservedAmount, err)
		}
	}
}

// checkNestedCopySettle holds Settle to every copy of a hold, not only the
// outermost: copies run innermost first, so a child copy C of P presents
// [C, P], and settling P's hold under [C, P] — or C's under [P] — would move
// spend past a copy's own limits.
func checkNestedCopySettle[TX any](t *testing.T, f fixture[TX]) {
	p := limes.CopyCeiling{RevocationID: []byte("copy-p"), Budget: &limes.BudgetCeiling{Max: childBudget}}
	c := limes.CopyCeiling{RevocationID: []byte("copy-c"), Budget: &limes.BudgetCeiling{Max: one}}
	under := func(copies ...limes.CopyCeiling) limes.MeterSubject {
		return newSubject(subjectSpec{capID: f.child, tenant: f.Tenant, budget: budgetOf(childBudget), copies: copies})
	}
	for name, tc := range map[string]struct{ held, settling limes.MeterSubject }{
		"the parent's hold by the child": {under(p), under(c, p)},
		"the child's hold by the parent": {under(c, p), under(p)},
	} {
		r, err := f.Usage.Reserve(f.Ctx, limes.ReserveRequest{Subject: tc.held, Amount: one})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Usage.Settle(f.Ctx, limes.SettleRequest{Subject: tc.settling, ReservationID: r.ID, Amount: one}, nil); !errors.Is(err, limes.ErrReservationNotFound) {
			t.Errorf("%s: err = %v, want ErrReservationNotFound", name, err)
		}
	}
}

// checkUnitWithHolds holds a store to keeping a tenant's unit while a hold in
// it is open: the hold could never settle in another.
func checkUnitWithHolds[TX any](t *testing.T, f fixture[TX]) {
	if _, err := f.Usage.Reserve(f.Ctx, limes.ReserveRequest{Subject: f.childSubject(), Amount: one}); err != nil {
		t.Fatal(err)
	}
	b, err := f.Usage.GetTenantBudget(f.Ctx, f.Tenant)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.Usage.SetTenantBudget(f.Ctx, limes.SetTenantBudgetRequest{
		TenantID: f.Tenant, Ceiling: limes.BudgetCeiling{Max: tenantBudget}, UnitCode: otherUnit, ResetSpend: true, ExpectedVersion: b.ResourceVersion,
	})
	if !errors.Is(err, limes.ErrUnitCodeMismatch) {
		t.Errorf("a unit change with a hold open: err = %v, want ErrUnitCodeMismatch", err)
	}
}

// checkChainUnits holds a store to every counter on the chain keeping its
// unit, not only the tenant's: once the tenant moves to another unit, a
// capability that counted in the first — or whose ancestor did — refuses an
// amount in the second.
func checkChainUnits[TX any](t *testing.T, f fixture[TX]) {
	parent, err := f.newCapability(uuid.Nil, unlimitedBudget)
	if err != nil {
		t.Fatal(err)
	}
	child, err := f.newCapability(parent, unlimitedBudget)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.charge(limes.ChargeRequest{Subject: newSubject(subjectSpec{capID: parent}), Amount: one}, nil); err != nil {
		t.Fatal(err)
	}
	b, err := f.Usage.GetTenantBudget(f.Ctx, f.Tenant)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Usage.SetTenantBudget(f.Ctx, limes.SetTenantBudgetRequest{
		TenantID: f.Tenant, Ceiling: limes.BudgetCeiling{Max: tenantBudget}, UnitCode: otherUnit, ResetSpend: true, ExpectedVersion: b.ResourceVersion,
	}); err != nil {
		t.Fatal(err)
	}
	for name, id := range map[string]uuid.UUID{"its own counter": parent, "its ancestor's counter": child} {
		_, err := f.Usage.Charge(f.Ctx, limes.ChargeRequest{Subject: newSubject(subjectSpec{capID: id, tenant: f.Tenant, unit: otherUnit}), Amount: one}, nil)
		if !errors.Is(err, limes.ErrUnitCodeMismatch) {
			t.Errorf("an amount in %s against %s in %s: err = %v, want ErrUnitCodeMismatch", otherUnit, name, unit, err)
		}
	}
}

// checkUnclampedRanking holds ListTenantBudgets to ranking by spend over the
// ceiling unclamped: of two tenants past their ceilings, the further leads,
// whatever their ids.
func checkUnclampedRanking[TX any](t *testing.T, f fixture[TX]) {
	const ceiling = 10 * one
	type overspender struct{ tenant, capability uuid.UUID }
	newOverspender := func() overspender {
		t.Helper()
		tenant, err := f.newTenant()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Usage.SetTenantBudget(f.tenantCtx(tenant), limes.SetTenantBudgetRequest{TenantID: tenant, Ceiling: limes.BudgetCeiling{Max: ceiling}, UnitCode: unit}); err != nil {
			t.Fatal(err)
		}
		id, err := f.NewCapability(CapabilitySpec{Tenant: tenant, Requests: limes.RequestCeiling{Unlimited: true}, Budget: unlimitedBudget})
		if err != nil {
			t.Fatal(err)
		}
		return overspender{tenant, id}
	}
	nearer, further := newOverspender(), newOverspender()
	// The further gets the higher id: clamped, the two would tie and the
	// id would put it second.
	if bytes.Compare(further.tenant[:], nearer.tenant[:]) < 0 {
		nearer, further = further, nearer
	}
	for o, spent := range map[overspender]limes.Nanos{nearer: 2 * ceiling, further: 3 * ceiling} {
		if _, err := f.Usage.Charge(f.tenantCtx(o.tenant), limes.ChargeRequest{
			Subject: newSubject(subjectSpec{capID: o.capability, tenant: o.tenant}), Amount: spent, Overrun: limes.OverrunRecord,
		}, nil); err != nil {
			t.Fatal(err)
		}
	}
	page, err := f.Usage.ListTenantBudgets(f.listCtx(), limes.ListTenantBudgetsRequest{Filter: limes.TenantBudgetsAtOrPast, ThresholdPct: fullPct})
	if err != nil {
		t.Fatal(err)
	}
	at := func(tenant uuid.UUID) int {
		return slices.IndexFunc(page.Items, func(r limes.TenantBudget) bool { return r.TenantID == tenant })
	}
	if fi, ni := at(further.tenant), at(nearer.tenant); fi < 0 || ni < 0 || fi > ni {
		t.Errorf("listed the tenant at 300%% at %d and the one at 200%% at %d; want the further first", fi, ni)
	}
}

// checkThresholdBoundary holds ThresholdPct to spent / max × 100 ≥ threshold
// exactly: 29 of 100 is at 29%, which a float quotient reads as just under.
func checkThresholdBoundary[TX any](t *testing.T, f fixture[TX]) {
	// 29 of 100 at 29%, and 11 of 1000 at 1.1%: a threshold is the decimal
	// written, not the float64 a hair above or below it.
	for _, tc := range []struct {
		ceiling, spent limes.Nanos
		threshold      float64
	}{
		{100 * one, 29 * one, 29},
		{1000 * one, 11 * one, 1.1},
	} {
		tenant, err := f.newTenant()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Usage.SetTenantBudget(f.tenantCtx(tenant), limes.SetTenantBudgetRequest{TenantID: tenant, Ceiling: limes.BudgetCeiling{Max: tc.ceiling}, UnitCode: unit}); err != nil {
			t.Fatal(err)
		}
		id, err := f.NewCapability(CapabilitySpec{Tenant: tenant, Requests: limes.RequestCeiling{Unlimited: true}, Budget: unlimitedBudget})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Usage.Charge(f.tenantCtx(tenant), limes.ChargeRequest{Subject: newSubject(subjectSpec{capID: id, tenant: tenant}), Amount: tc.spent}, nil); err != nil {
			t.Fatal(err)
		}
		page, err := f.Usage.ListTenantBudgets(f.listCtx(), limes.ListTenantBudgetsRequest{Filter: limes.TenantBudgetsAtOrPast, ThresholdPct: tc.threshold})
		if err != nil {
			t.Fatal(err)
		}
		if !slices.ContainsFunc(page.Items, func(r limes.TenantBudget) bool { return r.TenantID == tenant }) {
			t.Errorf("a tenant at exactly %v%% was not listed at a threshold of %v%%", tc.threshold, tc.threshold)
		}
	}
}

// checkExactRanking holds ListTenantBudgets to ranking by spent / max
// exactly: a third and a nano past a third are one float64, and the second
// must still lead, whatever the tenant ids.
func checkExactRanking[TX any](t *testing.T, f fixture[TX]) {
	const third = limes.MaxNanos / 3
	type spec struct {
		tenant         uuid.UUID
		ceiling, spent limes.Nanos
	}
	tenants := make([]uuid.UUID, 2)
	for i := range tenants {
		id, err := f.newTenant()
		if err != nil {
			t.Fatal(err)
		}
		tenants[i] = id
	}
	// The further gets the higher id: ranked by a float, the two tie and the
	// id puts it second.
	slices.SortFunc(tenants, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
	nearer, further := spec{tenants[0], 3, 1}, spec{tenants[1], 3 * third, third + 1}
	for _, s := range []spec{nearer, further} {
		if _, err := f.Usage.SetTenantBudget(f.tenantCtx(s.tenant), limes.SetTenantBudgetRequest{TenantID: s.tenant, Ceiling: limes.BudgetCeiling{Max: s.ceiling}, UnitCode: unit}); err != nil {
			t.Fatal(err)
		}
		id, err := f.NewCapability(CapabilitySpec{Tenant: s.tenant, Requests: limes.RequestCeiling{Unlimited: true}, Budget: unlimitedBudget})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Usage.Charge(f.tenantCtx(s.tenant), limes.ChargeRequest{Subject: newSubject(subjectSpec{capID: id, tenant: s.tenant}), Amount: s.spent}, nil); err != nil {
			t.Fatal(err)
		}
	}
	page, err := f.Usage.ListTenantBudgets(f.listCtx(), limes.ListTenantBudgetsRequest{Filter: limes.TenantBudgetsAtOrPast})
	if err != nil {
		t.Fatal(err)
	}
	at := func(tenant uuid.UUID) int {
		return slices.IndexFunc(page.Items, func(r limes.TenantBudget) bool { return r.TenantID == tenant })
	}
	if fi, ni := at(further.tenant), at(nearer.tenant); fi < 0 || ni < 0 || fi > ni {
		t.Errorf("listed a nano past a third at %d and a third at %d; want the further first", fi, ni)
	}
}

// checkHousekeepingBatches holds ReleaseExpired and PurgeOrphans to their
// Limit: a batch of one does one.
func checkHousekeepingBatches[TX any](t *testing.T, f fixture[TX]) {
	if f.Advance == nil {
		t.Skip("Env.Advance is not set")
	}
	const rows, batch = 2, 1
	for range rows {
		if _, err := f.Usage.Reserve(f.Ctx, limes.ReserveRequest{Subject: f.childSubject(), Amount: one, TTL: time.Minute}); err != nil {
			t.Fatal(err)
		}
	}
	f.Advance(time.Hour)
	if r, err := f.Usage.ReleaseExpired(f.Ctx, limes.ReleaseExpiredRequest{Limit: batch}); err != nil || r.Released != batch {
		t.Errorf("ReleaseExpired with a limit of %d = %+v, %v", batch, r, err)
	}
	if f.DeleteCapability == nil {
		return
	}
	var gone []uuid.UUID
	for range rows {
		id, err := f.newCapability(uuid.Nil, unlimitedBudget)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.charge(limes.ChargeRequest{Subject: newSubject(subjectSpec{capID: id}), Amount: one}, nil); err != nil {
			t.Fatal(err)
		}
		gone = append(gone, id)
	}
	for _, id := range gone {
		if err := f.DeleteCapability(id); err != nil {
			t.Fatal(err)
		}
	}
	if r, err := f.Usage.PurgeOrphans(f.Ctx, limes.PurgeOrphansRequest{Limit: batch}); err != nil || r.Purged != batch {
		t.Errorf("PurgeOrphans with a limit of %d = %+v, %v", batch, r, err)
	}
}

// checkCopySettleRefund holds a store's copy counters to following Settle and
// Refund as every other counter does: a settle moves the hold to spend, a
// refund returns it.
func checkCopySettleRefund[TX any](t *testing.T, f fixture[TX]) {
	reader, ok := f.Usage.(limes.CopyUsageReader)
	if !ok {
		t.Skip("the store reads no copy counters back")
	}
	copyID := []byte("copy-settle-refund")
	copies := []limes.CopyCeiling{{RevocationID: copyID, Budget: &limes.BudgetCeiling{Max: childBudget}}}
	subject := newSubject(subjectSpec{capID: f.child, tenant: f.Tenant, budget: budgetOf(childBudget), copies: copies})
	r, err := f.Usage.Reserve(f.Ctx, limes.ReserveRequest{Subject: subject, Amount: 2 * one})
	if err != nil {
		t.Fatal(err)
	}
	settled, err := f.Usage.Settle(f.Ctx, limes.SettleRequest{Subject: subject, ReservationID: r.ID, Amount: 3 * one}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Usage.Refund(f.Ctx, limes.RefundRequest{ChargeID: settled.ChargeID, Amount: one}); err != nil {
		t.Fatal(err)
	}
	res, err := reader.CopyUsage(f.Ctx, limes.CopyUsageRequest{RevocationIDs: [][]byte{copyID}})
	got := res.Copies
	if err != nil || len(got) != 1 || got[0].SpentAmount != 2*one || got[0].ReservedAmount != 0 {
		t.Errorf("the copy after a settle of 3 and a refund of 1 = %+v, %v; want 2 spent, nothing held", got, err)
	}
}

// checkTenantCeiling holds a store to the tenant aggregate: capabilities each
// within their own budgets must not sum past the tenant's.
func checkTenantCeiling[TX any](t *testing.T, f fixture[TX]) {
	other, err := f.newCapability(uuid.Nil, limes.BudgetCeiling{Max: rootBudget})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.charge(limes.ChargeRequest{Subject: newSubject(subjectSpec{capID: f.root, budget: budgetOf(rootBudget)}), Amount: 20 * one}, nil); err != nil {
		t.Fatal(err)
	}
	// 20 + 20 is within each root's 25, past the tenant's 30.
	_, err = f.charge(limes.ChargeRequest{Subject: newSubject(subjectSpec{capID: other, budget: budgetOf(rootBudget)}), Amount: 20 * one}, nil)
	if !errors.Is(err, limes.ErrTenantBudgetExceeded) {
		t.Fatalf("a charge past the tenant's ceiling: err = %v, want ErrTenantBudgetExceeded", err)
	}
	if f.spent(t, other) != 0 || f.tenantSpent(t) != 20*one {
		t.Error("a charge refused by the tenant's ceiling moved spend")
	}
}

// checkCallbackRollback holds a store to the atomicity of onCharged: its
// error is returned as it is, and no counter, ledger row or ExternalRef
// survives it.
func checkCallbackRollback[TX any](t *testing.T, f fixture[TX]) {
	boom := errors.New("side effect failed")
	if _, err := f.charge(limes.ChargeRequest{Amount: one, ExternalRef: "call-1"}, func(context.Context, TX) error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("charge with a failing callback: err = %v, want the callback's", err)
	}
	if f.spent(t, f.child) != 0 || f.spent(t, f.root) != 0 || f.tenantSpent(t) != 0 {
		t.Error("a charge whose callback failed moved spend")
	}
	if _, err := f.Usage.ChargeByRef(f.Ctx, limes.ChargeByRefRequest{CapabilityID: f.child, ExternalRef: "call-1"}); !errors.Is(err, limes.ErrChargeNotFound) {
		t.Errorf("the failed charge's ref: err = %v, want ErrChargeNotFound", err)
	}
	// The ref is free again: a retry charges.
	if r, err := f.charge(limes.ChargeRequest{Amount: one, ExternalRef: "call-1"}, nil); err != nil || r.Replayed {
		t.Errorf("the retry = %+v, %v; want a fresh charge", r, err)
	}

	res, err := f.Usage.Reserve(f.Ctx, limes.ReserveRequest{
		Subject: f.childSubject(),
		Amount:  2 * one,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Usage.Settle(f.Ctx, limes.SettleRequest{Subject: f.childSubject(), ReservationID: res.ID, Amount: 2 * one},
		func(context.Context, TX) error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("settle with a failing callback: err = %v, want the callback's", err)
	}
	if _, err := f.Usage.GetReservation(f.Ctx, res.ID); err != nil {
		t.Errorf("the reservation after a failed settle: %v; want it still open", err)
	}
	if got := f.spent(t, f.child); got != one {
		t.Errorf("child spent %v after a failed settle, want 1", got)
	}
}

// checkBump holds a store to counting requests on the capability and every
// ancestor, and to refusing past the capability's own limit without counting.
func checkBump[TX any](t *testing.T, f fixture[TX]) {
	const limit = 2
	bump := func() (int64, error) {
		r, err := f.Usage.Bump(f.Ctx, limes.BumpRequest{Subject: newSubject(subjectSpec{capID: f.child, tenant: f.Tenant, requests: requestsOf(limit)})})
		return r.RequestCount, err
	}
	for want := int64(1); want <= limit; want++ {
		if n, err := bump(); err != nil || n != want {
			t.Fatalf("bump = %d, %v; want %d", n, err, want)
		}
	}
	if _, err := bump(); !errors.Is(err, limes.ErrRequestLimitExceeded) {
		t.Fatalf("a bump past the limit: err = %v, want ErrRequestLimitExceeded", err)
	}
	// Counting requests alone records the subject's unit, so a reader
	// shows the counters in it rather than in none.
	for _, id := range []uuid.UUID{f.child, f.root} {
		if u, err := f.Usage.GetUsage(f.Ctx, id); err != nil || u.RequestCount != limit || u.UnitCode != unit {
			t.Errorf("usage of %s = %+v, %v; want %d requests in %s", id, u, err, limit, unit)
		}
	}
}

// checkRefund holds a store to the Refund contract: back to every counter
// the charge moved, never past what it took, and a full refund idempotent.
func checkRefund[TX any](t *testing.T, f fixture[TX]) {
	r, err := f.charge(limes.ChargeRequest{Amount: 3 * one}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.TenantID != f.Tenant || r.UnitCode != unit {
		t.Errorf("charge receipt names tenant %s in %q, want %s in %q", r.TenantID, r.UnitCode, f.Tenant, unit)
	}
	refund := func(amount limes.Nanos) (limes.RefundReceipt, error) {
		return f.Usage.Refund(f.Ctx, limes.RefundRequest{ChargeID: r.ChargeID, Amount: amount})
	}
	if got, err := refund(one); err != nil || got.Amount != one || got.ChargeID != r.ChargeID ||
		got.TenantID != f.Tenant || got.UnitCode != unit {
		t.Fatalf("partial refund = %+v, %v; want 1 of charge %s, tenant %s in %q", got, err, r.ChargeID, f.Tenant, unit)
	}
	if _, err := refund(3 * one); !errors.Is(err, limes.ErrRefundExceedsCharge) {
		t.Errorf("refunding past what is left: err = %v, want ErrRefundExceedsCharge", err)
	}
	for name, req := range map[string]limes.RefundRequest{
		"neither an amount nor Remaining": {ChargeID: r.ChargeID},
		"an amount and Remaining":         {ChargeID: r.ChargeID, Amount: one, Remaining: true},
	} {
		if _, err := f.Usage.Refund(f.Ctx, req); !errors.Is(err, limes.ErrInvalidRequest) {
			t.Errorf("refund of %s: err = %v, want ErrInvalidRequest", name, err)
		}
	}
	for range 2 { // the rest, then nothing: a full refund is idempotent
		if _, err := f.Usage.Refund(f.Ctx, limes.RefundRequest{ChargeID: r.ChargeID, Remaining: true}); err != nil {
			t.Fatalf("full refund: %v", err)
		}
	}
	if f.spent(t, f.child) != 0 || f.spent(t, f.root) != 0 || f.tenantSpent(t) != 0 {
		t.Error("a full refund left spend on a counter the charge moved")
	}
	if _, err := f.Usage.Refund(f.Ctx, limes.RefundRequest{ChargeID: uuid.New(), Remaining: true}); !errors.Is(err, limes.ErrChargeNotFound) {
		t.Errorf("refund of an unknown charge: err = %v, want ErrChargeNotFound", err)
	}
}

// checkHoldBlocksCharge holds a store to counting holds against charges as
// spend is counted.
func checkHoldBlocksCharge[TX any](t *testing.T, f fixture[TX]) {
	if _, err := f.Usage.Reserve(f.Ctx, limes.ReserveRequest{
		Subject: f.childSubject(),
		Amount:  15 * one,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.charge(limes.ChargeRequest{Amount: 10 * one}, nil); !errors.Is(err, limes.ErrBudgetExceeded) {
		t.Errorf("a charge past the ceiling with the hold: err = %v, want ErrBudgetExceeded", err)
	}
	if _, err := f.charge(limes.ChargeRequest{Amount: 5 * one}, nil); err != nil {
		t.Errorf("a charge within the ceiling with the hold: %v", err)
	}
}

// checkExpiry holds a store to letting a reservation lapse: past its TTL it
// cannot be settled, still counts until ReleaseExpired runs, and then does not.
func checkExpiry[TX any](t *testing.T, f fixture[TX]) {
	if f.Advance == nil {
		t.Skip("Env.Advance is not set")
	}
	const ttl = time.Minute
	res, err := f.Usage.Reserve(f.Ctx, limes.ReserveRequest{
		Subject: f.childSubject(),
		Amount:  15 * one,
		TTL:     ttl,
	})
	if err != nil {
		t.Fatal(err)
	}
	// A hold that outlives the clock's step: ReleaseExpired leaves it.
	long, err := f.Usage.Reserve(f.Ctx, limes.ReserveRequest{
		Subject: newSubject(subjectSpec{capID: f.root, tenant: f.Tenant, budget: budgetOf(rootBudget)}),
		Amount:  one,
		TTL:     time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.Advance(2 * ttl)
	if _, err := f.Usage.Settle(f.Ctx, limes.SettleRequest{Subject: f.childSubject(), ReservationID: res.ID, Amount: one}, nil); !errors.Is(err, limes.ErrReservationNotFound) {
		t.Errorf("settling an expired reservation: err = %v, want ErrReservationNotFound", err)
	}
	if _, err := f.Usage.GetReservation(f.Ctx, res.ID); err != nil {
		t.Errorf("an expired hold not yet released still reads back: %v", err)
	}
	if _, err := f.charge(limes.ChargeRequest{Amount: 10 * one}, nil); !errors.Is(err, limes.ErrBudgetExceeded) {
		t.Errorf("before ReleaseExpired the hold still counts: err = %v, want ErrBudgetExceeded", err)
	}
	if r, err := f.Usage.ReleaseExpired(f.Ctx, limes.ReleaseExpiredRequest{}); err != nil || r.Released != 1 {
		t.Fatalf("ReleaseExpired = %+v, %v; want 1 released", r, err)
	}
	if _, err := f.Usage.GetReservation(f.Ctx, long.ID); err != nil {
		t.Errorf("ReleaseExpired released a hold that has not expired: %v", err)
	}
	if _, err := f.charge(limes.ChargeRequest{Amount: 10 * one}, nil); err != nil {
		t.Errorf("after ReleaseExpired: %v", err)
	}
}

// checkListTenantBudgets holds a store to ListTenantBudgets' order, filters
// and paging: most at risk first, then by tenant id, a page at a time.
func checkListTenantBudgets[TX any](t *testing.T, f fixture[TX]) {
	for name, req := range map[string]limes.ListTenantBudgetsRequest{
		"a negative limit":              {Limit: -1},
		"a threshold past 100%":         {Filter: limes.TenantBudgetsAtOrPast, ThresholdPct: 101},
		"a negative threshold":          {Filter: limes.TenantBudgetsAtOrPast, ThresholdPct: -1},
		"a threshold with no filter":    {ThresholdPct: 1},
		"a threshold beside unlimited":  {Filter: limes.TenantBudgetsUnlimited, ThresholdPct: 1},
		"a filter this package lacks":   {Filter: limes.TenantBudgetsUnlimited + 1},
		"a threshold that is no number": {Filter: limes.TenantBudgetsAtOrPast, ThresholdPct: math.NaN()},
	} {
		if _, err := f.Usage.ListTenantBudgets(f.listCtx(), req); !errors.Is(err, limes.ErrInvalidRequest) {
			t.Errorf("ListTenantBudgets with %s: err = %v, want ErrInvalidRequest", name, err)
		}
	}
	// The fixture's tenant has a ceiling of 30 and spends half of it; two
	// more have ceilings and spend nothing, so they tie and their ids order
	// them; and one more is unlimited.
	if _, err := f.charge(limes.ChargeRequest{Subject: newSubject(subjectSpec{capID: f.root, budget: budgetOf(rootBudget)}), Amount: 15 * one}, nil); err != nil {
		t.Fatal(err)
	}
	newTenant := func(ceiling limes.BudgetCeiling) uuid.UUID {
		t.Helper()
		id, err := f.newTenant()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Usage.SetTenantBudget(f.tenantCtx(id), limes.SetTenantBudgetRequest{TenantID: id, Ceiling: ceiling, UnitCode: unit}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	for range tiedTenants {
		newTenant(limes.BudgetCeiling{Max: tenantBudget})
	}
	unlimited := newTenant(unlimitedBudget)
	frozen := newTenant(limes.BudgetCeiling{})
	all := map[uuid.UUID]bool{}
	var order []limes.TenantBudget
	cursor := ""
	// One page per tenant listed, and one more to see the end: a cursor that
	// does not advance fails here rather than looping forever.
	const listed = 1 + tiedTenants + 2
	for page := 0; ; page++ {
		if page > listed {
			t.Fatalf("paging did not end after %d pages of one tenant each", listed)
		}
		pagePage, err := f.Usage.ListTenantBudgets(f.listCtx(), limes.ListTenantBudgetsRequest{Limit: 1, Cursor: cursor})
		page, next := pagePage.Items, pagePage.Next
		if err != nil {
			t.Fatal(err)
		}
		if len(page) > 1 {
			t.Fatalf("a page of %d rows under a limit of 1", len(page))
		}
		for _, row := range page {
			if all[row.TenantID] {
				t.Errorf("tenant %s listed twice while nothing was spent", row.TenantID)
			}
			all[row.TenantID] = true
			order = append(order, row)
		}
		if next == "" {
			break
		}
		cursor = next
	}
	if !all[f.Tenant] || !all[unlimited] || !all[frozen] {
		t.Fatalf("paging skipped a tenant: %v", all)
	}
	if want := listed; len(order) != want {
		t.Errorf("listed %d tenants, want %d", len(order), want)
	}
	if order[0].TenantID != frozen || order[len(order)-1].TenantID != unlimited {
		t.Errorf("listed %s first and %s last; want the ceiling of 0 %s first and the unlimited %s last",
			order[0].TenantID, order[len(order)-1].TenantID, frozen, unlimited)
	}
	for i := 1; i < len(order); i++ {
		cur, prev := riskOf(order[i]), riskOf(order[i-1])
		switch {
		case prev.after(cur):
			t.Errorf("row %d (%+v) follows one at %+v; want most at risk first", i, cur, prev)
		case cur == prev && bytes.Compare(order[i].TenantID[:], order[i-1].TenantID[:]) < 0:
			t.Errorf("row %d (%s) follows %s at the same risk; want ascending tenant id", i, order[i].TenantID, order[i-1].TenantID)
		}
	}
	const half = 50
	riskyPage, err := f.Usage.ListTenantBudgets(f.listCtx(), limes.ListTenantBudgetsRequest{Filter: limes.TenantBudgetsAtOrPast, ThresholdPct: half})
	risky := riskyPage.Items
	if err != nil || len(risky) != 2 || risky[0].TenantID != frozen || risky[1].TenantID != f.Tenant || riskyPct(risky[1]) != half {
		t.Errorf("tenants at 50%% or more = %+v, %v; want %s, at a ceiling of 0, then %s at 50%%", risky, err, frozen, f.Tenant)
	}
	// At a threshold of 0, every tenant with a ceiling and none without.
	ceiled, err := f.Usage.ListTenantBudgets(f.listCtx(), limes.ListTenantBudgetsRequest{Filter: limes.TenantBudgetsAtOrPast})
	if err != nil || slices.ContainsFunc(ceiled.Items, func(s limes.TenantBudget) bool { return s.TenantID == unlimited }) ||
		!slices.ContainsFunc(ceiled.Items, func(s limes.TenantBudget) bool { return s.TenantID == f.Tenant }) {
		t.Errorf("tenants with a ceiling = %+v, %v; want %s and not %s", ceiled.Items, err, f.Tenant, unlimited)
	}
	unlPage, err := f.Usage.ListTenantBudgets(f.listCtx(), limes.ListTenantBudgetsRequest{Filter: limes.TenantBudgetsUnlimited})
	unl := unlPage.Items
	if err != nil || slices.ContainsFunc(unl, func(s limes.TenantBudget) bool { return s.TenantID == f.Tenant }) ||
		!slices.ContainsFunc(unl, func(s limes.TenantBudget) bool { return s.TenantID == unlimited }) {
		t.Errorf("unlimited tenants = %+v, %v; want %s and not %s", unl, err, unlimited, f.Tenant)
	}
	// TenantBudgetsAll names the filter a request that names none takes.
	named, err := f.Usage.ListTenantBudgets(f.listCtx(), limes.ListTenantBudgetsRequest{Filter: limes.TenantBudgetsAll, Limit: listed})
	if err != nil || len(named.Items) != len(order) {
		t.Fatalf("every tenant, by name = %d rows, %v; want %d", len(named.Items), err, len(order))
	}
	for i, row := range named.Items {
		if row.TenantID != order[i].TenantID {
			t.Errorf("every tenant, by name: row %d is %s, want %s as with no filter", i, row.TenantID, order[i].TenantID)
		}
	}
}

// longestTTL is the longest hold a ReserveRequest can ask for: it expires
// centuries out, past what a count of nanoseconds since 1970 can hold.
const longestTTL = time.Duration(math.MaxInt64)

// checkReservationsTied holds a store to its order among holds that expire
// at the same instant: ascending ID, bytewise, page after page. Ties form
// only when the store's clock stands still between the holds; without one
// there is nothing to order and the check is skipped.
func checkReservationsTied[TX any](t *testing.T, f fixture[TX]) {
	const holds = 4
	var want []limes.Reservation
	for range holds {
		r, err := f.Usage.Reserve(f.Ctx, limes.ReserveRequest{Subject: f.childSubject(), Amount: one, TTL: time.Minute})
		if err != nil {
			t.Fatal(err)
		}
		want = append(want, r)
	}
	slices.SortFunc(want, func(a, b limes.Reservation) int {
		return cmp.Or(a.ExpiresAt.Compare(b.ExpiresAt), bytes.Compare(a.ID[:], b.ID[:]))
	})
	if !slices.ContainsFunc(want[1:], func(r limes.Reservation) bool { return r.ExpiresAt.Equal(want[0].ExpiresAt) }) {
		t.Skip("the store's clock moved between the holds; none expire together")
	}
	var got []uuid.UUID
	cursor := ""
	for range holds + 1 {
		page, err := f.Usage.ListReservations(f.Ctx, limes.ListReservationsRequest{CapabilityID: f.child, Limit: 1, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range page.Items {
			got = append(got, r.ID)
		}
		if page.Next == "" {
			break
		}
		cursor = page.Next
	}
	wantIDs := make([]uuid.UUID, len(want))
	for i, r := range want {
		wantIDs[i] = r.ID
	}
	if !slices.Equal(got, wantIDs) {
		t.Errorf("holds expiring together, paged one at a time = %v, want %v in ascending ID", got, wantIDs)
	}
}

// clockSlack is how far apart a store's clock may read for two calls made
// one after the other: far below any TTL the suite tells apart.
const clockSlack = time.Second

// checkDefaultReservationTTL holds a store to giving a hold with no TTL
// DefaultReservationTTL: it expires when one asking for that TTL outright
// does.
func checkDefaultReservationTTL[TX any](t *testing.T, f fixture[TX]) {
	reserve := func(ttl time.Duration) time.Time {
		t.Helper()
		r, err := f.Usage.Reserve(f.Ctx, limes.ReserveRequest{Subject: f.childSubject(), Amount: one, TTL: ttl})
		if err != nil {
			t.Fatal(err)
		}
		return r.ExpiresAt
	}
	defaulted, explicit := reserve(0), reserve(limes.DefaultReservationTTL)
	if gap := explicit.Sub(defaulted); gap < 0 || gap > clockSlack {
		t.Errorf("a hold with no TTL expires at %v, %v before one of DefaultReservationTTL; want within %v", defaulted, gap, clockSlack)
	}
}

// checkReservationsPage holds a store to paging a capability's reservations
// in their order — the last two expiring past year 2262, where a cursor
// counting Unix nanoseconds overflows — and to refusing a negative page size.
func checkReservationsPage[TX any](t *testing.T, f fixture[TX]) {
	ttls := []time.Duration{time.Minute, longestTTL - time.Minute, longestTTL}
	holds := len(ttls)
	var want []uuid.UUID
	for _, ttl := range ttls {
		r, err := f.Usage.Reserve(f.Ctx, limes.ReserveRequest{
			Subject: f.childSubject(),
			Amount:  one,
			TTL:     ttl,
		})
		if err != nil {
			t.Fatal(err)
		}
		want = append(want, r.ID)
	}
	var got []uuid.UUID
	cursor := ""
	for range holds + 1 {
		page, err := f.Usage.ListReservations(f.Ctx, limes.ListReservationsRequest{CapabilityID: f.child, Limit: 1, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range page.Items {
			got = append(got, r.ID)
		}
		if page.Next == "" {
			break
		}
		cursor = page.Next
	}
	if !slices.Equal(got, want) {
		t.Errorf("paged one at a time, the holds = %v, want %v soonest first", got, want)
	}
	if _, err := f.Usage.ListReservations(f.Ctx, limes.ListReservationsRequest{CapabilityID: f.child, Limit: -1}); !errors.Is(err, limes.ErrInvalidRequest) {
		t.Errorf("ListReservations with a negative limit: err = %v, want ErrInvalidRequest", err)
	}
}

// checkHousekeepingLimits holds a store to refusing a negative batch.
func checkHousekeepingLimits[TX any](t *testing.T, f fixture[TX]) {
	if _, err := f.Usage.ReleaseExpired(f.Ctx, limes.ReleaseExpiredRequest{Limit: -1}); !errors.Is(err, limes.ErrInvalidRequest) {
		t.Errorf("ReleaseExpired with a negative limit: err = %v, want ErrInvalidRequest", err)
	}
	if _, err := f.Usage.PurgeOrphans(f.Ctx, limes.PurgeOrphansRequest{Limit: -1}); !errors.Is(err, limes.ErrInvalidRequest) {
		t.Errorf("PurgeOrphans with a negative limit: err = %v, want ErrInvalidRequest", err)
	}
}

// checkLineageAfterRemoval holds a store to counting only the ancestors
// still on record: a chain MaxDelegationDepth deep charges, takes no child,
// and once its top is removed takes one, which charges like any other.
func checkLineageAfterRemoval[TX any](t *testing.T, f fixture[TX]) {
	if f.DeleteCapability == nil {
		t.Skip("Env.DeleteCapability not set")
	}
	top, err := f.newCapability(uuid.Nil, unlimitedBudget)
	if err != nil {
		t.Fatal(err)
	}
	leaf := top
	for range limes.MaxDelegationDepth {
		if leaf, err = f.newCapability(leaf, unlimitedBudget); err != nil {
			t.Fatal(err)
		}
	}
	charge := func(capID uuid.UUID) error {
		_, err := f.Usage.Charge(f.Ctx, limes.ChargeRequest{Subject: newSubject(subjectSpec{capID: capID, tenant: f.Tenant}), Amount: one}, nil)
		return err
	}
	if err := charge(leaf); err != nil {
		t.Fatalf("a charge with %d ancestors on record: %v", limes.MaxDelegationDepth, err)
	}
	if _, err := f.newCapability(leaf, unlimitedBudget); !errors.Is(err, limes.ErrDelegationTooDeep) {
		t.Fatalf("a capability with %d ancestors on record: err = %v, want ErrDelegationTooDeep", limes.MaxDelegationDepth+1, err)
	}
	if err := f.DeleteCapability(top); err != nil {
		t.Fatal(err)
	}
	deeper, err := f.newCapability(leaf, unlimitedBudget)
	if err != nil {
		t.Fatalf("a capability with %d ancestors on record, the top removed: %v", limes.MaxDelegationDepth, err)
	}
	if err := charge(deeper); err != nil {
		t.Errorf("a charge with %d ancestors on record, the top removed: %v", limes.MaxDelegationDepth, err)
	}
}

// checkPurgeOrphans holds a store to deleting the usage of a capability no
// longer on record, and to returning what it held to its ancestor.
func checkPurgeOrphans[TX any](t *testing.T, f fixture[TX]) {
	if f.DeleteCapability == nil {
		t.Skip("Env.DeleteCapability not set")
	}
	if _, err := f.charge(limes.ChargeRequest{Amount: one}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Usage.Reserve(f.Ctx, limes.ReserveRequest{
		Subject: f.childSubject(),
		Amount:  one,
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.DeleteCapability(f.child); err != nil {
		t.Fatal(err)
	}
	got, err := f.Usage.PurgeOrphans(f.Ctx, limes.PurgeOrphansRequest{})
	if err != nil || got.Purged < 1 {
		t.Fatalf("PurgeOrphans = %+v, %v; want the removed capability's row", got, err)
	}
	if _, err := f.Usage.GetUsage(f.Ctx, f.child); !errors.Is(err, limes.ErrUsageNotFound) {
		t.Errorf("the removed capability's usage outlived PurgeOrphans: %v", err)
	}
	if u, err := f.Usage.GetUsage(f.Ctx, f.root); err != nil || u.ReservedAmount != 0 {
		t.Errorf("the root still holds %v for its removed child, %v; want 0", u.ReservedAmount, err)
	}
}

// checkNotOnRecord holds a store to refusing a capability whose record is
// gone: its ancestors can no longer be read, and metering it alone would let
// it spend past its live root's ceiling.
func checkNotOnRecord[TX any](t *testing.T, f fixture[TX]) {
	if f.DeleteCapability == nil {
		t.Skip("Env.DeleteCapability not set")
	}
	held, err := f.Usage.Reserve(f.Ctx, limes.ReserveRequest{Subject: f.childSubject(), Amount: one})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.DeleteCapability(f.child); err != nil {
		t.Fatal(err)
	}
	subject := f.childSubject()
	if _, err := f.Usage.Settle(f.Ctx, limes.SettleRequest{Subject: subject, ReservationID: held.ID, Amount: one}, nil); !errors.Is(err, limes.ErrNotFound) {
		t.Errorf("a settle of a capability not on record: err = %v, want ErrNotFound", err)
	}
	if _, err := f.Usage.Bump(f.Ctx, limes.BumpRequest{Subject: subject}); !errors.Is(err, limes.ErrNotFound) {
		t.Errorf("a bump of a capability not on record: err = %v, want ErrNotFound", err)
	}
	if _, err := f.Usage.Charge(f.Ctx, limes.ChargeRequest{Subject: subject, Amount: one}, nil); !errors.Is(err, limes.ErrNotFound) {
		t.Errorf("a charge of a capability not on record: err = %v, want ErrNotFound", err)
	}
	if _, err := f.Usage.Reserve(f.Ctx, limes.ReserveRequest{Subject: subject, Amount: one}); !errors.Is(err, limes.ErrNotFound) {
		t.Errorf("a hold of a capability not on record: err = %v, want ErrNotFound", err)
	}
	if u, err := f.Usage.GetUsage(f.Ctx, f.root); err == nil && (u.SpentAmount != 0 || u.RequestCount != 0) {
		t.Errorf("the root counted %+v for a child not on record", u)
	}
}

// riskyPct is a row's utilization, or -1 for a tenant with no ceiling, which
// no threshold should have listed.
// risk is where a tenant ranks in ListTenantBudgets: its ceiling's rank —
// 0 first, bounded next, unlimited last — then, for a bounded ceiling, its
// unclamped utilization.
type risk struct {
	rank int
	pct  float64
}

// The ranks of the kinds of ceiling, most at risk first.
const (
	rankZero = iota
	rankBounded
	rankUnlimited
)

func riskOf(b limes.TenantBudget) risk {
	switch {
	case b.Ceiling.Unlimited:
		return risk{rank: rankUnlimited}
	case b.Ceiling.Max == 0:
		return risk{rank: rankZero}
	}
	return risk{rank: rankBounded, pct: float64(b.SpentAmount) / float64(b.Ceiling.Max) * fullPct}
}

// after reports whether r ranks after o: less at risk.
func (r risk) after(o risk) bool {
	if r.rank != o.rank {
		return r.rank > o.rank
	}
	return r.pct < o.pct
}

// fullPct is a whole, in percent.
const fullPct = 100

func riskyPct(s limes.TenantBudget) float64 {
	pct, limited := s.Utilization()
	if !limited {
		return noUtilization
	}
	return pct
}

// checkPeriodEnd holds a store to keeping a tenant's PeriodEnd as set, and a
// zero one as zero: an open-ended window.
func checkPeriodEnd[TX any](t *testing.T, f fixture[TX]) {
	// Microseconds: the finest a timestamp column keeps.
	end := time.Now().UTC().Add(periodLength).Truncate(time.Microsecond)
	for _, want := range []time.Time{end, {}} {
		cur, err := f.Usage.GetTenantBudget(f.Ctx, f.Tenant)
		if err != nil {
			t.Fatal(err)
		}
		set, err := f.Usage.SetTenantBudget(f.Ctx, limes.SetTenantBudgetRequest{
			TenantID: f.Tenant, Ceiling: limes.BudgetCeiling{Max: tenantBudget}, UnitCode: unit, PeriodEnd: want, ExpectedVersion: cur.ResourceVersion,
		})
		if err != nil || !set.PeriodEnd.Equal(want) {
			t.Fatalf("SetTenantBudget(PeriodEnd %v) = %v, %v", want, set.PeriodEnd, err)
		}
		if got, err := f.Usage.GetTenantBudget(f.Ctx, f.Tenant); err != nil || !got.PeriodEnd.Equal(want) {
			t.Errorf("GetTenantBudget after PeriodEnd %v = %v, %v", want, got.PeriodEnd, err)
		}
	}
}

// checkPeriodStart holds a store to rolling a tenant's PeriodStart to its
// clock's now on ResetSpend, and only then: a new ceiling mid-window keeps
// the window it is in.
func checkPeriodStart[TX any](t *testing.T, f fixture[TX]) {
	if f.Advance == nil {
		t.Skip("Env.Advance is not set")
	}
	const step = time.Hour
	set := func(reset bool) limes.TenantBudget {
		t.Helper()
		f.Advance(step)
		cur, err := f.Usage.GetTenantBudget(f.Ctx, f.Tenant)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Usage.SetTenantBudget(f.Ctx, limes.SetTenantBudgetRequest{
			TenantID: f.Tenant, Ceiling: limes.BudgetCeiling{Max: tenantBudget}, UnitCode: unit, ResetSpend: reset, ExpectedVersion: cur.ResourceVersion,
		}); err != nil {
			t.Fatal(err)
		}
		got, err := f.Usage.GetTenantBudget(f.Ctx, f.Tenant)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	before, err := f.Usage.GetTenantBudget(f.Ctx, f.Tenant)
	if err != nil {
		t.Fatal(err)
	}
	if kept := set(false); !kept.PeriodStart.Equal(before.PeriodStart) {
		t.Errorf("PeriodStart after a new ceiling without ResetSpend = %v, want %v kept", kept.PeriodStart, before.PeriodStart)
	}
	// Two steps on: the one before the kept ceiling and the one before this.
	if rolled := set(true); rolled.PeriodStart.Before(before.PeriodStart.Add(2 * step)) {
		t.Errorf("PeriodStart after ResetSpend = %v, want at least %v, the store's now", rolled.PeriodStart, before.PeriodStart.Add(2*step))
	}
}

// checkSettleWithinHold holds a store to Settle's contract: only an amount
// above the hold must fit the ceilings. A settle at or below it adds nothing
// to what is already counted, so a ceiling crossed meanwhile — by an
// OverrunRecord charge elsewhere in the chain, or a tenant ceiling lowered —
// does not refuse it, and the whole hold is released everywhere it counted.
func checkSettleWithinHold[TX any](t *testing.T, f fixture[TX]) {
	const held, actual = 10 * one, 4 * one
	r, err := f.Usage.Reserve(f.Ctx, limes.ReserveRequest{Subject: f.childSubject(), Amount: held})
	if err != nil {
		t.Fatal(err)
	}
	again, err := f.Usage.Reserve(f.Ctx, limes.ReserveRequest{Subject: f.childSubject(), Amount: one})
	if err != nil {
		t.Fatal(err)
	}
	// The root spends past its ceiling, its child's hold still counted.
	if _, err := f.Usage.Charge(f.Ctx, limes.ChargeRequest{
		Subject: newSubject(subjectSpec{capID: f.root, tenant: f.Tenant, budget: budgetOf(rootBudget)}),
		Amount:  rootBudget,
		Overrun: limes.OverrunRecord,
	}, nil); err != nil {
		t.Fatal(err)
	}
	got, err := f.Usage.Settle(f.Ctx, limes.SettleRequest{Subject: f.childSubject(), ReservationID: r.ID, Amount: actual}, nil)
	if err != nil {
		t.Fatalf("settle below the hold after the root crossed its ceiling: %v", err)
	}
	if got.Overrun {
		t.Error("a settle below its hold reported an overrun")
	}
	for name, id := range map[string]uuid.UUID{"child": f.child, "root": f.root} {
		if u, err := f.Usage.GetUsage(f.Ctx, id); err != nil || u.ReservedAmount != one {
			t.Errorf("%s after the settle = %+v, %v; want only the other hold, %v", name, u, err, one)
		}
	}
	if b, err := f.Usage.GetTenantBudget(f.Ctx, f.Tenant); err != nil || b.ReservedAmount != one {
		t.Errorf("tenant after the settle = %+v, %v; want only the other hold, %v", b, err, one)
	}
	if f.spent(t, f.child) != actual {
		t.Errorf("child spent %v, want %v", f.spent(t, f.child), actual)
	}

	// A tenant ceiling lowered under a hold refuses nothing the hold covers.
	cur, err := f.Usage.GetTenantBudget(f.Ctx, f.Tenant)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Usage.SetTenantBudget(f.Ctx, limes.SetTenantBudgetRequest{
		TenantID: f.Tenant, Ceiling: limes.BudgetCeiling{Max: cent}, UnitCode: unit, ExpectedVersion: cur.ResourceVersion,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Usage.Settle(f.Ctx, limes.SettleRequest{Subject: f.childSubject(), ReservationID: again.ID, Amount: one}, nil); err != nil {
		t.Errorf("settle at the hold after the tenant ceiling was lowered under it: %v", err)
	}
}

// checkZeroSubject holds a store to refusing the zero MeterSubject — one no
// verifier or record made, naming no capability and no tenant — rather than
// counting it under the nil id.
func checkZeroSubject[TX any](t *testing.T, f fixture[TX]) {
	var zero limes.MeterSubject
	if _, err := f.Usage.Bump(f.Ctx, limes.BumpRequest{Subject: zero}); !errors.Is(err, limes.ErrInvalidRequest) {
		t.Errorf("Bump: err = %v, want ErrInvalidRequest", err)
	}
	if _, err := f.Usage.Charge(f.Ctx, limes.ChargeRequest{Subject: zero, Amount: one}, nil); !errors.Is(err, limes.ErrInvalidRequest) {
		t.Errorf("Charge: err = %v, want ErrInvalidRequest", err)
	}
	if _, err := f.Usage.Reserve(f.Ctx, limes.ReserveRequest{Subject: zero, Amount: one}); !errors.Is(err, limes.ErrInvalidRequest) {
		t.Errorf("Reserve: err = %v, want ErrInvalidRequest", err)
	}
	if _, err := f.Usage.Settle(f.Ctx, limes.SettleRequest{Subject: zero, ReservationID: uuid.New(), Amount: one}, nil); !errors.Is(err, limes.ErrInvalidRequest) {
		t.Errorf("Settle: err = %v, want ErrInvalidRequest", err)
	}
	if _, err := f.Usage.GetUsage(f.Ctx, uuid.Nil); !errors.Is(err, limes.ErrUsageNotFound) {
		t.Errorf("GetUsage(nil id) = %v; want ErrUsageNotFound, nothing counted", err)
	}
}

// A call naming Tenant, made under another tenant's context, is refused as
// one: the rows are there, so answering as if they were not would read as a
// bad request or a miss. Nothing it would have moved moves.
func checkScopeMismatch[TX any](t *testing.T, f fixture[TX]) {
	if !f.ScopesTenants {
		t.Skip("Env.ScopesTenants not set")
	}
	other, err := f.newTenant()
	if err != nil {
		t.Fatalf("NewTenant: %v", err)
	}
	held, err := f.Usage.Reserve(f.Ctx, limes.ReserveRequest{Subject: f.childSubject(), Amount: one})
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	before, err := f.Usage.GetUsage(f.Ctx, f.child)
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	wrong := f.tenantCtx(other)
	for name, call := range map[string]func() error{
		"Bump": func() error {
			_, err := f.Usage.Bump(wrong, limes.BumpRequest{Subject: f.childSubject()})
			return err
		},
		"Charge": func() error {
			_, err := f.Usage.Charge(wrong, limes.ChargeRequest{Subject: f.childSubject(), Amount: one}, nil)
			return err
		},
		"Reserve": func() error {
			_, err := f.Usage.Reserve(wrong, limes.ReserveRequest{Subject: f.childSubject(), Amount: one})
			return err
		},
		"Settle": func() error {
			_, err := f.Usage.Settle(wrong, limes.SettleRequest{Subject: f.childSubject(), ReservationID: held.ID, Amount: one}, nil)
			return err
		},
		"GetTenantBudget": func() error {
			_, err := f.Usage.GetTenantBudget(wrong, f.Tenant)
			return err
		},
		"SetTenantBudget": func() error {
			_, err := f.Usage.SetTenantBudget(wrong, limes.SetTenantBudgetRequest{
				TenantID: f.Tenant, Ceiling: limes.BudgetCeiling{Unlimited: true}, UnitCode: unit,
			})
			return err
		},
	} {
		if err := call(); !errors.Is(err, limes.ErrScopeMismatch) {
			t.Errorf("%s for Tenant under another tenant's context: err = %v, want ErrScopeMismatch", name, err)
		}
	}
	after, err := f.Usage.GetUsage(f.Ctx, f.child)
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Errorf("usage after the refused calls = %+v, want %+v", after, before)
	}
	if _, err := f.Usage.GetReservation(f.Ctx, held.ID); err != nil {
		t.Errorf("the hold after a refused Settle: %v; want it open", err)
	}
	b, err := f.Usage.GetTenantBudget(f.Ctx, f.Tenant)
	if err != nil {
		t.Fatalf("tenant budget: %v", err)
	}
	if b.Ceiling != (limes.BudgetCeiling{Max: tenantBudget}) {
		t.Errorf("tenant ceiling after a refused SetTenantBudget = %+v, want %v", b.Ceiling, tenantBudget)
	}
}

// Erasing a tenant's usage drops its counters, holds, copies' counters,
// charges and aggregate, and no other tenant's.
func checkEraseTenantUsage[TX any](t *testing.T, f fixture[TX]) {
	eraser, ok := f.Usage.(limes.TenantUsageEraser)
	if !ok {
		t.Skip("the UsageStore does not implement limes.TenantUsageEraser")
	}
	other, err := f.newTenant()
	if err != nil {
		t.Fatalf("NewTenant: %v", err)
	}
	theirCap, err := f.NewCapability(CapabilitySpec{Tenant: other, Requests: limes.RequestCeiling{Unlimited: true}, Budget: unlimitedBudget})
	if err != nil {
		t.Fatalf("capability of another tenant: %v", err)
	}
	theirCtx := f.tenantCtx(other)
	if _, err := f.Usage.SetTenantBudget(theirCtx, limes.SetTenantBudgetRequest{
		TenantID: other, Ceiling: limes.BudgetCeiling{Unlimited: true}, UnitCode: unit,
	}); err != nil {
		t.Fatalf("budget of another tenant: %v", err)
	}
	theirSubject := newSubject(subjectSpec{capID: theirCap, tenant: other})
	theirCharge, err := f.Usage.Charge(theirCtx, limes.ChargeRequest{Subject: theirSubject, Amount: one}, nil)
	if err != nil {
		t.Fatalf("charge of another tenant: %v", err)
	}
	copyID := []byte("erased-tenant-copy")
	mine := newSubject(subjectSpec{capID: f.child, tenant: f.Tenant, budget: budgetOf(childBudget),
		copies: []limes.CopyCeiling{{RevocationID: copyID, Budget: &limes.BudgetCeiling{Max: childBudget}}}})
	charged, err := f.Usage.Charge(f.Ctx, limes.ChargeRequest{Subject: mine, Amount: one, ExternalRef: "erased"}, nil)
	if err != nil {
		t.Fatalf("charge: %v", err)
	}
	held, err := f.Usage.Reserve(f.Ctx, limes.ReserveRequest{Subject: f.childSubject(), Amount: one})
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	req := limes.EraseTenantUsageRequest{TenantID: f.Tenant}
	if f.ScopesTenants {
		if err := eraser.EraseTenantUsage(theirCtx, req); !errors.Is(err, limes.ErrScopeMismatch) {
			t.Errorf("EraseTenantUsage under another tenant's context: err = %v, want ErrScopeMismatch", err)
		}
	}
	if err := eraser.EraseTenantUsage(f.Ctx, limes.EraseTenantUsageRequest{}); !errors.Is(err, limes.ErrInvalidRequest) {
		t.Errorf("EraseTenantUsage naming no tenant: err = %v, want ErrInvalidRequest", err)
	}
	if err := eraser.EraseTenantUsage(f.Ctx, req); err != nil {
		t.Fatalf("EraseTenantUsage: %v", err)
	}
	for _, id := range []uuid.UUID{f.root, f.child} {
		if u, err := f.Usage.GetUsage(f.Ctx, id); !errors.Is(err, limes.ErrUsageNotFound) {
			t.Errorf("usage of %s after the erase = %+v, %v; want ErrUsageNotFound", id, u, err)
		}
	}
	if _, err := f.Usage.GetCharge(f.Ctx, charged.ChargeID); !errors.Is(err, limes.ErrChargeNotFound) {
		t.Errorf("GetCharge after the erase: err = %v, want ErrChargeNotFound", err)
	}
	if _, err := f.Usage.ChargeByRef(f.Ctx, limes.ChargeByRefRequest{CapabilityID: f.child, ExternalRef: "erased"}); !errors.Is(err, limes.ErrChargeNotFound) {
		t.Errorf("ChargeByRef after the erase: err = %v, want ErrChargeNotFound", err)
	}
	if _, err := f.Usage.GetReservation(f.Ctx, held.ID); !errors.Is(err, limes.ErrReservationNotFound) {
		t.Errorf("GetReservation after the erase: err = %v, want ErrReservationNotFound", err)
	}
	if _, err := f.Usage.GetTenantBudget(f.Ctx, f.Tenant); !errors.Is(err, limes.ErrTenantBudgetNotFound) {
		t.Errorf("GetTenantBudget after the erase: err = %v, want ErrTenantBudgetNotFound", err)
	}
	if copies, ok := f.Usage.(limes.CopyUsageReader); ok {
		got, err := copies.CopyUsage(f.Ctx, limes.CopyUsageRequest{RevocationIDs: [][]byte{copyID}})
		if err != nil || len(got.Copies) != 0 {
			t.Errorf("CopyUsage after the erase = %+v, %v; want no copy", got, err)
		}
	}
	if u, err := f.Usage.GetUsage(theirCtx, theirCap); err != nil || u.SpentAmount != one {
		t.Errorf("another tenant's usage after the erase = %+v, %v; want %v spent", u, err, one)
	}
	if _, err := f.Usage.GetCharge(theirCtx, theirCharge.ChargeID); err != nil {
		t.Errorf("another tenant's charge after the erase: %v", err)
	}
	if b, err := f.Usage.GetTenantBudget(theirCtx, other); err != nil || b.SpentAmount != one {
		t.Errorf("another tenant's budget after the erase = %+v, %v; want %v spent", b, err, one)
	}
}

// sumWindow is how far either side of a check's clock SumCharges looks when
// the Env reads no clock: past any store's offset from the wall clock.
const sumWindow = 24 * time.Hour

// sumPeriod is a period holding every charge a check takes.
func (f fixture[TX]) sumPeriod() (from, to time.Time) {
	now := time.Now()
	if f.Now != nil {
		now = f.Now()
	}
	return now.Add(-sumWindow), now.Add(sumWindow)
}

// SumCharges sums Tenant's charges alone, grouped as asked, the largest
// first, a refund in its charge's group, and pages them.
func checkSumCharges[TX any](t *testing.T, f fixture[TX]) {
	if f.Ledger == nil {
		t.Skip("Env.Ledger not set")
	}
	other, err := f.newTenant()
	if err != nil {
		t.Fatalf("NewTenant: %v", err)
	}
	theirCap, err := f.NewCapability(CapabilitySpec{Tenant: other, Requests: limes.RequestCeiling{Unlimited: true}, Budget: unlimitedBudget})
	if err != nil {
		t.Fatalf("capability of another tenant: %v", err)
	}
	if _, err := f.Usage.Charge(f.tenantCtx(other), limes.ChargeRequest{Subject: newSubject(subjectSpec{capID: theirCap, tenant: other}), Amount: 7 * one}, nil); err != nil {
		t.Fatalf("charge of another tenant: %v", err)
	}
	root := newSubject(subjectSpec{capID: f.root, tenant: f.Tenant, budget: budgetOf(rootBudget)})
	for _, c := range []limes.ChargeRequest{
		{Amount: 2 * one, Op: limes.OpGet, Actor: "a"},
		{Amount: 3 * one, Op: limes.OpPut, Actor: "b"},
		{Subject: root, Amount: 6 * one, Op: limes.OpGet, Actor: "a"},
	} {
		r, err := f.charge(c, nil)
		if err != nil {
			t.Fatalf("charge: %v", err)
		}
		if c.Op == limes.OpPut {
			if _, err := f.Usage.Refund(f.Ctx, limes.RefundRequest{ChargeID: r.ChargeID, Amount: one}); err != nil {
				t.Fatalf("refund: %v", err)
			}
		}
	}
	from, to := f.sumPeriod()
	sums := func(req limes.SumChargesRequest) []limes.ChargeSum {
		t.Helper()
		req.TenantID, req.From, req.To = f.Tenant, from, to
		page, err := f.Ledger.SumCharges(f.Ctx, req)
		if err != nil {
			t.Fatalf("SumCharges(%+v): %v", req, err)
		}
		return page.Items
	}
	for name, tc := range map[string]struct {
		req  limes.SumChargesRequest
		want []limes.ChargeSum
	}{
		"the whole period": {limes.SumChargesRequest{}, []limes.ChargeSum{
			{UnitCode: unit, Charged: 11 * one, Refunded: one, Charges: 3},
		}},
		"by operation": {limes.SumChargesRequest{ByOp: true}, []limes.ChargeSum{
			{UnitCode: unit, Op: limes.OpGet, Charged: 8 * one, Charges: 2},
			{UnitCode: unit, Op: limes.OpPut, Charged: 3 * one, Refunded: one, Charges: 1},
		}},
		"by actor": {limes.SumChargesRequest{ByActor: true}, []limes.ChargeSum{
			{UnitCode: unit, Actor: "a", Charged: 8 * one, Charges: 2},
			{UnitCode: unit, Actor: "b", Charged: 3 * one, Refunded: one, Charges: 1},
		}},
		"by capability": {limes.SumChargesRequest{ByCapability: true}, []limes.ChargeSum{
			{UnitCode: unit, CapabilityID: f.root, Charged: 6 * one, Charges: 1},
			{UnitCode: unit, CapabilityID: f.child, Charged: 5 * one, Refunded: one, Charges: 2},
		}},
	} {
		if got := sums(tc.req); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: SumCharges = %+v, want %+v", name, got, tc.want)
		}
	}
	// The top one, then the rest after its cursor.
	req := limes.SumChargesRequest{TenantID: f.Tenant, From: from, To: to, ByCapability: true, Limit: 1}
	first, err := f.Ledger.SumCharges(f.Ctx, req)
	if err != nil || len(first.Items) != 1 || first.Items[0].CapabilityID != f.root || first.Next == "" {
		t.Fatalf("the first page of one = %+v, %v; want the root and a cursor", first, err)
	}
	req.Cursor = first.Next
	second, err := f.Ledger.SumCharges(f.Ctx, req)
	if err != nil || len(second.Items) != 1 || second.Items[0].CapabilityID != f.child || second.Next != "" {
		t.Errorf("the page after it = %+v, %v; want the child, and the last page", second, err)
	}
	// A period that ends before the charges holds none of them.
	if page, err := f.Ledger.SumCharges(f.Ctx, limes.SumChargesRequest{TenantID: f.Tenant, From: from, To: from.Add(time.Second)}); err != nil || len(page.Items) != 0 {
		t.Errorf("SumCharges of a period before the charges = %+v, %v; want none", page, err)
	}
	for name, bad := range map[string]limes.SumChargesRequest{
		"no tenant":         {From: from, To: to},
		"an empty period":   {TenantID: f.Tenant, From: to, To: from},
		"no period":         {TenantID: f.Tenant},
		"an unknown bucket": {TenantID: f.Tenant, From: from, To: to, Bucket: limes.ChargeBucket(math.MaxUint8)},
		"a negative limit":  {TenantID: f.Tenant, From: from, To: to, Limit: -1},
		"a foreign cursor":  {TenantID: f.Tenant, From: from, To: to, Cursor: "not a cursor"},
	} {
		if _, err := f.Ledger.SumCharges(f.Ctx, bad); !errors.Is(err, limes.ErrInvalidRequest) {
			t.Errorf("SumCharges with %s: err = %v, want ErrInvalidRequest", name, err)
		}
	}
	if f.ScopesTenants {
		if _, err := f.Ledger.SumCharges(f.tenantCtx(other), limes.SumChargesRequest{TenantID: f.Tenant, From: from, To: to}); !errors.Is(err, limes.ErrScopeMismatch) {
			t.Errorf("SumCharges under another tenant's context: err = %v, want ErrScopeMismatch", err)
		}
	}
}

// SumCharges orders sums by amount, not by their digits: sums of different
// lengths come largest first, whole and within each bucket, and a walk one
// row at a time yields what one page does. A refund stays in its charge's
// group throughout.
func checkSumChargesOrderByAmount[TX any](t *testing.T, f fixture[TX]) {
	if f.Ledger == nil {
		t.Skip("Env.Ledger not set")
	}
	// The charges outgrow the fixture's ceilings: an unlimited capability
	// under an unlimited tenant.
	b, err := f.Usage.GetTenantBudget(f.Ctx, f.Tenant)
	if err != nil {
		t.Fatalf("tenant budget: %v", err)
	}
	if _, err := f.Usage.SetTenantBudget(f.Ctx, limes.SetTenantBudgetRequest{
		TenantID: f.Tenant, Ceiling: unlimitedBudget, UnitCode: unit, ExpectedVersion: b.ResourceVersion,
	}); err != nil {
		t.Fatalf("tenant budget: %v", err)
	}
	capID, err := f.NewCapability(CapabilitySpec{Requests: limes.RequestCeiling{Unlimited: true}, Budget: unlimitedBudget})
	if err != nil {
		t.Fatalf("capability: %v", err)
	}
	subject := newSubject(subjectSpec{capID: capID, tenant: f.Tenant})
	charge := func(actor string, amount, refund limes.Nanos) {
		t.Helper()
		r, err := f.charge(limes.ChargeRequest{Subject: subject, Amount: amount, Actor: actor}, nil)
		if err != nil {
			t.Fatalf("charge of %s by %s: %v", amount, actor, err)
		}
		if refund == 0 {
			return
		}
		if _, err := f.Usage.Refund(f.Ctx, limes.RefundRequest{ChargeID: r.ChargeID, Amount: refund}); err != nil {
			t.Fatalf("refund: %v", err)
		}
	}
	from, to := f.sumPeriod()
	walk := func(req limes.SumChargesRequest, want []limes.ChargeSum) {
		t.Helper()
		req.TenantID, req.From, req.To, req.ByActor = f.Tenant, from, to, true
		page, err := f.Ledger.SumCharges(f.Ctx, req)
		if err != nil || page.Next != "" || !sameSums(page.Items, want) {
			t.Errorf("SumCharges by %s = %+v, %q, %v; want %+v on one page", req.Bucket, page.Items, page.Next, err, want)
		}
		req.Limit = 1
		var walked []limes.ChargeSum
		for range len(want) + 1 {
			page, err := f.Ledger.SumCharges(f.Ctx, req)
			if err != nil {
				t.Fatalf("SumCharges by %s, a page of one: %v", req.Bucket, err)
			}
			walked = append(walked, page.Items...)
			if req.Cursor = page.Next; req.Cursor == "" {
				break
			}
		}
		if !sameSums(walked, want) {
			t.Errorf("SumCharges by %s a row at a time = %+v, want %+v", req.Bucket, walked, want)
		}
	}
	// Sums of nine, ten and eleven digits in nanos, whose text order is the
	// reverse of their order by amount.
	charge("x", 12*one, one)
	charge("y", 2*one, 0)
	charge("z", one/2, 0)
	walk(limes.SumChargesRequest{}, []limes.ChargeSum{
		{UnitCode: unit, Actor: "x", Charged: 12 * one, Refunded: one, Charges: 1},
		{UnitCode: unit, Actor: "y", Charged: 2 * one, Charges: 1},
		{UnitCode: unit, Actor: "z", Charged: one / 2, Charges: 1},
	})
	if f.Now == nil || f.Advance == nil {
		return
	}
	// The next day, an order of its own, again the reverse of the text's.
	first := f.Now().UTC()
	firstDay := time.Date(first.Year(), first.Month(), first.Day(), 0, 0, 0, 0, time.UTC)
	nextDay := firstDay.AddDate(0, 0, 1)
	f.Advance(nextDay.Sub(first))
	charge("z", 100*one, 0)
	charge("x", 7*one, 0)
	charge("y", 9*cent, 0)
	_, to = f.sumPeriod()
	walk(limes.SumChargesRequest{}, []limes.ChargeSum{
		{UnitCode: unit, Actor: "z", Charged: 100*one + one/2, Charges: 2},
		{UnitCode: unit, Actor: "x", Charged: 19 * one, Refunded: one, Charges: 2},
		{UnitCode: unit, Actor: "y", Charged: 2*one + 9*cent, Charges: 2},
	})
	walk(limes.SumChargesRequest{Bucket: limes.ChargeBucketDay}, []limes.ChargeSum{
		{BucketStart: firstDay, UnitCode: unit, Actor: "x", Charged: 12 * one, Refunded: one, Charges: 1},
		{BucketStart: firstDay, UnitCode: unit, Actor: "y", Charged: 2 * one, Charges: 1},
		{BucketStart: firstDay, UnitCode: unit, Actor: "z", Charged: one / 2, Charges: 1},
		{BucketStart: nextDay, UnitCode: unit, Actor: "z", Charged: 100 * one, Charges: 1},
		{BucketStart: nextDay, UnitCode: unit, Actor: "x", Charged: 7 * one, Charges: 1},
		{BucketStart: nextDay, UnitCode: unit, Actor: "y", Charged: 9 * cent, Charges: 1},
	})
}

// Buckets are cut at UTC boundaries: a charge a microsecond — the finest a
// timestamp column keeps — before midnight falls in that day and hour, and
// one at midnight in the next.
func checkSumChargeBuckets[TX any](t *testing.T, f fixture[TX]) {
	if f.Ledger == nil || f.Now == nil || f.Advance == nil {
		t.Skip("Env.Ledger, Now or Advance not set")
	}
	const tick = time.Microsecond
	from := f.Now()
	y, m, d := from.UTC().Date()
	midnight := time.Date(y, m, d+1, 0, 0, 0, 0, time.UTC)
	f.Advance(midnight.Sub(from) - tick)
	if _, err := f.charge(limes.ChargeRequest{Amount: 2 * one}, nil); err != nil {
		t.Fatalf("charge before midnight: %v", err)
	}
	f.Advance(tick)
	if _, err := f.charge(limes.ChargeRequest{Amount: 3 * one}, nil); err != nil {
		t.Fatalf("charge at midnight: %v", err)
	}
	to := midnight.Add(time.Second)
	for b, before := range map[limes.ChargeBucket]time.Time{
		limes.ChargeBucketHour: midnight.Add(-time.Hour),
		limes.ChargeBucketDay:  midnight.AddDate(0, 0, -1),
	} {
		page, err := f.Ledger.SumCharges(f.Ctx, limes.SumChargesRequest{TenantID: f.Tenant, From: from, To: to, Bucket: b})
		want := []limes.ChargeSum{
			{BucketStart: before, UnitCode: unit, Charged: 2 * one, Charges: 1},
			{BucketStart: midnight, UnitCode: unit, Charged: 3 * one, Charges: 1},
		}
		if err != nil || !sameSums(page.Items, want) {
			t.Errorf("SumCharges by %s = %+v, %v; want %+v", b, page.Items, err, want)
		}
	}
	page, err := f.Ledger.SumCharges(f.Ctx, limes.SumChargesRequest{TenantID: f.Tenant, From: from, To: to, Bucket: limes.ChargeBucketMonth})
	months := map[time.Time]bool{}
	for _, s := range page.Items {
		months[s.BucketStart] = true
	}
	want := map[time.Time]bool{
		time.Date(y, m, 1, 0, 0, 0, 0, time.UTC):                              true,
		time.Date(midnight.Year(), midnight.Month(), 1, 0, 0, 0, 0, time.UTC): true,
	}
	if err != nil || !reflect.DeepEqual(months, want) {
		t.Errorf("SumCharges by month = %+v, %v; want the months %v", page.Items, err, want)
	}
}

// sameSums compares sums, their bucket starts as instants.
func sameSums(got, want []limes.ChargeSum) bool {
	return slices.EqualFunc(got, want, func(a, b limes.ChargeSum) bool {
		a.BucketStart, b.BucketStart = a.BucketStart.UTC(), b.BucketStart.UTC()
		return reflect.DeepEqual(a, b)
	})
}
