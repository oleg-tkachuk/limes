// Package metertest checks a meter.UsageStore against the module's contract
// — its Meter, tenant budgets and housekeeping — the way testing/fstest
// checks an fs.FS. A UsageStore is the consumer's to write,
// and most of its contract lives in request fields — a store that ignores
// ExternalRef or Overrun still compiles, and then charges a repeated cost
// twice. Run it from the store's own tests:
//
//	func TestMeterContract(t *testing.T) {
//		metertest.Run(t, func(t *testing.T) metertest.Env { … })
//	}
//
// The checks cover what a charge does at its ceilings and when it arrives
// again: rejection that moves nothing — by a Biscuit copy's, the capability's,
// an ancestor's or the tenant's ceiling, by a failed onCharged, under
// concurrent charges — idempotency on ExternalRef and on a settled
// reservation, also once the capability is gone (Env.DeleteCapability), ChargeByRef,
// OverrunPolicyRecord past every ceiling, refunds, holds, their listing and their expiry, request
// counts, copy counters and CopyUsage, units, counters that never pass
// MaxNanos, a capability not on record and PurgeOrphans (Env.DeleteCapability),
// DeleteUsage, the
// tenant aggregate, its version guard and its listing, and the validation of
// the request; and, where the Env enables them, EraseTenantUsage,
// limes.ErrScopeMismatch under another tenant's scope and for every write
// under a read-only one, and a LedgerReader's
// sums, their order, paging, UTC buckets, period edges, late refunds and
// overflow, and, when the Ledger is a meter.LedgerHousekeeping, PurgeCharges
// to its retention. They need a real ledger, not a fake.
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
	"github.com/oleg-tkachuk/limes/internal/expect"
	"math"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/internal/fields"
	"github.com/oleg-tkachuk/limes/internal/link"
	"github.com/oleg-tkachuk/limes/internal/watchdog"
	"github.com/oleg-tkachuk/limes/meter"
)

// DefaultCheckTimeout bounds a check whose Env sets no CheckTimeout.
const DefaultCheckTimeout = watchdog.DefaultLimit

// Env is one store under test, fresh for each check.
type Env struct {
	// Ctx is the context every call for Tenant runs under — scoped to it, if
	// the store needs that. A write for a tenant from NewTenant runs under
	// TenantCtx, and ListTenantBudgets under ListCtx, when the Env sets them.
	Ctx context.Context
	// Usage is the store under test, with no usage recorded.
	Usage meter.UsageStore
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
	// the checks that need time to pass are skipped or cut short — a hold's
	// expiry and ReleaseExpired, its batches, a hold dating the tenant
	// budget, the accounting period's start, and the ledger's day and week
	// buckets.
	Advance func(d time.Duration)
	// DeleteCapability removes a capability's record, as Store.Purge does
	// once it has expired. Optional: without it, the checks of what follows
	// a record's removal are skipped or cut short — PurgeOrphans, a replay
	// once the capability is gone, a settle under a removed ancestor, a
	// chain's depth once its top is removed, a capability not on record,
	// and a copy's counters going with their capability.
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
	// checks of meter.LedgerReader are skipped.
	Ledger meter.LedgerReader
	// Now reads the store's clock, which stands still while a check runs
	// but for Advance. Optional: without it, the checks of the ledger's
	// time buckets, of a hold ending with its capability and of a hold
	// dating the tenant budget are skipped.
	Now func() time.Time
	// ScopesTenants reports that the store keeps each tenant's rows apart
	// by the call's context, TenantCtx giving each tenant's: a call whose
	// request names one tenant, made under another tenant's context, is
	// refused with limes.ErrScopeMismatch and changes nothing. Optional:
	// without it, the checks of that are skipped.
	ScopesTenants bool
	// MaxCopiesPerCapability is the quota of Biscuit copies per capability
	// the store under test was built with (meter.Meter, "Copies of a
	// Biscuit"). Optional: without it, the check of the quota is skipped. Set
	// it above any check's own copies — 16 is room enough.
	MaxCopiesPerCapability int
	// ReadOnlyCtx is a context scoped to reading every tenant's rows and
	// writing none, as the store's consumer scopes it — by the store's own
	// context or by a scope of the consumer's: Tenant's usage reads under
	// it, and every call that writes is refused with
	// limes.ErrScopeMismatch, runs no onCharged and changes nothing.
	// Optional: without it, the checks of that are skipped.
	ReadOnlyCtx context.Context
}

// CapabilitySpec is a capability Env.NewCapability records: its parent and
// the ceilings of its caveats.
type CapabilitySpec struct {
	// Parent is the capability it was delegated from; uuid.Nil() for a root.
	Parent uuid.UUID
	// Tenant is the tenant it is issued in, one NewTenant created; uuid.Nil()
	// for Env.Tenant.
	Tenant uuid.UUID
	// Requests and Budget are its caveats' ceilings; Budget names the unit
	// the capability counts in.
	Requests limes.RequestCeiling
	Budget   limes.BudgetCeiling
	// Copies is its quota of Biscuit copies; nil is Unlimited. CopyQuota
	// reads it as the record should hold it.
	Copies *limes.CopyQuota
}

// CopyQuota is the quota of copies the record holds: Copies, or Unlimited
// when the spec names none.
func (s CapabilitySpec) CopyQuota() limes.CopyQuota {
	if s.Copies == nil {
		return limes.CopyQuota{Unlimited: true}
	}
	return *s.Copies
}

// newCapability records a capability under parent with the given budget and
// no request ceiling.
func (e Env) newCapability(parent uuid.UUID, budget limes.BudgetCeiling) (uuid.UUID, error) {
	return e.NewCapability(CapabilitySpec{Parent: parent, Requests: limes.RequestCeiling{Unlimited: true}, Budget: inUnit(budget)})
}

// inUnit is c in the suite's unit, unless it names one of its own.
func inUnit(c limes.BudgetCeiling) limes.BudgetCeiling {
	if c.UnitCode == "" {
		c.UnitCode = unit
	}
	return c
}

// newTenant is another tenant, from NewTenant when the Env has one.
func (e Env) newTenant() (uuid.UUID, error) {
	if e.NewTenant == nil {
		return uuid.New(), nil
	}
	return e.NewTenant()
}

// tenantCtx is the context a write for tenant runs under.
func (e Env) tenantCtx(tenant uuid.UUID) context.Context {
	if e.TenantCtx == nil {
		return e.Ctx
	}
	return e.TenantCtx(tenant)
}

// listCtx is the context a read across tenants runs under.
func (e Env) listCtx() context.Context {
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
	unit      = "USD"
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
type fixture struct {
	Env
	root, child uuid.UUID
}

func newFixture(t *testing.T, setup func(t *testing.T) Env) fixture {
	t.Helper()
	env := setup(t)
	f := fixture{Env: env}
	var err error
	f.root, err = env.newCapability(uuid.Nil(), limes.BudgetCeiling{Max: rootBudget})
	expect.Must(t, err == nil, "root capability: %v", err)
	f.child, err = env.newCapability(f.root, limes.BudgetCeiling{Max: childBudget})
	expect.Must(t, err == nil, "child capability: %v", err)
	_, err = env.Usage.SetTenantBudget(env.Ctx, meter.SetTenantBudgetRequest{
		TenantID: env.Tenant, Ceiling: limes.BudgetCeiling{Max: tenantBudget, UnitCode: unit},
	})
	expect.Must(t, err == nil, "tenant budget: %v", err)
	return f
}

// unlimitedBudget is a capability's budget when the check needs none.
var unlimitedBudget = limes.BudgetCeiling{Unlimited: true, UnitCode: unit}

// subjectSpec is a meter subject's parts; zero fields take the fixture's.
type subjectSpec struct {
	capID  uuid.UUID
	tenant uuid.UUID
	// requests and budget are the subject's ceilings; nil, a check that
	// sets none, is Unlimited.
	requests *limes.RequestCeiling
	budget   *limes.BudgetCeiling
	// unit is the unit of the subject's budget, the suite's own when empty;
	// unitless gives a subject whose budget is the zero one, in no unit.
	unit     string
	unitless bool
	copies   []limes.CopyCeiling
	// copyQuota is the subject's quota of copies; nil is Unlimited.
	copyQuota *limes.CopyQuota
}

// newSubject is the MeterSubject a verified capability with spec's parts
// would give: the suite checks a store against ceilings no issuer signed.
func newSubject(spec subjectSpec) limes.MeterSubject {
	return link.NewMeterSubject(link.MeterSubjectFields{
		CapabilityID: spec.capID, TenantID: spec.tenant,
		Requests: spec.requestCeiling(), Budget: spec.budgetCeiling(), Copies: spec.copies,
		CopyQuota: spec.quota(),
	}).(limes.MeterSubject)
}

func (spec subjectSpec) quota() limes.CopyQuota {
	if spec.copyQuota == nil {
		return limes.CopyQuota{Unlimited: true}
	}
	return *spec.copyQuota
}

func (spec subjectSpec) requestCeiling() limes.RequestCeiling {
	if spec.requests == nil {
		return limes.RequestCeiling{Unlimited: true}
	}
	return *spec.requests
}

func (spec subjectSpec) budgetCeiling() limes.BudgetCeiling {
	if spec.unitless {
		return limes.BudgetCeiling{}
	}
	c := limes.BudgetCeiling{Unlimited: true}
	if spec.budget != nil {
		c = *spec.budget
	}
	if spec.unit != "" {
		c.UnitCode = spec.unit
	}
	return inUnit(c)
}

// budgetOf and requestsOf are ceilings of n, for a spec to set.
func budgetOf(n limes.Nanos) *limes.BudgetCeiling { return &limes.BudgetCeiling{Max: n} }
func requestsOf(n int64) *limes.RequestCeiling    { return &limes.RequestCeiling{Max: n} }

// childSubject is the fixture's child as a verified capability would give it.
func (f fixture) childSubject() limes.MeterSubject {
	return newSubject(subjectSpec{capID: f.child, tenant: f.Tenant, budget: budgetOf(childBudget)})
}

// charge charges req against the fixture's child, its ceiling, tenant and
// unit, unless the request's subject names another capability or ceiling.
func (f fixture) charge(req meter.ChargeRequest, onCharged func(context.Context, meter.Charged) error) (meter.ChargeReceipt, error) {
	s := req.Subject
	requests, budget := s.Requests(), s.Budget()
	spec := subjectSpec{capID: s.CapabilityID(), tenant: f.Tenant, requests: &requests, budget: &budget, copies: s.Copies()}
	// A subject naming no capability is the child's, under the child's
	// budget and no request ceiling, with whatever copies it names.
	if spec.capID == uuid.Nil() {
		spec.capID = f.child
		spec.budget = budgetOf(childBudget)
		spec.requests = nil
	}
	req.Subject = newSubject(spec)
	return f.Usage.Charge(f.Ctx, req, onCharged)
}

func (f fixture) spent(t *testing.T, id uuid.UUID) limes.Nanos {
	t.Helper()
	u, err := f.Usage.GetUsage(f.Ctx, id)
	if errors.Is(err, meter.ErrUsageNotFound) {
		return 0
	}
	expect.Must(t, err == nil, "usage of %s: %v", id, err)
	return u.SpentAmount
}

func (f fixture) tenantSpent(t *testing.T) limes.Nanos {
	t.Helper()
	b, err := f.Usage.GetTenantBudget(f.Ctx, f.Tenant)
	expect.Must(t, err == nil, "tenant budget: %v", err)
	return b.SpentAmount
}

// Run runs every check as a subtest, each on a fresh Env from setup.
func Run(t *testing.T, setup func(t *testing.T) Env) {
	t.Helper()
	checks := []struct {
		name string
		run  func(*testing.T, fixture)
	}{
		{"RejectionMovesNothing", checkRejectionMovesNothing},
		{"ExternalRefChargesOnce", checkExternalRefChargesOnce},
		{"ExternalRefChargesOnceConcurrently", checkExternalRefConcurrent},
		{"ChargeByRef", checkChargeByRef},
		{"MalformedRequestsAreRefused", checkMalformed},
		{"OverrunRecordCrossesEveryCeiling", checkOverrunRecord},
		{"SettleReplaysAndRecordsAnOverrun", checkSettle},
		{"SettleOfNoReservationIsNotFound", checkSettleNil},
		{"SettleOfAnotherCapabilitysHoldIsNotFound", checkSettleOwner},
		{"SettleWithinTheHoldPassesACrossedCeiling", checkSettleWithinHold},
		{"RefundOfAClosedPeriodLeavesTheTenantPeriod", checkRefundAcrossReset},
		{"EveryCounterKeepsOneUnit", checkOneUnit},
		{"AnAncestorCountsInTheUnitOfItsChildren", checkAncestorUnit},
		{"ASubjectWithNoUnitCountsRequestsAlone", checkNoUnit},
		{"ATenantCeilingCountsInTheUnitItNames", checkTenantCeilingUnit},
		{"DeleteReleasesHolds", checkDeleteReleasesHolds},
		{"AReplayIsAnsweredOnceTheCapabilityIsGone", checkReplayAfterRemoval},
		{"AnAncestorAndATenantCeilingAreExact", checkExactCeilings},
		{"AHoldAloneCountsOnItsTenant", checkHoldCreatesTenant},
		{"ASettleReplayCommitsNothingAgain", checkSettleReplay},
		{"ReleasingAHoldReturnsItToTheCopy", checkReleaseReturnsCopy},
		{"ARefusedBumpCountsNothing", checkRefusedBump},
		{"HoldsCountTowardsTheCounterRange", checkRangeCountsHolds},
		{"ARefundOfExactlyWhatIsLeftRefundsIt", checkRefundExactRemainder},
		{"PurgeChargesKeepsTheRetentionAndTakesThePromises", checkPurgeCharges},
		{"ASettleWritesNoRowForARemovedAncestor", checkSettleAfterRemovedAncestor},
		{"ASettleCountsOnAnAncestorWhoseRowWasDropped", checkSettleAfterAncestorDropped},
		{"ConcurrentChargesKeepTheCeiling", checkConcurrentCeiling},
		{"AnAncestorCeilingBoundsItsChildren", checkAncestorCeiling},
		{"TheTenantCeilingBoundsEveryCapability", checkTenantCeiling},
		{"AFailedCallbackMovesNothing", checkCallbackRollback},
		{"TheCallbackSeesTheReceipt", checkCallbackSeesTheReceipt},
		{"BumpCountsTheChain", checkBump},
		{"RefundReturnsToEveryCounter", checkRefund},
		{"AHoldCountsAgainstCharges", checkHoldBlocksCharge},
		{"AnExpiredHoldIsReleased", checkExpiry},
		{"ListTenantBudgetsPagesMostAtRiskFirst", checkListTenantBudgets},
		{"OverrunRecordCrossesACopyBudget", checkCopyOverrun},
		{"ACopyCountsForItsCapabilityAndGoesWithIt", checkCopyCountersGo},
		{"ACopyIDSharedByTwoCapabilitiesCountsApart", checkCopySharedID},
		{"CopiesPastTheQuotaAreRefused", checkCopyQuota},
		{"TheCapabilitysCopyQuotaBinds", checkCopyQuotaCaveat},
		{"ACopyQuotaBindsTheWholeLineage", checkCopyQuotaLineage},
		{"CopyUsageRefusesAnInvalidRequest", checkCopyUsageRefusesInvalid},
		{"TenantBudgetReadsAgree", checkTenantBudgetReads},
		{"ReservationsReadBack", checkReservations},
		{"AHoldNeverOutlivesItsCapability", checkHoldWithinCapability},
		{"AHoldDatesTheTenantBudget", checkHoldDatesTenantBudget},
		{"SumsExactly", checkSumsExactly},
		{"ReservationsPage", checkReservationsPage},
		{"AHoldWithNoTTLLastsTheDefault", checkDefaultReservationTTL},
		{"ReservationsTiedOnExpiryPageInIDOrder", checkReservationsTied},
		{"HousekeepingRefusesANegativeLimit", checkHousekeepingLimits},
		{"PurgeOrphansReleasesWhatARemovedCapabilityHeld", checkPurgeOrphans},
		{"AChainAtMaxDepthTakesAChildOnceItsTopIsRemoved", checkLineageAfterRemoval},
		{"TenantBudgetKeepsItsPeriodEnd", checkPeriodEnd},
		{"ACallScopedToAnotherTenantIsRefused", checkScopeMismatch},
		{"AReadOnlyScopeWritesNothing", checkReadOnlyScope},
		{"EraseTenantUsageDropsEveryRowOfTheTenant", checkEraseTenantUsage},
		{"SumChargesGroupsAndPagesTheLedger", checkSumCharges},
		{"SumChargesAcrossTenants", checkSumChargesAcrossTenants},
		{"SumChargesCutsBucketsAtUTCBoundaries", checkSumChargeBuckets},
		{"SumChargesOrdersByAmountNotDigits", checkSumChargesOrderByAmount},
		{"SumChargesCutsWeeksAtMonday", checkSumChargeWeeks},
		{"SumChargesBoundsThePeriodFromIncludedToNot", checkSumChargesPeriodEdges},
		{"SumChargesCountsARefundInItsChargesClosedBucket", checkSumChargesLateRefund},
		{"ASumPastMaxNanosIsAnOverflow", checkSumChargesOverflow},
		{"ATenantWithNoRowHasNoBudget", checkTenantBudgetNotFound},
		{"ResetSpendAloneRollsThePeriodStart", checkPeriodStart},
		{"TheZeroSubjectIsRefused", checkZeroSubject},
		{"AnAncestorRequestCeilingBoundsBump", checkAncestorRequests},
		{"ACopyRequestCeilingBoundsBump", checkCopyRequests},
		{"AHoldCountsAgainstACopyBudget", checkCopyHold},
		{"AStaleVersionIsRefused", checkStaleVersion},
		{"ACapabilityNotOnRecordIsNotMetered", checkNotOnRecord},
		{"ACounterNeverPassesMaxNanos", checkCounterOverflow},
		{"ReleaseReturnsTheHoldToEveryCounter", checkRelease},
		{"ReleaseIsTheReservingCopys", checkReleaseIsTheOwners},
		{"AResetKeepsTheHolds", checkResetKeepsHolds},
		{"AnAncestorCountsItsDescendantsHolds", checkDescendantHolds},
		{"SettleAboveTheHoldCountsTheHoldOnce", checkSettleAboveHold},
		{"ANestedCopySettlesOnlyItsOwnHold", checkNestedCopySettle},
		{"TheUnitStaysWhileHoldsAreOpen", checkUnitWithHolds},
		{"EveryCounterOnTheChainKeepsItsUnit", checkChainUnits},
		{"ListTenantBudgetsRanksUnclamped", checkUnclampedRanking},
		{"AThresholdIsMetExactly", checkThresholdBoundary},
		{"ListTenantBudgetsRanksExactly", checkExactRanking},
		{"HousekeepingKeepsItsLimit", checkHousekeepingBatches},
		{"CopiesFollowSettleAndRefund", checkCopySettleRefund},
		{"AZeroCeilingAllowsNothing", checkZeroCeilings},
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
		run  func(*testing.T, Env)
	}{
		{"TenantAggregateCountsBeforeACeiling", checkTenantCountsBeforeACeiling},
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
func checkTenantCountsBeforeACeiling(t *testing.T, env Env) {
	const (
		spent   = 5 * one
		held    = 8 * one
		ceiling = 10 * one
	)
	id, err := env.newCapability(uuid.Nil(), unlimitedBudget)
	expect.Must(t, err == nil, "%v", err)
	_, err = env.Usage.Charge(env.Ctx, meter.ChargeRequest{
		Subject: newSubject(subjectSpec{capID: id, tenant: env.Tenant, unit: unit}),
		Amount:  spent,
	}, nil)
	expect.Must(t, err == nil, "charge: %v", err)
	reserve := func() error {
		_, err := env.Usage.Reserve(env.Ctx, meter.ReserveRequest{
			Subject: newSubject(subjectSpec{capID: id, tenant: env.Tenant, unit: unit}),
			Amount:  held,
		})
		return err
	}
	err = reserve()
	expect.Must(t, err == nil, "reserve: %v", err)
	b, err := env.Usage.GetTenantBudget(env.Ctx, env.Tenant)
	expect.Must(t, !(err != nil || b.SpentAmount != spent || b.ReservedAmount != held || !b.Ceiling.Unlimited || b.ResourceVersion != 0), "GetTenantBudget before any ceiling = %+v, %v; want %v spent, %v held, no ceiling, version 0", b, err, spent, held)
	set, err := env.Usage.SetTenantBudget(env.Ctx, meter.SetTenantBudgetRequest{
		TenantID: env.Tenant, Ceiling: limes.BudgetCeiling{Max: ceiling, UnitCode: unit},
	})
	expect.Must(t, !(err != nil || set.SpentAmount != spent || set.ReservedAmount != held), "SetTenantBudget = %+v, %v; want the ceiling over %v spent and %v held", set, err, spent, held)
	err = reserve()
	expect.That(t, errors.Is(err, limes.ErrTenantBudgetExceeded), "a hold past the new ceiling: err = %v, want ErrTenantBudgetExceeded", err)
	_, err = env.Usage.Charge(env.Ctx, meter.ChargeRequest{
		Subject: newSubject(subjectSpec{capID: id, tenant: env.Tenant, unit: unit}),
		Amount:  one,
	}, nil)
	expect.That(t, errors.Is(err, limes.ErrTenantBudgetExceeded), "a charge past the new ceiling: err = %v, want ErrTenantBudgetExceeded", err)
}

func checkRejectionMovesNothing(t *testing.T, f fixture) {
	ran := false
	r, err := f.charge(meter.ChargeRequest{Amount: childBudget + one}, func(context.Context, meter.Charged) error { ran = true; return nil })
	expect.Must(t, errors.Is(err, limes.ErrBudgetExceeded), "charge past the ceiling: err = %v, want ErrBudgetExceeded", err)
	expect.That(t, !(r.TenantID != f.Tenant || r.UnitCode != unit), "a refusal's receipt names tenant %s in %q, want %s in %q", r.TenantID, r.UnitCode, f.Tenant, unit)
	expect.Must(t, !(ran || f.spent(t, f.child) != 0 || f.spent(t, f.root) != 0 || f.tenantSpent(t) != 0), "a rejected charge moved something (callback ran: %v)", ran)
}

func checkExternalRefChargesOnce(t *testing.T, f fixture) {
	// copyRequests is the request ceiling of the copy below: any, as a
	// charge counts no requests.
	const copyRequests = 10
	fanOuts := 0
	onCharged := func(context.Context, meter.Charged) error { fanOuts++; return nil }
	first, err := f.charge(meter.ChargeRequest{Amount: 2 * one, ExternalRef: "call-1"}, onCharged)
	expect.Must(t, !(err != nil || first.Replayed), "first charge = %+v, %v", first, err)
	again, err := f.charge(meter.ChargeRequest{Amount: 2 * one, ExternalRef: "call-1"}, onCharged)
	expect.Must(t, !(err != nil || !again.Replayed || again.ChargeID != first.ChargeID || again.SpentAmount != 2*one), "repeated charge = %+v, %v; want a replay of %s at spend 2", again, err, first.ChargeID)
	// The same name for a different cost is the caller's bug; replaying the
	// first receipt would hide it.
	r, err := f.charge(meter.ChargeRequest{Amount: 5 * one, ExternalRef: "call-1"}, onCharged)
	expect.Must(t, errors.Is(err, meter.ErrExternalRefConflict), "the ref again for another amount = %+v, %v; want ErrExternalRefConflict", r, err)
	// So is the same name for the same amount in another unit or for
	// another tenant: it names another cost all the same.
	for name, subject := range map[string]limes.MeterSubject{
		"in another unit":    newSubject(subjectSpec{capID: f.child, tenant: f.Tenant, budget: budgetOf(childBudget), unit: otherUnit}),
		"for another tenant": newSubject(subjectSpec{capID: f.child, tenant: uuid.New(), budget: budgetOf(childBudget)}),
		// Another copy of the capability charges its own counters: the
		// first copy's receipt is not its to read back.
		"under a Biscuit copy": newSubject(subjectSpec{capID: f.child, tenant: f.Tenant, budget: budgetOf(childBudget),
			copies: []limes.CopyCeiling{{RevocationID: []byte("another-copy"), Requests: &limes.RequestCeiling{Max: copyRequests}}}}),
	} {
		want := meter.ErrExternalRefConflict
		// A store that scopes tenants refuses the call before it reads
		// the ledger: Ctx is not that tenant's.
		if f.ScopesTenants && subject.TenantID() != f.Tenant {
			want = limes.ErrScopeMismatch
		}
		r, err := f.Usage.Charge(f.Ctx, meter.ChargeRequest{Subject: subject, Amount: 2 * one, ExternalRef: "call-1"}, onCharged)
		expect.That(t, errors.Is(err, want), "the ref again %s = %+v, %v; want %v", name, r, err, want)
		// The conflict says which ref, not what the other charge was.
		expect.That(t, !(err != nil && strings.Contains(err.Error(), f.Tenant.String())), "the ref again %s: the error names the first charge's tenant: %v", name, err)
	}
	expect.That(t, fanOuts == 1, "onCharged ran %d times, want once", fanOuts)
	// The name is per capability, and no name never deduplicates.
	r, err = f.charge(meter.ChargeRequest{Subject: newSubject(subjectSpec{capID: f.root, budget: budgetOf(rootBudget)}), Amount: one, ExternalRef: "call-1"}, nil)
	expect.That(t, !(err != nil || r.Replayed), "same ref on another capability = %+v, %v; want a fresh charge", r, err)
	for range 2 {
		r, err := f.charge(meter.ChargeRequest{Amount: one}, nil)
		expect.That(t, !(err != nil || r.Replayed), "charge without a ref = %+v, %v; want a fresh charge", r, err)
	}
	got := f.spent(t, f.child)
	expect.That(t, got == 4*one, "child spent %v, want 2 + 1 + 1", got)
}

func checkExternalRefConcurrent(t *testing.T, f fixture) {
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		fresh   int
		charges = map[uuid.UUID]bool{}
	)
	for range concurrentDeliveries {
		wg.Go(func() {
			r, err := f.charge(meter.ChargeRequest{Amount: 2 * one, ExternalRef: "call-1"}, nil)
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
	expect.Must(t, !(fresh != 1 || len(charges) != 1), "%d fresh charges over %d charge ids, want one of each", fresh, len(charges))
	got := f.spent(t, f.root)
	expect.That(t, got == 2*one, "root spent %v, want 2", got)
}

// chargedOp is the operation a ledger row names: a consumer's own.
const chargedOp limes.Op = "index:search"

func checkChargeByRef(t *testing.T, f fixture) {
	_, err := f.Usage.ChargeByRef(f.Ctx, meter.ChargeByRefRequest{CapabilityID: f.child, ExternalRef: "call-1"})
	expect.Must(t, errors.Is(err, meter.ErrChargeNotFound), "before any charge: err = %v, want ErrChargeNotFound", err)
	const actor = "reporter"
	r, err := f.charge(meter.ChargeRequest{Amount: 3 * one, ExternalRef: "call-1", Op: chargedOp, Actor: actor}, nil)
	expect.Must(t, err == nil, "%v", err)
	_, err = f.Usage.Refund(f.Ctx, meter.RefundRequest{ChargeID: r.ChargeID, Amount: one})
	expect.Must(t, err == nil, "%v", err)
	got, err := f.Usage.ChargeByRef(f.Ctx, meter.ChargeByRefRequest{CapabilityID: f.child, ExternalRef: "call-1"})
	expect.Must(t, !(err != nil || got.ID != r.ChargeID || got.CapabilityID != f.child || got.Amount != 3*one ||
		got.RefundedAmount != one || got.ExternalRef != "call-1" || got.UnitCode != unit || got.Overrun ||
		got.TenantID != f.Tenant || got.Op != chargedOp || got.Actor != actor || got.At.IsZero()), "ChargeByRef = %+v, %v; want charge %s of 3 with 1 refunded, for %s by %s in %s", got, err, r.ChargeID, chargedOp, actor, f.Tenant)
	byID, err := f.Usage.GetCharge(f.Ctx, r.ChargeID)
	expect.That(t, !(err != nil || !reflect.DeepEqual(byID, got)), "GetCharge = %+v, %v; want what ChargeByRef read, %+v", byID, err, got)
	_, err = f.Usage.GetCharge(f.Ctx, uuid.New())
	expect.That(t, errors.Is(err, meter.ErrChargeNotFound), "GetCharge of an unknown id: err = %v, want ErrChargeNotFound", err)
	_, err = f.Usage.ChargeByRef(f.Ctx, meter.ChargeByRefRequest{CapabilityID: f.root, ExternalRef: "call-1"})
	expect.That(t, errors.Is(err, meter.ErrChargeNotFound), "the ref on another capability: err = %v, want ErrChargeNotFound", err)
	// A request that names no charge is the caller's bug, not a charge
	// missing (ChargeByRefRequest.Validate).
	for name, req := range map[string]meter.ChargeByRefRequest{
		"an empty ref":  {CapabilityID: f.child},
		"no capability": {ExternalRef: "call-1"},
	} {
		_, err := f.Usage.ChargeByRef(f.Ctx, req)
		expect.That(t, errors.Is(err, limes.ErrInvalidRequest), "ChargeByRef with %s: err = %v, want ErrInvalidRequest", name, err)
	}
}

func checkMalformed(t *testing.T, f fixture) {
	for name, req := range map[string]meter.ChargeRequest{
		"ref too long":      {ExternalRef: strings.Repeat("x", meter.MaxExternalRefBytes+1)},
		"ref not printable": {ExternalRef: "call\n1"},
		"unknown overrun":   {Overrun: meter.OverrunPolicyRecord + 1},
	} {
		req.Amount = 1
		_, err := f.charge(req, nil)
		expect.That(t, errors.Is(err, limes.ErrInvalidRequest), "%s: err = %v, want ErrInvalidRequest", name, err)
	}
	_, err := f.Usage.Settle(f.Ctx, meter.SettleRequest{
		Subject: f.childSubject(), ReservationID: uuid.New(), Amount: one, Overrun: meter.OverrunPolicyRecord + 1,
	}, nil)
	expect.That(t, errors.Is(err, limes.ErrInvalidRequest), "settle with an unknown overrun: err = %v, want ErrInvalidRequest", err)
	_, err = f.Usage.Reserve(f.Ctx, meter.ReserveRequest{
		Subject: f.childSubject(),
		Amount:  one,
		TTL:     -time.Minute,
	})
	expect.That(t, errors.Is(err, limes.ErrInvalidRequest), "reserve with a negative TTL: err = %v, want ErrInvalidRequest", err)
	// A malformed amount or unit is a malformed request, whatever else it
	// matches.
	for name, amount := range map[string]limes.Nanos{"negative": -one, "past MaxNanos": limes.MaxNanos + 1} {
		_, err := f.charge(meter.ChargeRequest{Amount: amount}, nil)
		expect.That(t, !(!errors.Is(err, limes.ErrInvalidAmount) || !errors.Is(err, limes.ErrInvalidRequest)), "a charge of a %s amount: err = %v, want ErrInvalidAmount and ErrInvalidRequest", name, err)
		_, err = f.Usage.Reserve(f.Ctx, meter.ReserveRequest{Subject: f.childSubject(), Amount: amount})
		expect.That(t, errors.Is(err, limes.ErrInvalidRequest), "a hold of a %s amount: err = %v, want ErrInvalidRequest", name, err)
	}
	// A settle of a malformed amount, below its hold or past MaxNanos: below
	// the hold no ceiling is asked, so only the amount's own check stops a
	// negative one lowering every counter.
	held, err := f.Usage.Reserve(f.Ctx, meter.ReserveRequest{Subject: f.childSubject(), Amount: one})
	expect.Must(t, err == nil, "%v", err)
	for name, amount := range map[string]limes.Nanos{"negative": -one, "past MaxNanos": limes.MaxNanos + 1} {
		_, err := f.Usage.Settle(f.Ctx, meter.SettleRequest{Subject: f.childSubject(), ReservationID: held.ID, Amount: amount}, nil)
		expect.That(t, errors.Is(err, limes.ErrInvalidAmount), "a settle of a %s amount: err = %v, want ErrInvalidAmount", name, err)
	}
	for name, req := range map[string]meter.SetTenantBudgetRequest{
		"in a unit that is not one": {TenantID: f.Tenant, Ceiling: limes.BudgetCeiling{Max: one, UnitCode: "usd"}},
		"for no tenant":             {Ceiling: limes.BudgetCeiling{Max: one}},
	} {
		_, err := f.Usage.SetTenantBudget(f.Ctx, req)
		expect.That(t, errors.Is(err, limes.ErrInvalidRequest), "a tenant budget %s: err = %v, want ErrInvalidRequest", name, err)
	}
	expect.That(t, f.spent(t, f.child) == 0, "a refused request moved spend")
}

// checkCounterOverflow holds a store to refusing, even past every ceiling,
// a charge that would take a counter past MaxNanos: each amount is valid,
// their sum has no room.
func checkCounterOverflow(t *testing.T, f fixture) {
	id, err := f.newCapability(uuid.Nil(), unlimitedBudget)
	expect.Must(t, err == nil, "%v", err)
	subject := newSubject(subjectSpec{capID: id, tenant: f.Tenant})
	_, err = f.charge(meter.ChargeRequest{Subject: subject, Amount: limes.MaxNanos, Overrun: meter.OverrunPolicyRecord}, nil)
	expect.Must(t, err == nil, "a charge of MaxNanos: %v", err)
	_, err = f.charge(meter.ChargeRequest{Subject: subject, Amount: 1, Overrun: meter.OverrunPolicyRecord}, nil)
	expect.That(t, !(!errors.Is(err, meter.ErrCounterOverflow) || errors.Is(err, limes.ErrInvalidRequest)), "a nano past MaxNanos on the counter: err = %v, want ErrCounterOverflow and not ErrInvalidRequest", err)
	got := f.spent(t, id)
	expect.That(t, got == limes.MaxNanos, "spent %s after the overflow was refused, want %s", got, limes.MaxNanos)
	// Each counter on its own: a fresh capability under no full one meets
	// only the tenant's full counter, and once the tenant is reset, a fresh
	// child meets only its parent's.
	fresh, err := f.newCapability(uuid.Nil(), unlimitedBudget)
	expect.Must(t, err == nil, "%v", err)
	_, err = f.charge(meter.ChargeRequest{Subject: newSubject(subjectSpec{capID: fresh}), Amount: 1, Overrun: meter.OverrunPolicyRecord}, nil)
	expect.That(t, errors.Is(err, meter.ErrCounterOverflow), "a nano past MaxNanos on the tenant alone: err = %v, want ErrCounterOverflow", err)
	b, err := f.Usage.GetTenantBudget(f.Ctx, f.Tenant)
	expect.Must(t, err == nil, "%v", err)
	_, err = f.Usage.SetTenantBudget(f.Ctx, meter.SetTenantBudgetRequest{
		TenantID: f.Tenant, Ceiling: limes.BudgetCeiling{Unlimited: true, UnitCode: unit}, ResetSpend: true, ExpectedVersion: b.ResourceVersion,
	})
	expect.Must(t, err == nil, "%v", err)
	child, err := f.newCapability(id, unlimitedBudget)
	expect.Must(t, err == nil, "%v", err)
	_, err = f.charge(meter.ChargeRequest{Subject: newSubject(subjectSpec{capID: child}), Amount: 1, Overrun: meter.OverrunPolicyRecord}, nil)
	expect.That(t, errors.Is(err, meter.ErrCounterOverflow), "a nano past MaxNanos on the parent alone: err = %v, want ErrCounterOverflow", err)
}

func checkOverrunRecord(t *testing.T, f fixture) {
	// 40 crosses the child's 20, the root's 25 and the tenant's 30.
	const amount = 40 * one
	_, err := f.charge(meter.ChargeRequest{Amount: amount}, nil)
	expect.Must(t, err != nil, "a rejecting charge past every ceiling was accepted")
	r, err := f.charge(meter.ChargeRequest{Amount: amount, Overrun: meter.OverrunPolicyRecord}, nil)
	expect.Must(t, !(err != nil || !r.Overrun || r.Replayed || r.SpentAmount != amount), "recording charge = %+v, %v; want %v spent, overrun", r, err, amount)
	got := f.spent(t, f.root)
	expect.That(t, got == amount, "root spent %v, want %v", got, amount)
	got = f.tenantSpent(t)
	expect.That(t, got == amount, "tenant spent %v, want %v", got, amount)
	_, err = f.charge(meter.ChargeRequest{Amount: cent}, nil)
	expect.That(t, errors.Is(err, limes.ErrBudgetExceeded), "after an overrun a rejecting charge: err = %v, want ErrBudgetExceeded", err)
	_, err = f.Usage.Reserve(f.Ctx, meter.ReserveRequest{
		Subject: f.childSubject(),
		Amount:  cent,
	})
	expect.That(t, errors.Is(err, limes.ErrBudgetExceeded), "after an overrun a reservation: err = %v, want ErrBudgetExceeded", err)
}

func checkSettle(t *testing.T, f fixture) {
	res, err := f.Usage.Reserve(f.Ctx, meter.ReserveRequest{
		Subject: f.childSubject(),
		Amount:  10 * one,
	})
	expect.Must(t, err == nil, "reserve: %v", err)
	settle := func(amount limes.Nanos, p meter.OverrunPolicy) (meter.ChargeReceipt, error) {
		return f.Usage.Settle(f.Ctx, meter.SettleRequest{
			Subject:       f.childSubject(),
			ReservationID: res.ID,
			Amount:        amount,
			Overrun:       p,
		}, nil)
	}
	_, err = settle(24*one, meter.OverrunPolicyReject)
	expect.Must(t, errors.Is(err, limes.ErrBudgetExceeded), "rejecting settle past the ceiling: err = %v, want ErrBudgetExceeded", err)
	r, err := settle(24*one, meter.OverrunPolicyRecord)
	expect.Must(t, !(err != nil || !r.Overrun || r.SpentAmount != 24*one), "recording settle = %+v, %v; want 24 spent, overrun", r, err)
	u, err := f.Usage.GetUsage(f.Ctx, f.root)
	expect.That(t, !(err != nil || u.ReservedAmount != 0), "root after settling = %+v, %v; want nothing held", u, err)
	// Replayed for the same amount, under any policy: the first receipt.
	again, err := settle(24*one, meter.OverrunPolicyReject)
	expect.That(t, !(err != nil || !again.Replayed || !again.Overrun || again.ChargeID != r.ChargeID || again.SpentAmount != 24*one), "settling again = %+v, %v; want a replay of %s", again, err, r.ChargeID)
	err = f.Usage.Release(f.Ctx, meter.ReleaseRequest{Subject: f.childSubject(), ReservationID: res.ID})
	expect.That(t, err == nil, "releasing a settled reservation: %v", err)
	onRoot := newSubject(subjectSpec{capID: f.root, tenant: f.Tenant, budget: budgetOf(rootBudget), unit: unit})
	other, err := f.Usage.Reserve(f.Ctx, meter.ReserveRequest{Subject: onRoot, Amount: 0})
	expect.Must(t, err == nil, "reserve nothing: %v", err)
	err = f.Usage.Release(f.Ctx, meter.ReleaseRequest{Subject: onRoot, ReservationID: other.ID})
	expect.Must(t, err == nil, "%v", err)
	_, err = f.Usage.Settle(f.Ctx, meter.SettleRequest{Subject: newSubject(subjectSpec{capID: f.root, tenant: f.Tenant, budget: budgetOf(rootBudget)}), ReservationID: other.ID, Amount: 0}, nil)
	expect.That(t, errors.Is(err, meter.ErrReservationNotFound), "settling a released reservation: err = %v, want ErrReservationNotFound", err)
}

// checkSettleNil holds a store to keeping direct charges apart from settled
// reservations: a direct charge has no reservation, and a store that files it
// under the zero id would replay it to a Settle that names none.
func checkSettleNil(t *testing.T, f fixture) {
	_, err := f.charge(meter.ChargeRequest{Amount: one}, nil)
	expect.Must(t, err == nil, "%v", err)
	r, err := f.Usage.Settle(f.Ctx, meter.SettleRequest{Subject: f.childSubject(), ReservationID: uuid.Nil(), Amount: one}, nil)
	expect.Must(t, errors.Is(err, meter.ErrReservationNotFound), "Settle of the zero reservation id = %+v, %v; want ErrReservationNotFound", r, err)
	got := f.spent(t, f.child)
	expect.That(t, got == one, "child spent %v after a refused settle, want 1", got)
}

// checkSettleOwner holds a store to settling a reservation only for the
// capability that made it: a reservation id is no bearer credential, so a
// subject naming another capability finds no such hold, before the settle
// and after it, and the hold stays where it was.
func checkSettleOwner(t *testing.T, f fixture) {
	res, err := f.Usage.Reserve(f.Ctx, meter.ReserveRequest{Subject: f.childSubject(), Amount: 2 * one})
	expect.Must(t, err == nil, "%v", err)
	stranger := newSubject(subjectSpec{capID: f.root, tenant: f.Tenant, budget: budgetOf(rootBudget)})
	_, err = f.Usage.Settle(f.Ctx, meter.SettleRequest{Subject: stranger, ReservationID: res.ID, Amount: one}, nil)
	expect.That(t, errors.Is(err, meter.ErrReservationNotFound), "settling the child's hold as the root: err = %v, want ErrReservationNotFound", err)
	// The hold's capability under another tenant is not its owner either; a
	// store that scopes tenants may refuse the call as out of scope instead.
	elsewhere := newSubject(subjectSpec{capID: f.child, tenant: uuid.New(), budget: budgetOf(childBudget)})
	_, err = f.Usage.Settle(f.Ctx, meter.SettleRequest{Subject: elsewhere, ReservationID: res.ID, Amount: one}, nil)
	expect.That(t, !(!errors.Is(err, meter.ErrReservationNotFound) && !errors.Is(err, limes.ErrScopeMismatch)), "settling the child's hold under another tenant: err = %v, want ErrReservationNotFound or ErrScopeMismatch", err)
	u, err := f.Usage.GetUsage(f.Ctx, f.child)
	expect.That(t, !(err != nil || u.ReservedAmount != 2*one || u.SpentAmount != 0), "child after a refused settle = %+v, %v; want 2 held, nothing spent", u, err)
	_, err = f.Usage.Settle(f.Ctx, meter.SettleRequest{Subject: f.childSubject(), ReservationID: res.ID, Amount: one}, nil)
	expect.Must(t, err == nil, "settling as the owner: %v", err)
	r, err := f.Usage.Settle(f.Ctx, meter.SettleRequest{Subject: stranger, ReservationID: res.ID, Amount: one}, nil)
	expect.That(t, errors.Is(err, meter.ErrReservationNotFound), "replaying the settle as the root = %+v, %v; want ErrReservationNotFound", r, err)
}

// checkRefundAcrossReset holds a store to keeping accounting periods apart: a
// charge counted before the tenant's spend was reset is not in the new
// period's spend, so refunding it must not take it out of that spend.
func checkRefundAcrossReset(t *testing.T, f fixture) {
	const amount = 9 * one
	before, err := f.charge(meter.ChargeRequest{Amount: amount}, nil)
	expect.Must(t, err == nil, "%v", err)
	b, err := f.Usage.GetTenantBudget(f.Ctx, f.Tenant)
	expect.Must(t, err == nil, "%v", err)
	_, err = f.Usage.SetTenantBudget(f.Ctx, meter.SetTenantBudgetRequest{
		TenantID: f.Tenant, Ceiling: limes.BudgetCeiling{Max: tenantBudget, UnitCode: unit},
		ResetSpend: true, ExpectedVersion: b.ResourceVersion,
	})
	expect.Must(t, err == nil, "reset: %v", err)
	after, err := f.charge(meter.ChargeRequest{Amount: amount}, nil)
	expect.Must(t, err == nil, "%v", err)
	r, err := f.Usage.Refund(f.Ctx, meter.RefundRequest{ChargeID: before.ChargeID, Remaining: true})
	expect.Must(t, !(err != nil || r.Amount != amount), "refund of the earlier period's charge = %+v, %v; want %v", r, err, amount)
	got := f.tenantSpent(t)
	expect.That(t, got == amount, "tenant spent %v in the new period, want %v: the refund came out of a period that never counted it", got, amount)
	got = f.spent(t, f.child)
	expect.That(t, got == amount, "child spent %v, want %v: the refund still returns to the capability", got, amount)
	// A charge of the new period returns to it.
	_, err = f.Usage.Refund(f.Ctx, meter.RefundRequest{ChargeID: after.ChargeID, Remaining: true})
	expect.Must(t, err == nil, "%v", err)
	got = f.tenantSpent(t)
	expect.That(t, got == 0, "tenant spent %v after refunding the new period's charge, want 0", got)
}

// checkAncestorUnit holds a store to giving an ancestor counted only through
// a child — by a charge or by a hold — the unit the child counts in, as
// GetUsage reads it back.
func checkAncestorUnit(t *testing.T, f fixture) {
	tree := func() (parent, child uuid.UUID) {
		t.Helper()
		parent, err := f.newCapability(uuid.Nil(), unlimitedBudget)
		expect.Must(t, err == nil, "%v", err)
		child, err = f.newCapability(parent, unlimitedBudget)
		expect.Must(t, err == nil, "%v", err)
		return parent, child
	}
	charged, chargedChild := tree()
	_, err := f.Usage.Charge(f.Ctx, meter.ChargeRequest{Subject: newSubject(subjectSpec{capID: chargedChild, tenant: f.Tenant, unit: unit}), Amount: one}, nil)
	expect.Must(t, err == nil, "%v", err)
	held, heldChild := tree()
	_, err = f.Usage.Reserve(f.Ctx, meter.ReserveRequest{Subject: newSubject(subjectSpec{capID: heldChild, tenant: f.Tenant, unit: unit}), Amount: one})
	expect.Must(t, err == nil, "%v", err)
	for name, id := range map[string]uuid.UUID{"charged": charged, "held": held} {
		u, err := f.Usage.GetUsage(f.Ctx, id)
		expect.That(t, !(err != nil || u.UnitCode != unit), "the parent of a %s child: usage %+v, %v; want counted in %q", name, u, err, unit)
	}
}

// checkNoUnit holds a store to a subject that names no unit: it is bumped,
// with its counters left in none, and no amount is charged, held or settled
// on it; the first charge that names a unit pins it.
func checkNoUnit(t *testing.T, f fixture) {
	none := newSubject(subjectSpec{capID: f.child, tenant: f.Tenant, unitless: true})
	_, err := f.Usage.Bump(f.Ctx, meter.BumpRequest{Subject: none})
	expect.Must(t, err == nil, "Bump of a subject with no unit: %v", err)
	u, err := f.Usage.GetUsage(f.Ctx, f.child)
	expect.That(t, !(err != nil || u.UnitCode != "" || u.RequestCount != 1), "usage after a bump with no unit = %+v, %v; want one request in no unit", u, err)
	for name, call := range map[string]func() error{
		"Charge": func() error {
			_, err := f.Usage.Charge(f.Ctx, meter.ChargeRequest{Subject: none, Amount: one}, nil)
			return err
		},
		"Reserve": func() error {
			_, err := f.Usage.Reserve(f.Ctx, meter.ReserveRequest{Subject: none, Amount: one})
			return err
		},
		"Settle": func() error {
			_, err := f.Usage.Settle(f.Ctx, meter.SettleRequest{Subject: none, ReservationID: uuid.New(), Amount: one}, nil)
			return err
		},
	} {
		err := call()
		expect.That(t, errors.Is(err, limes.ErrInvalidRequest), "%s of a subject with no unit: err = %v, want ErrInvalidRequest", name, err)
	}
	_, err = f.charge(meter.ChargeRequest{Amount: one}, nil)
	expect.Must(t, err == nil, "charge in %s: %v", unit, err)
	u, err = f.Usage.GetUsage(f.Ctx, f.child)
	expect.That(t, !(err != nil || u.UnitCode != unit), "usage after a charge in %s = %+v, %v; want it counted in %s", unit, u, err, unit)
}

// checkTenantCeilingUnit holds a store to a tenant ceiling in the unit it
// names: one that lets anything be spent names it, the tenant counts in it,
// and a charge in another is ErrUnitCodeMismatch. A tenant with no ceiling
// set counts in the unit of what it was first charged or held, under an
// unlimited ceiling in that unit.
func checkTenantCeilingUnit(t *testing.T, f fixture) {
	fresh := func() (tenant, id uuid.UUID, ctx context.Context) {
		t.Helper()
		tenant, err := f.newTenant()
		expect.Must(t, err == nil, "%v", err)
		id, err = f.NewCapability(CapabilitySpec{Tenant: tenant, Requests: limes.RequestCeiling{Unlimited: true}, Budget: limes.BudgetCeiling{Unlimited: true, UnitCode: otherUnit}})
		expect.Must(t, err == nil, "%v", err)
		return tenant, id, f.tenantCtx(tenant)
	}
	tenant, id, ctx := fresh()
	_, err := f.Usage.SetTenantBudget(ctx, meter.SetTenantBudgetRequest{TenantID: tenant, Ceiling: limes.BudgetCeiling{Unlimited: true}})
	expect.That(t, errors.Is(err, limes.ErrInvalidRequest), "SetTenantBudget, an unlimited ceiling in no unit: err = %v, want ErrInvalidRequest", err)
	b, err := f.Usage.SetTenantBudget(ctx, meter.SetTenantBudgetRequest{TenantID: tenant, Ceiling: limes.BudgetCeiling{Unlimited: true, UnitCode: otherUnit}})
	expect.Must(t, !(err != nil || b.Ceiling.UnitCode != otherUnit), "SetTenantBudget in %s = %+v, %v; want a ceiling in it", otherUnit, b, err)
	_, err = f.Usage.Charge(ctx, meter.ChargeRequest{Subject: newSubject(subjectSpec{capID: id, tenant: tenant, unit: unit}), Amount: one}, nil)
	expect.That(t, errors.Is(err, limes.ErrUnitCodeMismatch), "a charge in %s on a tenant ceiling in %s: err = %v, want ErrUnitCodeMismatch", unit, otherUnit, err)
	_, err = f.Usage.Charge(ctx, meter.ChargeRequest{Subject: newSubject(subjectSpec{capID: id, tenant: tenant, unit: otherUnit}), Amount: one}, nil)
	expect.That(t, err == nil, "a charge in the tenant ceiling's %s: %v", otherUnit, err)
	held, heldID, heldCtx := fresh()
	_, err = f.Usage.Reserve(heldCtx, meter.ReserveRequest{Subject: newSubject(subjectSpec{capID: heldID, tenant: held, unit: otherUnit}), Amount: one})
	expect.Must(t, err == nil, "hold in %s: %v", otherUnit, err)
	want := limes.BudgetCeiling{Unlimited: true, UnitCode: otherUnit}
	b, err = f.Usage.GetTenantBudget(heldCtx, held)
	expect.That(t, !(err != nil || b.Ceiling != want), "tenant budget after a first hold in %s = %+v, %v; want ceiling %s", otherUnit, b, err, want)
}

// checkOneUnit holds a store to counting each counter in one unit: an amount
// in another is not comparable with what is there, and adding it would sum
// two currencies as one.
func checkOneUnit(t *testing.T, f fixture) {
	_, err := f.charge(meter.ChargeRequest{Amount: one}, nil)
	expect.Must(t, err == nil, "%v", err)
	other := meter.ChargeRequest{Subject: newSubject(subjectSpec{capID: f.child, tenant: f.Tenant, budget: budgetOf(childBudget), unit: otherUnit}), Amount: one}
	_, err = f.Usage.Charge(f.Ctx, other, nil)
	expect.That(t, errors.Is(err, limes.ErrUnitCodeMismatch), "a charge in %s on a %s counter: err = %v, want ErrUnitCodeMismatch", otherUnit, unit, err)
	_, err = f.Usage.Reserve(f.Ctx, meter.ReserveRequest{
		Subject: newSubject(subjectSpec{capID: f.child, tenant: f.Tenant, budget: budgetOf(childBudget), unit: otherUnit}),
		Amount:  one,
	})
	expect.That(t, errors.Is(err, limes.ErrUnitCodeMismatch), "a hold in %s on a %s counter: err = %v, want ErrUnitCodeMismatch", otherUnit, unit, err)
	// A fresh capability has no unit of its own yet, but its tenant does.
	fresh, err := f.newCapability(uuid.Nil(), unlimitedBudget)
	expect.Must(t, err == nil, "%v", err)
	other.Subject = newSubject(subjectSpec{capID: fresh, tenant: f.Tenant, unit: otherUnit})
	_, err = f.Usage.Charge(f.Ctx, other, nil)
	expect.That(t, errors.Is(err, limes.ErrUnitCodeMismatch), "a charge in %s under a %s tenant: err = %v, want ErrUnitCodeMismatch", otherUnit, unit, err)
	expect.That(t, !(f.spent(t, f.child) != one || f.spent(t, fresh) != 0 || f.tenantSpent(t) != one), "a refused charge in another unit moved spend")
	b, err := f.Usage.GetTenantBudget(f.Ctx, f.Tenant)
	expect.Must(t, err == nil, "%v", err)
	_, err = f.Usage.SetTenantBudget(f.Ctx, meter.SetTenantBudgetRequest{
		TenantID: f.Tenant, Ceiling: limes.BudgetCeiling{Max: tenantBudget, UnitCode: otherUnit}, ExpectedVersion: b.ResourceVersion,
	})
	expect.That(t, errors.Is(err, limes.ErrUnitCodeMismatch), "re-denominating a tenant with %s spent: err = %v, want ErrUnitCodeMismatch", unit, err)
}

// checkSettleAfterAncestorDropped holds Settle to the lineage as it stands:
// an ancestor whose row DeleteUsage dropped after the hold was made no
// longer holds it, so the settled amount must fit its ceiling whole, and is
// counted on it once it does.
func checkSettleAfterAncestorDropped(t *testing.T, f fixture) {
	// The root holds 8, spends 18 once its row is dropped, and then holds
	// nothing of the hold: settling all 8 would take it to 26, past its 25;
	// settling 7 takes it to exactly 25.
	const held, rootSpend, admitted = 8 * one, 18 * one, 7 * one
	r, err := f.Usage.Reserve(f.Ctx, meter.ReserveRequest{Subject: f.childSubject(), Amount: held})
	expect.Must(t, err == nil, "%v", err)
	err = f.Usage.DeleteUsage(f.Ctx, meter.DeleteUsageRequest{CapabilityID: f.root})
	expect.Must(t, err == nil, "%v", err)
	_, err = f.Usage.Charge(f.Ctx, meter.ChargeRequest{
		Subject: newSubject(subjectSpec{capID: f.root, tenant: f.Tenant, budget: budgetOf(rootBudget)}), Amount: rootSpend,
	}, nil)
	expect.Must(t, err == nil, "%v", err)
	settle := func(amount limes.Nanos) error {
		_, err := f.Usage.Settle(f.Ctx, meter.SettleRequest{Subject: f.childSubject(), ReservationID: r.ID, Amount: amount}, nil)
		return err
	}
	err = settle(held)
	expect.That(t, errors.Is(err, limes.ErrBudgetExceeded), "a settle within its hold but past the root's ceiling, the root's row dropped: err = %v, want ErrBudgetExceeded", err)
	err = settle(admitted)
	expect.Must(t, err == nil, "a settle within the root's ceiling: %v", err)
	got := f.spent(t, f.root)
	expect.That(t, got == rootSpend+admitted, "root spent %v after the settle, want %v counted on it", got, rootSpend+admitted)
}

// checkExactCeilings holds a store to an ancestor's and a tenant's ceiling
// to the nano: a charge reaching one exactly is admitted, a nano more is not.
func checkExactCeilings(t *testing.T, f fixture) {
	const nano limes.Nanos = 1
	under, err := f.newCapability(f.root, unlimitedBudget)
	expect.Must(t, err == nil, "%v", err)
	charge := func(capID uuid.UUID, amount limes.Nanos) error {
		_, err := f.Usage.Charge(f.Ctx, meter.ChargeRequest{Subject: newSubject(subjectSpec{capID: capID, tenant: f.Tenant}), Amount: amount}, nil)
		return err
	}
	err = charge(under, rootBudget)
	expect.That(t, err == nil, "a charge taking the root to its ceiling exactly: %v", err)
	err = charge(under, nano)
	expect.That(t, errors.Is(err, limes.ErrBudgetExceeded), "a nano past the root's ceiling: err = %v, want ErrBudgetExceeded", err)
	other, err := f.newCapability(uuid.Nil(), unlimitedBudget)
	expect.Must(t, err == nil, "%v", err)
	err = charge(other, tenantBudget-rootBudget)
	expect.That(t, err == nil, "a charge taking the tenant to its ceiling exactly: %v", err)
	err = charge(other, nano)
	expect.That(t, errors.Is(err, limes.ErrTenantBudgetExceeded), "a nano past the tenant's ceiling: err = %v, want ErrTenantBudgetExceeded", err)
}

// checkHoldCreatesTenant holds a store to counting a hold on a tenant whose
// first act it is: a ceiling set afterwards must see it.
func checkHoldCreatesTenant(t *testing.T, f fixture) {
	tenant, err := f.newTenant()
	expect.Must(t, err == nil, "%v", err)
	id, err := f.NewCapability(CapabilitySpec{Tenant: tenant, Requests: limes.RequestCeiling{Unlimited: true}, Budget: unlimitedBudget})
	expect.Must(t, err == nil, "%v", err)
	_, err = f.Usage.Reserve(f.tenantCtx(tenant), meter.ReserveRequest{Subject: newSubject(subjectSpec{capID: id, tenant: tenant}), Amount: one})
	expect.Must(t, err == nil, "%v", err)
	b, err := f.Usage.GetTenantBudget(f.tenantCtx(tenant), tenant)
	expect.That(t, !(err != nil || b.ReservedAmount != one), "a tenant whose first act is a hold: %+v, %v; want %v held", b, err, one)
}

// checkSettleReplay holds a store to a settle replayed: the same receipt,
// and onCharged not run again, so a side effect committed with the first is
// not committed twice; for another amount, a conflict.
func checkSettleReplay(t *testing.T, f fixture) {
	r, err := f.Usage.Reserve(f.Ctx, meter.ReserveRequest{Subject: f.childSubject(), Amount: 2 * one, Op: "llm", Actor: "agent"})
	expect.Must(t, err == nil, "%v", err)
	req := meter.SettleRequest{Subject: f.childSubject(), ReservationID: r.ID, Amount: one}
	first, err := f.Usage.Settle(f.Ctx, req, nil)
	expect.Must(t, err == nil, "%v", err)
	ran := false
	again, err := f.Usage.Settle(f.Ctx, req, func(context.Context, meter.Charged) error { ran = true; return nil })
	expect.That(t, !(err != nil || !again.Replayed || again.ChargeID != first.ChargeID || again.SpentAmount != first.SpentAmount), "a replayed settle = %+v, %v; want the first's receipt %+v, replayed", again, err, first)
	expect.That(t, !(ran), "a replayed settle ran onCharged again")
	// The reservation names one cost: settling it again for another amount
	// is a conflict, not the first receipt, and moves nothing.
	other := req
	other.Amount = 2 * one
	_, err = f.Usage.Settle(f.Ctx, other, func(context.Context, meter.Charged) error { ran = true; return nil })
	expect.That(t, !(!errors.Is(err, meter.ErrSettleConflict) || !errors.Is(err, limes.ErrConflict)), "a settle replayed for another amount: err = %v, want ErrSettleConflict, matching ErrConflict", err)
	expect.That(t, !(ran || f.spent(t, f.child) != one), "a settle replayed for another amount ran onCharged or moved spend to %v; want %v", f.spent(t, f.child), one)
	charge, err := f.Usage.GetCharge(f.Ctx, first.ChargeID)
	expect.That(t, !(err != nil || charge.Op != "llm" || charge.Actor != "agent"), "the settled charge = %+v, %v; want the hold's op and actor", charge, err)
}

// checkReleaseReturnsCopy holds a store to returning a hold to the copy it
// was made under, by Release and by ReleaseExpired: a hold left on the copy
// blocks it for good.
func checkReleaseReturnsCopy(t *testing.T, f fixture) {
	reader, ok := f.Usage.(meter.CopyUsageReader)
	if !ok {
		return // a Meter that does not read copies back is checked no further
	}
	const ttl = time.Minute
	for name, release := range map[string]func(subject limes.MeterSubject, id uuid.UUID) error{
		"Release": func(subject limes.MeterSubject, id uuid.UUID) error {
			return f.Usage.Release(f.Ctx, meter.ReleaseRequest{Subject: subject, ReservationID: id})
		},
		"ReleaseExpired": func(limes.MeterSubject, uuid.UUID) error {
			f.Advance(2 * ttl)
			_, err := f.Usage.ReleaseExpired(f.Ctx, meter.ReleaseExpiredRequest{})
			return err
		},
	} {
		if name == "ReleaseExpired" && f.Advance == nil {
			continue
		}
		copyID := []byte("copy-" + name)
		copies := []limes.CopyCeiling{{RevocationID: copyID, Budget: &limes.BudgetCeiling{Max: childBudget, UnitCode: unit}}}
		subject := newSubject(subjectSpec{capID: f.child, tenant: f.Tenant, copies: copies})
		r, err := f.Usage.Reserve(f.Ctx, meter.ReserveRequest{Subject: subject, Amount: one, TTL: ttl})
		expect.Must(t, err == nil, "%v", err)
		err = release(subject, r.ID)
		expect.Must(t, err == nil, "%v", err)
		got := f.copyUsage(t, reader, f.child, copyID)
		expect.That(t, !(len(got) != 1 || got[0].ReservedAmount != 0), "the copy after %s = %+v; want nothing held", name, got)
	}
}

// checkRefusedBump holds a store to a Bump refused by a copy's request
// ceiling moving no counter.
func checkRefusedBump(t *testing.T, f fixture) {
	frozen := []limes.CopyCeiling{{RevocationID: []byte("frozen"), Requests: &limes.RequestCeiling{}}}
	_, err := f.Usage.Bump(f.Ctx, meter.BumpRequest{Subject: newSubject(subjectSpec{capID: f.child, tenant: f.Tenant, copies: frozen})})
	expect.Must(t, errors.Is(err, limes.ErrRequestLimitExceeded), "a bump past a frozen copy: err = %v, want ErrRequestLimitExceeded", err)
	for name, id := range map[string]uuid.UUID{"capability": f.child, "root": f.root} {
		u, err := f.Usage.GetUsage(f.Ctx, id)
		expect.Must(t, !(err != nil && !errors.Is(err, meter.ErrUsageNotFound)), "%v", err)
		expect.That(t, u.RequestCount == 0, "the %s counted %d requests after a refused bump, want 0", name, u.RequestCount)
	}
}

// checkRangeCountsHolds holds a store to the counter range with what is held
// counted: holds summing past MaxNanos would leave a counter it cannot
// keep, so the one that would is ErrCounterOverflow.
func checkRangeCountsHolds(t *testing.T, f fixture) {
	tenant, err := f.newTenant()
	expect.Must(t, err == nil, "%v", err)
	id, err := f.NewCapability(CapabilitySpec{Tenant: tenant, Requests: limes.RequestCeiling{Unlimited: true}, Budget: unlimitedBudget})
	expect.Must(t, err == nil, "%v", err)
	reserve := func(amount limes.Nanos) error {
		_, err := f.Usage.Reserve(f.tenantCtx(tenant), meter.ReserveRequest{Subject: newSubject(subjectSpec{capID: id, tenant: tenant}), Amount: amount})
		return err
	}
	err = reserve(limes.MaxNanos)
	expect.Must(t, err == nil, "%v", err)
	err = reserve(1)
	expect.That(t, errors.Is(err, meter.ErrCounterOverflow), "a hold past MaxNanos held: err = %v, want ErrCounterOverflow", err)
}

// checkRefundExactRemainder holds a store to a refund of an Amount exactly
// what is left of the charge: it is refunded whole, not refused.
func checkRefundExactRemainder(t *testing.T, f fixture) {
	const charged, first = 3 * one, one
	receipt, err := f.charge(meter.ChargeRequest{Amount: charged}, nil)
	expect.Must(t, err == nil, "%v", err)
	refund := func(amount limes.Nanos) (meter.RefundReceipt, error) {
		return f.Usage.Refund(f.Ctx, meter.RefundRequest{ChargeID: receipt.ChargeID, Amount: amount})
	}
	_, err = refund(first)
	expect.Must(t, err == nil, "%v", err)
	got, err := refund(charged - first)
	expect.That(t, !(err != nil || got.Amount != charged-first), "a refund of exactly what is left = %+v, %v; want %v refunded", got, err, charged-first)
	spent := f.spent(t, f.child)
	expect.That(t, spent == 0, "spent %v after refunding the whole charge, want 0", spent)
}

// checkSettleAfterRemovedAncestor holds a store to writing no row for an
// ancestor that PurgeOrphans has dropped: a hold settled afterwards counts
// on the lineage still on record, and the removed one stays gone.
func checkSettleAfterRemovedAncestor(t *testing.T, f fixture) {
	if f.DeleteCapability == nil {
		t.Skip("Env.DeleteCapability not set")
	}
	r, err := f.Usage.Reserve(f.Ctx, meter.ReserveRequest{Subject: f.childSubject(), Amount: one})
	expect.Must(t, err == nil, "%v", err)
	err = f.DeleteCapability(f.root)
	expect.Must(t, err == nil, "%v", err)
	_, err = f.Usage.PurgeOrphans(f.Ctx, meter.PurgeOrphansRequest{})
	expect.Must(t, err == nil, "%v", err)
	_, err = f.Usage.Settle(f.Ctx, meter.SettleRequest{Subject: f.childSubject(), ReservationID: r.ID, Amount: one}, nil)
	expect.Must(t, err == nil, "%v", err)
	u, err := f.Usage.GetUsage(f.Ctx, f.root)
	expect.That(t, errors.Is(err, meter.ErrUsageNotFound), "the removed root after a settle: %+v, %v; want no row", u, err)
}

// checkReplayAfterRemoval holds a store to answering a replay the same way
// for a charge and a settle once the capability is off record: each gets the
// receipt it was given, Replayed, and moves nothing.
func checkReplayAfterRemoval(t *testing.T, f fixture) {
	if f.DeleteCapability == nil {
		t.Skip("Env.DeleteCapability not set")
	}
	const ref = "cost-1"
	charge := meter.ChargeRequest{Subject: f.childSubject(), Amount: one, ExternalRef: ref}
	charged, err := f.Usage.Charge(f.Ctx, charge, nil)
	expect.Must(t, err == nil, "%v", err)
	r, err := f.Usage.Reserve(f.Ctx, meter.ReserveRequest{Subject: f.childSubject(), Amount: one})
	expect.Must(t, err == nil, "%v", err)
	settle := meter.SettleRequest{Subject: f.childSubject(), ReservationID: r.ID, Amount: one}
	settled, err := f.Usage.Settle(f.Ctx, settle, nil)
	expect.Must(t, err == nil, "%v", err)
	err = f.DeleteCapability(f.child)
	expect.Must(t, err == nil, "%v", err)
	got, err := f.Usage.Charge(f.Ctx, charge, nil)
	expect.That(t, !(err != nil || !got.Replayed || got.ChargeID != charged.ChargeID), "a charge replayed after its capability is gone = %+v, %v; want the first receipt, replayed", got, err)
	got, err = f.Usage.Settle(f.Ctx, settle, nil)
	expect.That(t, !(err != nil || !got.Replayed || got.ChargeID != settled.ChargeID), "a settle replayed after its capability is gone = %+v, %v; want the first receipt, replayed", got, err)
}

// checkDeleteReleasesHolds holds a store to releasing what a reaped
// capability held: nothing can settle or release a hold of a capability that
// is gone, so a hold left behind counts against its ancestors and its tenant
// until the store is rebuilt.
func checkDeleteReleasesHolds(t *testing.T, f fixture) {
	res, err := f.Usage.Reserve(f.Ctx, meter.ReserveRequest{
		Subject: f.childSubject(),
		Amount:  3 * one,
	})
	expect.Must(t, err == nil, "%v", err)
	// A capability with no row is no error: there is nothing to drop.
	err = f.Usage.DeleteUsage(f.Ctx, meter.DeleteUsageRequest{CapabilityID: uuid.New()})
	expect.That(t, err == nil, "DeleteUsage of a capability with no row: %v", err)
	err = f.Usage.DeleteUsage(f.Ctx, meter.DeleteUsageRequest{CapabilityID: f.child})
	expect.Must(t, err == nil, "DeleteUsage: %v", err)
	_, err = f.Usage.GetReservation(f.Ctx, res.ID)
	expect.That(t, errors.Is(err, meter.ErrReservationNotFound), "the deleted capability's hold: err = %v, want ErrReservationNotFound", err)
	u, err := f.Usage.GetUsage(f.Ctx, f.root)
	expect.That(t, !(err != nil || u.ReservedAmount != 0), "root after deleting its child = %+v, %v; want nothing held", u, err)
	b, err := f.Usage.GetTenantBudget(f.Ctx, f.Tenant)
	expect.That(t, !(err != nil || b.ReservedAmount != 0), "tenant after deleting the child = %+v, %v; want nothing held", b, err)
}

// GetTenantBudget, SetTenantBudget and ListTenantBudgets read one row; each
// returns all of it.
func checkTenantBudgetReads(t *testing.T, f fixture) {
	const held = 3 * one
	_, err := f.Usage.Reserve(f.Ctx, meter.ReserveRequest{
		Subject: f.childSubject(),
		Amount:  held,
	})
	expect.Must(t, err == nil, "%v", err)
	got, err := f.Usage.GetTenantBudget(f.Ctx, f.Tenant)
	expect.Must(t, !(err != nil || got.ReservedAmount != held || got.ResourceVersion == 0), "GetTenantBudget = %+v, %v; want %v held and a version", got, err, held)
	set, err := f.Usage.SetTenantBudget(f.Ctx, meter.SetTenantBudgetRequest{
		TenantID: f.Tenant, Ceiling: limes.BudgetCeiling{Max: tenantBudget, UnitCode: unit}, ExpectedVersion: got.ResourceVersion,
	})
	// The version is an opaque token: a write changes it, and it is never 0
	// once a ceiling is set — nothing says by how much it moves.
	expect.That(t, !(err != nil || set.ReservedAmount != held || set.ResourceVersion == got.ResourceVersion || set.ResourceVersion == 0), "SetTenantBudget = %+v, %v; want %v held and a new version", set, err, held)
	// Nothing is spent and the period is open-ended; every other field holds
	// a value, so one the store does not keep reads back unset.
	open := []string{"Ceiling.Unlimited", "SpentAmount", "PeriodEnd"}
	unset := fields.Unset(set)
	expect.That(t, slices.Equal(unset, open), "SetTenantBudget = %+v: %v unset, want only %v", set, unset, open)
	if read, err := f.Usage.GetTenantBudget(f.Ctx, f.Tenant); err != nil {
		t.Errorf("GetTenantBudget after a set: %v", err)
	} else if diff := sameTenantBudget(read, set); diff != "" {
		t.Errorf("GetTenantBudget = %+v, SetTenantBudget returned %+v: %s", read, set, diff)
	}
	for _, ceiling := range []limes.Nanos{-1, limes.MaxNanos + 1} {
		_, err := f.Usage.SetTenantBudget(f.Ctx, meter.SetTenantBudgetRequest{
			TenantID: f.Tenant, Ceiling: limes.BudgetCeiling{Max: ceiling, UnitCode: unit}, ExpectedVersion: set.ResourceVersion,
		})
		expect.That(t, errors.Is(err, limes.ErrInvalidAmount), "SetTenantBudget(%s): err = %v, want ErrInvalidAmount", ceiling, err)
	}
	listedPage, err := f.Usage.ListTenantBudgets(f.listCtx(), meter.ListTenantBudgetsRequest{})
	listed := listedPage.Items
	expect.Must(t, err == nil, "%v", err)
	i := slices.IndexFunc(listed, func(s meter.TenantBudget) bool { return s.TenantID == f.Tenant })
	expect.Must(t, !(i < 0), "ListTenantBudgets left out the tenant: %+v", listed)
	diff := sameTenantBudget(listed[i], set)
	expect.That(t, diff == "", "listed budget = %+v, SetTenantBudget returned %+v: %s", listed[i], set, diff)
}

// sameReservation names how got differs from want, or returns "". Every
// field counts, so one the store does not keep is a difference; the expiry
// compares as an instant, whatever its zone.
func sameReservation(got, want meter.Reservation) string {
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
func sameTenantBudget(got, want meter.TenantBudget) string {
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
// checkHoldDatesTenantBudget holds a store to dating the tenant's aggregate
// whenever it writes it: a tenant that has only ever reserved reads back
// when that happened, and a release dates it again.
func checkHoldDatesTenantBudget(t *testing.T, f fixture) {
	if f.Now == nil || f.Advance == nil {
		t.Skip("Env.Now or Env.Advance not set")
	}
	// later is how far the clock moves before the release.
	const later = time.Minute
	r, err := f.Usage.Reserve(f.Ctx, meter.ReserveRequest{Subject: f.childSubject(), Amount: one})
	expect.Must(t, err == nil, "%v", err)
	b, err := f.Usage.GetTenantBudget(f.Ctx, f.Tenant)
	expect.Must(t, err == nil, "%v", err)
	expect.That(t, b.UpdatedAt.Equal(f.Now()), "after the first hold the aggregate is dated %s, want %s", b.UpdatedAt, f.Now())
	f.Advance(later)
	err = f.Usage.Release(f.Ctx, meter.ReleaseRequest{Subject: f.childSubject(), ReservationID: r.ID})
	expect.Must(t, err == nil, "%v", err)
	b, err = f.Usage.GetTenantBudget(f.Ctx, f.Tenant)
	expect.That(t, !(err != nil || !b.UpdatedAt.Equal(f.Now())), "after the release the aggregate is dated %s, %v; want %s", b.UpdatedAt, err, f.Now())
}

// checkHoldWithinCapability holds a store to ending a hold no later than its
// capability: a TTL past the capability's expiry is cut to it, so a hold no
// one can settle any more does not keep the ancestors' and the tenant's
// budget held.
func checkHoldWithinCapability(t *testing.T, f fixture) {
	if f.Now == nil {
		t.Skip("Env.Now not set")
	}
	// farTTL outlives any capability an Env records.
	const farTTL = 100 * 365 * 24 * time.Hour
	r, err := f.Usage.Reserve(f.Ctx, meter.ReserveRequest{Subject: f.childSubject(), Amount: one, TTL: farTTL})
	expect.Must(t, err == nil, "%v", err)
	expect.That(t, r.ExpiresAt.Before(f.Now().Add(farTTL)), "a hold asked for %s expires at %s, past its capability", farTTL, r.ExpiresAt)
	if f.Advance == nil {
		return // the record's expiry needs a clock the check can move
	}
	// Past its record's expiry a capability — still on record, a late cost
	// names it — holds nothing: no one could settle the hold.
	f.Advance(farTTL)
	_, err = f.Usage.Reserve(f.Ctx, meter.ReserveRequest{Subject: f.childSubject(), Amount: one})
	expect.That(t, errors.Is(err, limes.ErrExpired), "Reserve for a capability past its expiry: err = %v, want ErrExpired", err)
}

func checkReservations(t *testing.T, f fixture) {
	const (
		held   = 2 * one
		sooner = time.Minute
		later  = 2 * time.Minute
		// copyLimit holds every reservation below, through the one copy.
		copyLimit = 10
	)
	copies := []limes.CopyCeiling{{
		RevocationID: []byte("copy-1"), Requests: &limes.RequestCeiling{Max: copyLimit}, Budget: &limes.BudgetCeiling{Max: copyLimit * one, UnitCode: unit},
	}}
	reserve := func(capID uuid.UUID, ceiling limes.Nanos, ttl time.Duration) meter.Reservation {
		t.Helper()
		r, err := f.Usage.Reserve(f.Ctx, meter.ReserveRequest{
			Subject: newSubject(subjectSpec{capID: capID, tenant: f.Tenant, budget: budgetOf(ceiling), unit: unit, copies: copies}),
			Amount:  held,
			TTL:     ttl,
			Op:      "llm",
			Actor:   "agent",
		})
		expect.Must(t, err == nil, "reserve: %v", err)
		return r
	}
	first := reserve(f.child, childBudget, sooner)
	second := reserve(f.child, childBudget, later)
	onRoot := reserve(f.root, rootBudget, sooner)
	expect.That(t, !(first.CapabilityID != f.child || first.TenantID != f.Tenant || first.Amount != held ||
		first.Op != "llm" || first.Actor != "agent" || len(first.Copies) != 1 || !sameCeiling(first.Copies[0].Budget, copies[0].Budget)), "Reserve returned %+v; want the whole hold, copies with their budgets alone", first)
	// The copy's budget has a Max; every other field of this hold holds a
	// value, so one the store does not keep reads back unset.
	unset, want := fields.Unset(first), []string{"Copies[0].Budget.Unlimited"}
	expect.That(t, slices.Equal(unset, want), "Reserve returned %+v with %v unset, want only %v", first, unset, want)

	got, err := f.Usage.GetReservation(f.Ctx, first.ID)
	expect.Must(t, err == nil, "GetReservation: %v", err)
	diff := sameReservation(got, first)
	expect.That(t, diff == "", "GetReservation = %+v, Reserve returned %+v: %s", got, first, diff)
	list := func(capID uuid.UUID) []uuid.UUID {
		t.Helper()
		rs, err := f.Usage.ListReservations(f.Ctx, meter.ListReservationsRequest{CapabilityID: capID})
		expect.Must(t, err == nil, "ListReservations: %v", err)
		ids := []uuid.UUID{}
		for _, r := range rs.Items {
			ids = append(ids, r.ID)
		}
		return ids
	}
	holds := list(f.child)
	expect.That(t, slices.Equal(holds, []uuid.UUID{first.ID, second.ID}), "the child's holds = %v, want %v soonest first", holds, []uuid.UUID{first.ID, second.ID})
	holds = list(f.root)
	expect.That(t, slices.Equal(holds, []uuid.UUID{onRoot.ID}), "the root's holds = %v, want only its own %v", holds, onRoot.ID)

	// A hold is settled by the copy that made it: not by the capability
	// without it, nor under another copy, whose own limits it would shed.
	other := []limes.CopyCeiling{{RevocationID: []byte("copy-2")}}
	for name, c := range map[string][]limes.CopyCeiling{"no copy": nil, "another copy": other} {
		subject := newSubject(subjectSpec{capID: f.child, tenant: f.Tenant, budget: budgetOf(childBudget), unit: unit, copies: c})
		_, err := f.Usage.Settle(f.Ctx, meter.SettleRequest{Subject: subject, ReservationID: first.ID, Amount: held}, nil)
		expect.That(t, errors.Is(err, meter.ErrReservationNotFound), "a copy's hold settled under %s: err = %v, want ErrReservationNotFound", name, err)
	}
	byCopy := newSubject(subjectSpec{capID: f.child, tenant: f.Tenant, budget: budgetOf(childBudget), unit: unit, copies: copies})
	_, err = f.Usage.Settle(f.Ctx, meter.SettleRequest{Subject: byCopy, ReservationID: first.ID, Amount: held}, nil)
	expect.Must(t, err == nil, "%v", err)
	// Settled, the hold's receipt reads back to its copy alone.
	r, err := f.Usage.Settle(f.Ctx, meter.SettleRequest{Subject: byCopy, ReservationID: first.ID, Amount: held}, nil)
	expect.That(t, !(err != nil || !r.Replayed), "the copy settling its hold again = %+v, %v; want the replay", r, err)
	another := newSubject(subjectSpec{capID: f.child, tenant: f.Tenant, budget: budgetOf(childBudget), unit: unit, copies: other})
	_, err = f.Usage.Settle(f.Ctx, meter.SettleRequest{Subject: another, ReservationID: first.ID, Amount: held}, nil)
	expect.That(t, errors.Is(err, meter.ErrReservationNotFound), "another copy settling a settled hold: err = %v, want ErrReservationNotFound, not the receipt", err)
	err = f.Usage.Release(f.Ctx, meter.ReleaseRequest{Subject: byCopy, ReservationID: second.ID})
	expect.Must(t, err == nil, "%v", err)
	for _, id := range []uuid.UUID{first.ID, second.ID, uuid.New()} {
		_, err := f.Usage.GetReservation(f.Ctx, id)
		expect.That(t, errors.Is(err, meter.ErrReservationNotFound), "GetReservation %s: err = %v, want ErrReservationNotFound", id, err)
	}
	left := list(f.child)
	expect.That(t, len(left) == 0, "holds left after settling and releasing: %v", left)
}

// checkCopyUsageRefusesInvalid holds a CopyUsageReader to the request's
// bounds: no copy, more than MaxCopyUsageIDs, or an empty id is refused.
func checkCopyUsageRefusesInvalid(t *testing.T, f fixture) {
	reader, ok := f.Usage.(meter.CopyUsageReader)
	if !ok {
		return // a Meter that does not read copies back is checked no further
	}
	tooMany := make([][]byte, meter.MaxCopyUsageIDs+1)
	for i := range tooMany {
		tooMany[i] = []byte(strconv.Itoa(i))
	}
	for name, req := range map[string]meter.CopyUsageRequest{
		"no capability": {RevocationIDs: [][]byte{[]byte("copy-1")}},
		"no id":         {CapabilityID: f.child},
		"too many":      {CapabilityID: f.child, RevocationIDs: tooMany},
		"empty id":      {CapabilityID: f.child, RevocationIDs: [][]byte{[]byte("copy-1"), {}}},
	} {
		_, err := reader.CopyUsage(f.Ctx, req)
		expect.That(t, errors.Is(err, limes.ErrInvalidRequest), "CopyUsage with %s: err = %v, want ErrInvalidRequest", name, err)
	}
}

// checkCopyCountersGo holds a CopyUsageReader to naming the capability a
// copy counts for, and to dropping the copy's counters with the
// capability's usage: by DeleteUsage, and by PurgeOrphans once the
// capability is off record.
func checkCopyCountersGo(t *testing.T, f fixture) {
	reader, ok := f.Usage.(meter.CopyUsageReader)
	if !ok {
		return // a Meter that does not read copies back is checked no further
	}
	// requests is each copy's ceiling: the one request it is counted with.
	const requests = 1
	counted := func(capID uuid.UUID, copyID []byte) []meter.CopyUsage {
		t.Helper()
		copies := []limes.CopyCeiling{{RevocationID: copyID, Requests: &limes.RequestCeiling{Max: requests}}}
		_, err := f.Usage.Bump(f.Ctx, meter.BumpRequest{Subject: newSubject(subjectSpec{capID: capID, tenant: f.Tenant, copies: copies})})
		expect.Must(t, err == nil, "%v", err)
		return f.copyUsage(t, reader, capID, copyID)
	}
	deleted := []byte("copy-deleted")
	got := counted(f.child, deleted)
	expect.Must(t, !(len(got) != 1 || got[0].CapabilityID != f.child), "copy usage = %+v; want one copy counting for %s", got, f.child)
	err := f.Usage.DeleteUsage(f.Ctx, meter.DeleteUsageRequest{CapabilityID: f.child})
	expect.Must(t, err == nil, "%v", err)
	got = f.copyUsage(t, reader, f.child, deleted)
	expect.That(t, len(got) == 0, "copy usage after DeleteUsage of its capability = %+v; want none", got)

	if f.DeleteCapability == nil {
		return // PurgeOrphans needs a capability off record
	}
	orphan, err := f.newCapability(uuid.Nil(), unlimitedBudget)
	expect.Must(t, err == nil, "%v", err)
	purged := []byte("copy-purged")
	counted(orphan, purged)
	err = f.DeleteCapability(orphan)
	expect.Must(t, err == nil, "%v", err)
	_, err = f.Usage.PurgeOrphans(f.Ctx, meter.PurgeOrphansRequest{})
	expect.Must(t, err == nil, "%v", err)
	got = f.copyUsage(t, reader, orphan, purged)
	expect.That(t, len(got) == 0, "copy usage after PurgeOrphans of its capability = %+v; want none", got)
}

// checkCopySharedID holds a store to counting a copy under its capability,
// not under its revocation id alone. A Biscuit block's signature does not
// cover the block before it, so a holder of two Biscuits can graft one's
// block onto the other: two capabilities, here of two tenants, then carry
// the same id. Neither may spend, refund or drop the other's counter.
func checkCopySharedID(t *testing.T, f fixture) {
	reader, ok := f.Usage.(meter.CopyUsageReader)
	if !ok {
		return // a Meter that does not read copies back is checked no further
	}
	tenant, err := f.newTenant()
	expect.Must(t, err == nil, "%v", err)
	theirCap, err := f.NewCapability(CapabilitySpec{Tenant: tenant, Requests: limes.RequestCeiling{Unlimited: true}, Budget: unlimitedBudget})
	expect.Must(t, err == nil, "%v", err)
	theirCtx := f.tenantCtx(tenant)
	// requests is the copy's request ceiling, and its budget is one unit.
	const requests = 1
	shared := []limes.CopyCeiling{{
		RevocationID: []byte("copy-shared"),
		Requests:     &limes.RequestCeiling{Max: requests},
		Budget:       &limes.BudgetCeiling{Max: one, UnitCode: unit},
	}}
	mine := newSubject(subjectSpec{capID: f.child, tenant: f.Tenant, copies: shared})
	theirs := newSubject(subjectSpec{capID: theirCap, tenant: tenant, copies: shared})

	_, err = f.Usage.Bump(f.Ctx, meter.BumpRequest{Subject: mine})
	expect.Must(t, err == nil, "%v", err)
	_, err = f.Usage.Bump(theirCtx, meter.BumpRequest{Subject: theirs})
	expect.That(t, err == nil, "the other capability's first request under the shared id: err = %v, want it counted apart", err)
	theirCharge, err := f.Usage.Charge(theirCtx, meter.ChargeRequest{Subject: theirs, Amount: one}, nil)
	expect.Must(t, err == nil, "the other capability's charge under the shared id: %v", err)
	_, err = f.Usage.Charge(f.Ctx, meter.ChargeRequest{Subject: mine, Amount: one}, nil)
	expect.That(t, err == nil, "a charge within the copy's own budget: err = %v, want it admitted", err)
	_, err = f.Usage.Refund(theirCtx, meter.RefundRequest{ChargeID: theirCharge.ChargeID, Amount: one})
	expect.Must(t, err == nil, "%v", err)
	err = f.Usage.DeleteUsage(theirCtx, meter.DeleteUsageRequest{CapabilityID: theirCap})
	expect.Must(t, err == nil, "%v", err)
	got := f.copyUsage(t, reader, f.child, shared[0].RevocationID)
	expect.That(t, !(len(got) != 1 || got[0].CapabilityID != f.child || got[0].RequestCount != requests || got[0].SpentAmount != one), "the copy after the other capability's refund and DeleteUsage = %+v; want %d request and %v spent, untouched", got, requests, one)
	_, err = f.Usage.Bump(f.Ctx, meter.BumpRequest{Subject: mine})
	expect.That(t, errors.Is(err, limes.ErrRequestLimitExceeded), "a request past the copy's ceiling after the other capability's DeleteUsage: err = %v, want ErrRequestLimitExceeded", err)
}

// checkCopyQuota holds a store to its quota of copies per capability: a call
// that would count one copy past it is refused, by Bump, Charge and Reserve
// alike, and counts nothing; a copy already counted goes on being metered;
// and the quota is the capability's, so dropping its usage frees it.
func checkCopyQuota(t *testing.T, f fixture) {
	quota := f.MaxCopiesPerCapability
	if quota == 0 {
		t.Skip("Env.MaxCopiesPerCapability not set")
	}
	capID, err := f.newCapability(uuid.Nil(), unlimitedBudget)
	expect.Must(t, err == nil, "%v", err)
	copies := func(prefix string, n int) []limes.CopyCeiling {
		out := make([]limes.CopyCeiling, n)
		for i := range out {
			out[i] = limes.CopyCeiling{RevocationID: []byte(prefix + strconv.Itoa(i)), Requests: &limes.RequestCeiling{Unlimited: true}}
		}
		return out
	}
	subject := func(c []limes.CopyCeiling) limes.MeterSubject {
		return newSubject(subjectSpec{capID: capID, tenant: f.Tenant, copies: c})
	}
	full := copies("counted-", quota)
	_, err = f.Usage.Bump(f.Ctx, meter.BumpRequest{Subject: subject(full)})
	expect.Must(t, err == nil, "Bump of %d copies, the quota: %v", quota, err)
	past := slices.Concat(full[:1], copies("past-", 1))
	_, err = f.Usage.Bump(f.Ctx, meter.BumpRequest{Subject: subject(past)})
	expect.That(t, !(!errors.Is(err, meter.ErrCopyLimitExceeded) || !errors.Is(err, limes.ErrRequestLimitExceeded)), "Bump of a copy past the quota: err = %v, want ErrCopyLimitExceeded, matching ErrRequestLimitExceeded", err)
	_, err = f.Usage.Charge(f.Ctx, meter.ChargeRequest{Subject: subject(past), Amount: one}, nil)
	expect.That(t, errors.Is(err, meter.ErrCopyLimitExceeded), "Charge of a copy past the quota: err = %v, want ErrCopyLimitExceeded", err)
	_, err = f.Usage.Reserve(f.Ctx, meter.ReserveRequest{Subject: subject(past), Amount: one})
	expect.That(t, errors.Is(err, meter.ErrCopyLimitExceeded), "Reserve of a copy past the quota: err = %v, want ErrCopyLimitExceeded", err)
	u, err := f.Usage.GetUsage(f.Ctx, capID)
	expect.That(t, !(err != nil || u.RequestCount != 1 || u.SpentAmount != 0 || u.ReservedAmount != 0), "usage after the refused calls = %+v, %v; want only the first request counted", u, err)
	_, err = f.Usage.Bump(f.Ctx, meter.BumpRequest{Subject: subject(full[:1])})
	expect.That(t, err == nil, "Bump of a copy already counted, at the quota: %v", err)
	err = f.Usage.DeleteUsage(f.Ctx, meter.DeleteUsageRequest{CapabilityID: capID})
	expect.Must(t, err == nil, "%v", err)
	_, err = f.Usage.Bump(f.Ctx, meter.BumpRequest{Subject: subject(past)})
	expect.That(t, err == nil, "Bump of a new copy once the capability's usage was dropped: %v", err)
}

// capabilityCopyQuota is the quota a capability carries in
// checkCopyQuotaCaveat, under any store's own bound.
const capabilityCopyQuota = 2

// checkCopyQuotaCaveat holds a store to the capability's own quota of copies
// (Caveats.Copies) under its own bound: a capability allowing two counts two
// and refuses a third, a copy already counted is still metered, and the zero
// quota refuses the first copy while the capability itself, presented with
// no copy, is metered as before.
func checkCopyQuotaCaveat(t *testing.T, f fixture) {
	if f.MaxCopiesPerCapability != 0 && f.MaxCopiesPerCapability <= capabilityCopyQuota {
		t.Skipf("Env.MaxCopiesPerCapability %d leaves no room under it", f.MaxCopiesPerCapability)
	}
	limited := func(id string) limes.CopyCeiling {
		return limes.CopyCeiling{RevocationID: []byte(id), Requests: &limes.RequestCeiling{Unlimited: true}}
	}
	subjectOf := func(t *testing.T, quota limes.CopyQuota, copies ...limes.CopyCeiling) limes.MeterSubject {
		t.Helper()
		capID, err := f.newCapability(uuid.Nil(), unlimitedBudget)
		expect.Must(t, err == nil, "%v", err)
		return newSubject(subjectSpec{capID: capID, tenant: f.Tenant, copies: copies, copyQuota: &quota})
	}
	t.Run("a quota of two", func(t *testing.T) {
		two := subjectOf(t, limes.CopyQuota{Max: capabilityCopyQuota}, limited("first"), limited("second"))
		_, err := f.Usage.Bump(f.Ctx, meter.BumpRequest{Subject: two})
		expect.Must(t, err == nil, "Bump of two copies under a quota of two: %v", err)
		third := newSubject(subjectSpec{capID: two.CapabilityID(), tenant: f.Tenant, copies: []limes.CopyCeiling{limited("third")},
			copyQuota: &limes.CopyQuota{Max: capabilityCopyQuota}})
		_, err = f.Usage.Charge(f.Ctx, meter.ChargeRequest{Subject: third, Amount: one}, nil)
		expect.That(t, errors.Is(err, meter.ErrCopyLimitExceeded), "Charge of a third copy under a quota of two: err = %v, want ErrCopyLimitExceeded", err)
		again := newSubject(subjectSpec{capID: two.CapabilityID(), tenant: f.Tenant, copies: []limes.CopyCeiling{limited("first")},
			copyQuota: &limes.CopyQuota{Max: capabilityCopyQuota}})
		_, err = f.Usage.Bump(f.Ctx, meter.BumpRequest{Subject: again})
		expect.That(t, err == nil, "Bump of a copy already counted, at the quota: %v", err)
	})
	t.Run("the zero quota", func(t *testing.T) {
		none := subjectOf(t, limes.CopyQuota{}, limited("limited"))
		_, err := f.Usage.Reserve(f.Ctx, meter.ReserveRequest{Subject: none, Amount: one})
		expect.That(t, errors.Is(err, meter.ErrCopyLimitExceeded), "Reserve of a copy with limits under the zero quota: err = %v, want ErrCopyLimitExceeded", err)
		_, err = f.Usage.Bump(f.Ctx, meter.BumpRequest{Subject: newSubject(subjectSpec{capID: none.CapabilityID(), tenant: f.Tenant, copyQuota: &limes.CopyQuota{}})})
		expect.That(t, err == nil, "Bump of the capability itself under the zero quota: %v", err)
	})
}

// lineageCopyQuota is the quota of the root in checkCopyQuotaLineage, under
// any store's own bound.
const lineageCopyQuota = 2

// checkCopyQuotaLineage holds a store to counting a capability's copies under
// every capability above it, as it counts requests and spend: a root allowing
// two counts two across all it delegated, each child under a quota of its own
// of two, so a second child's first copy is refused; so is the root's own;
// and dropping one child's usage leaves its copies counted under the root,
// as its requests are; dropping the root's own restarts its count, as it
// restarts its other counters.
func checkCopyQuotaLineage(t *testing.T, f fixture) {
	if f.MaxCopiesPerCapability != 0 && f.MaxCopiesPerCapability <= lineageCopyQuota {
		t.Skipf("Env.MaxCopiesPerCapability %d leaves no room under it", f.MaxCopiesPerCapability)
	}
	quota := limes.CopyQuota{Max: lineageCopyQuota}
	root, err := f.NewCapability(CapabilitySpec{Requests: limes.RequestCeiling{Unlimited: true}, Budget: unlimitedBudget, Copies: &quota})
	expect.Must(t, err == nil, "%v", err)
	child := func() uuid.UUID {
		t.Helper()
		id, err := f.NewCapability(CapabilitySpec{Parent: root, Requests: limes.RequestCeiling{Unlimited: true}, Budget: unlimitedBudget, Copies: &quota})
		expect.Must(t, err == nil, "%v", err)
		return id
	}
	a, b := child(), child()
	copies := func(capID uuid.UUID, ids ...string) limes.MeterSubject {
		out := make([]limes.CopyCeiling, len(ids))
		for i, id := range ids {
			out[i] = limes.CopyCeiling{RevocationID: []byte(id), Requests: &limes.RequestCeiling{Unlimited: true}}
		}
		return newSubject(subjectSpec{capID: capID, tenant: f.Tenant, copies: out, copyQuota: &quota})
	}
	_, err = f.Usage.Bump(f.Ctx, meter.BumpRequest{Subject: copies(a, "a1", "a2")})
	expect.Must(t, err == nil, "Bump of two copies of a child, the root's quota: %v", err)
	_, err = f.Usage.Bump(f.Ctx, meter.BumpRequest{Subject: copies(b, "b1")})
	expect.That(t, errors.Is(err, meter.ErrCopyLimitExceeded), "Bump of a copy of a second child past the root's quota: err = %v, want ErrCopyLimitExceeded", err)
	_, err = f.Usage.Bump(f.Ctx, meter.BumpRequest{Subject: copies(root, "r1")})
	expect.That(t, errors.Is(err, meter.ErrCopyLimitExceeded), "Bump of a copy of the root, its children holding its quota: err = %v, want ErrCopyLimitExceeded", err)
	err = f.Usage.DeleteUsage(f.Ctx, meter.DeleteUsageRequest{CapabilityID: a})
	expect.Must(t, err == nil, "%v", err)
	_, err = f.Usage.Bump(f.Ctx, meter.BumpRequest{Subject: copies(b, "b1")})
	expect.That(t, errors.Is(err, meter.ErrCopyLimitExceeded), "Bump of a copy of the second child once the first's usage was dropped: err = %v, want ErrCopyLimitExceeded", err)
	err = f.Usage.DeleteUsage(f.Ctx, meter.DeleteUsageRequest{CapabilityID: root})
	expect.Must(t, err == nil, "%v", err)
	_, err = f.Usage.Bump(f.Ctx, meter.BumpRequest{Subject: copies(b, "b1")})
	expect.That(t, err == nil, "Bump of a copy of the second child once the root's own usage was dropped: %v", err)
}

// copyUsage is what reader holds for the copy copyID of capability capID.
func (f fixture) copyUsage(t *testing.T, reader meter.CopyUsageReader, capID uuid.UUID, copyID []byte) []meter.CopyUsage {
	t.Helper()
	res, err := reader.CopyUsage(f.Ctx, meter.CopyUsageRequest{CapabilityID: capID, RevocationIDs: [][]byte{copyID}})
	expect.Must(t, err == nil, "%v", err)
	return res.Copies
}

func checkCopyOverrun(t *testing.T, f fixture) {
	copyID := []byte("copy-1")
	copies := []limes.CopyCeiling{{RevocationID: copyID, Budget: &limes.BudgetCeiling{Max: one, UnitCode: unit}}}
	_, err := f.charge(meter.ChargeRequest{Subject: newSubject(subjectSpec{copies: copies}), Amount: 2 * one}, nil)
	expect.Must(t, errors.Is(err, limes.ErrBudgetExceeded), "rejecting charge past the copy's budget: err = %v, want ErrBudgetExceeded", err)
	// The copy is checked first, but its refusal must not leave the
	// capability's chain charged.
	expect.Must(t, !(f.spent(t, f.child) != 0 || f.spent(t, f.root) != 0 || f.tenantSpent(t) != 0), "a charge refused at the copy's budget moved the chain: child %v, root %v, tenant %v", f.spent(t, f.child), f.spent(t, f.root), f.tenantSpent(t))
	r, err := f.charge(meter.ChargeRequest{Subject: newSubject(subjectSpec{copies: copies}), Amount: 2 * one, Overrun: meter.OverrunPolicyRecord}, nil)
	expect.Must(t, !(err != nil || !r.Overrun), "recording charge = %+v, %v; want an overrun", r, err)
	reader, ok := f.Usage.(meter.CopyUsageReader)
	if !ok {
		return // a Meter that does not read copies back is checked no further
	}
	res, err := reader.CopyUsage(f.Ctx, meter.CopyUsageRequest{CapabilityID: f.child, RevocationIDs: [][]byte{copyID}})
	got := res.Copies
	expect.That(t, !(err != nil || len(got) != 1 || got[0].SpentAmount != 2*one), "copy usage = %+v, %v; want 2 spent", got, err)
}

// checkSumsExactly holds a store to exact amounts: a tenth and two tenths fit
// under a ceiling of three tenths with not a nano to spare, and seven tenths
// add up to seven tenths — the sums a float64 gets wrong.
func checkSumsExactly(t *testing.T, f fixture) {
	tenth, threeTenths := one/10, 3*one/10
	id, err := f.newCapability(uuid.Nil(), limes.BudgetCeiling{Max: threeTenths})
	expect.Must(t, err == nil, "%v", err)
	for _, amount := range []limes.Nanos{tenth, 2 * tenth} {
		_, err := f.charge(meter.ChargeRequest{Subject: newSubject(subjectSpec{capID: id, budget: budgetOf(threeTenths)}), Amount: amount}, nil)
		expect.Must(t, err == nil, "charging %s under a ceiling of %s: %v", amount, threeTenths, err)
	}
	got := f.spent(t, id)
	expect.That(t, got == threeTenths, "spent %s, want exactly %s", got, threeTenths)
	_, err = f.charge(meter.ChargeRequest{Subject: newSubject(subjectSpec{capID: id, budget: budgetOf(threeTenths)}), Amount: 1}, nil)
	expect.That(t, errors.Is(err, limes.ErrBudgetExceeded), "a nano past the ceiling: err = %v, want ErrBudgetExceeded", err)
	for range 7 {
		_, err := f.charge(meter.ChargeRequest{Subject: newSubject(subjectSpec{capID: f.root, budget: budgetOf(rootBudget)}), Amount: tenth}, nil)
		expect.Must(t, err == nil, "%v", err)
	}
	got = f.spent(t, f.root)
	expect.That(t, got == 7*tenth, "seven tenths spent as %s", got)
}

// checkConcurrentCeiling holds a store to checking and moving a ceiling as
// one step: charges racing to the last of a budget must not all see room for
// themselves. A read-then-write without a lock is the bug this catches.
func checkConcurrentCeiling(t *testing.T, f fixture) {
	var (
		wg       sync.WaitGroup
		accepted atomic.Int64
	)
	for range concurrentDeliveries {
		wg.Go(func() {
			_, err := f.charge(meter.ChargeRequest{Amount: 2 * one}, nil)
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
	got, want := accepted.Load(), int64(childBudget/(2*one))
	expect.That(t, got == want, "%d charges of 2 accepted under a ceiling of 20, want %d", got, want)
	spent := f.spent(t, f.child)
	expect.That(t, spent == childBudget, "child spent %v, want exactly its ceiling %v", spent, childBudget)
}

// checkAncestorCeiling holds a store to reading each ancestor's ceiling from
// its record: two children each within their own budget must not sum past
// their parent's.
func checkAncestorCeiling(t *testing.T, f fixture) {
	sibling, err := f.newCapability(f.root, limes.BudgetCeiling{Max: childBudget})
	expect.Must(t, err == nil, "%v", err)
	_, err = f.charge(meter.ChargeRequest{Amount: 15 * one}, nil)
	expect.Must(t, err == nil, "%v", err)
	// 15 + 15 is within each child's 20, past the root's 25.
	_, err = f.charge(meter.ChargeRequest{Subject: newSubject(subjectSpec{capID: sibling}), Amount: 15 * one}, nil)
	expect.Must(t, errors.Is(err, limes.ErrBudgetExceeded), "a sibling's charge past the root's ceiling: err = %v, want ErrBudgetExceeded", err)
	expect.That(t, !(f.spent(t, sibling) != 0 || f.spent(t, f.root) != 15*one), "a charge refused by an ancestor's ceiling moved spend")
}

// checkAncestorRequests holds Bump to an ancestor's request ceiling: a child
// with none of its own still stops at its root's.
func checkAncestorRequests(t *testing.T, f fixture) {
	const limit = 1
	root, err := f.NewCapability(CapabilitySpec{Requests: limes.RequestCeiling{Max: limit}, Budget: unlimitedBudget})
	expect.Must(t, err == nil, "%v", err)
	child, err := f.newCapability(root, unlimitedBudget)
	expect.Must(t, err == nil, "%v", err)
	bump := func() error {
		_, err := f.Usage.Bump(f.Ctx, meter.BumpRequest{Subject: newSubject(subjectSpec{capID: child, tenant: f.Tenant})})
		return err
	}
	for range limit {
		err := bump()
		expect.Must(t, err == nil, "a bump within the root's ceiling: %v", err)
	}
	err = bump()
	expect.That(t, errors.Is(err, limes.ErrRequestLimitExceeded), "a bump past the root's ceiling: err = %v, want ErrRequestLimitExceeded", err)
}

// checkCopyRequests holds Bump to a copy's request ceiling, which binds the
// copy while the capability's own allows more.
func checkCopyRequests(t *testing.T, f fixture) {
	const limit = 1
	copies := []limes.CopyCeiling{{RevocationID: []byte("copy-requests"), Requests: &limes.RequestCeiling{Max: limit}}}
	bump := func() error {
		_, err := f.Usage.Bump(f.Ctx, meter.BumpRequest{Subject: newSubject(subjectSpec{capID: f.child, tenant: f.Tenant, copies: copies})})
		return err
	}
	for range limit {
		err := bump()
		expect.Must(t, err == nil, "a bump within the copy's ceiling: %v", err)
	}
	err := bump()
	expect.That(t, errors.Is(err, limes.ErrRequestLimitExceeded), "a bump past the copy's ceiling: err = %v, want ErrRequestLimitExceeded", err)
}

// checkCopyHold holds a store to counting a hold against the copy it was
// made under, as against every other ceiling.
func checkCopyHold(t *testing.T, f fixture) {
	copies := []limes.CopyCeiling{{RevocationID: []byte("copy-hold"), Budget: &limes.BudgetCeiling{Max: one, UnitCode: unit}}}
	subject := newSubject(subjectSpec{capID: f.child, tenant: f.Tenant, budget: budgetOf(childBudget), copies: copies})
	_, err := f.Usage.Reserve(f.Ctx, meter.ReserveRequest{Subject: subject, Amount: one})
	expect.Must(t, err == nil, "a hold of the copy's whole budget: %v", err)
	_, err = f.charge(meter.ChargeRequest{Subject: subject, Amount: cent}, nil)
	expect.That(t, errors.Is(err, limes.ErrBudgetExceeded), "a charge past the copy's budget with the hold: err = %v, want ErrBudgetExceeded", err)
}

// checkStaleVersion holds SetTenantBudget to its optimistic-concurrency
// guard: a write carrying a version another write has moved past is
// refused, and leaves the ceiling as it was.
func checkStaleVersion(t *testing.T, f fixture) {
	b, err := f.Usage.GetTenantBudget(f.Ctx, f.Tenant)
	expect.Must(t, err == nil, "%v", err)
	set := func(max limes.Nanos) error {
		_, err := f.Usage.SetTenantBudget(f.Ctx, meter.SetTenantBudgetRequest{
			TenantID: f.Tenant, Ceiling: limes.BudgetCeiling{Max: max, UnitCode: unit}, ExpectedVersion: b.ResourceVersion,
		})
		return err
	}
	err = set(tenantBudget + one)
	expect.Must(t, err == nil, "a write at the current version: %v", err)
	err = set(tenantBudget + 2*one)
	expect.That(t, errors.Is(err, meter.ErrTenantBudgetVersionMismatch), "a write at a stale version: err = %v, want ErrTenantBudgetVersionMismatch", err)
	got, err := f.Usage.GetTenantBudget(f.Ctx, f.Tenant)
	expect.That(t, !(err != nil || got.Ceiling != (limes.BudgetCeiling{Max: tenantBudget + one, UnitCode: unit})), "after a stale write the ceiling = %v, %v; want %s", got.Ceiling, err, tenantBudget+one)
}

// checkZeroCeilings holds a store to a ceiling of 0 allowing nothing — on
// the subject's requests and budget, on a hold, on an ancestor and on the
// tenant — rather than standing for no limit, which only Unlimited does.
func checkZeroCeilings(t *testing.T, f fixture) {
	zero := newSubject(subjectSpec{capID: f.child, tenant: f.Tenant, requests: requestsOf(0), budget: budgetOf(0)})
	_, err := f.Usage.Bump(f.Ctx, meter.BumpRequest{Subject: zero})
	expect.That(t, errors.Is(err, limes.ErrRequestLimitExceeded), "a bump under a request ceiling of 0: err = %v, want ErrRequestLimitExceeded", err)
	_, err = f.charge(meter.ChargeRequest{Subject: zero, Amount: cent}, nil)
	expect.That(t, errors.Is(err, limes.ErrBudgetExceeded), "a charge under a budget of 0: err = %v, want ErrBudgetExceeded", err)
	_, err = f.Usage.Reserve(f.Ctx, meter.ReserveRequest{Subject: zero, Amount: cent})
	expect.That(t, errors.Is(err, limes.ErrBudgetExceeded), "a hold under a budget of 0: err = %v, want ErrBudgetExceeded", err)

	frozen, err := f.newCapability(uuid.Nil(), limes.BudgetCeiling{})
	expect.Must(t, err == nil, "%v", err)
	under, err := f.newCapability(frozen, unlimitedBudget)
	expect.Must(t, err == nil, "%v", err)
	_, err = f.charge(meter.ChargeRequest{Subject: newSubject(subjectSpec{capID: under}), Amount: cent}, nil)
	expect.That(t, errors.Is(err, limes.ErrBudgetExceeded), "a charge under an ancestor's budget of 0: err = %v, want ErrBudgetExceeded", err)
	expect.That(t, !(f.spent(t, f.child) != 0 || f.spent(t, under) != 0 || f.tenantSpent(t) != 0), "a charge refused by a ceiling of 0 moved spend")

	b, err := f.Usage.GetTenantBudget(f.Ctx, f.Tenant)
	expect.Must(t, err == nil, "%v", err)
	_, err = f.Usage.SetTenantBudget(f.Ctx, meter.SetTenantBudgetRequest{TenantID: f.Tenant, Ceiling: limes.BudgetCeiling{UnitCode: unit}, ExpectedVersion: b.ResourceVersion})
	expect.Must(t, err == nil, "tenant ceiling of 0: %v", err)
	_, err = f.charge(meter.ChargeRequest{Subject: newSubject(subjectSpec{capID: f.root}), Amount: cent}, nil)
	expect.That(t, errors.Is(err, limes.ErrTenantBudgetExceeded), "a charge under a tenant ceiling of 0: err = %v, want ErrTenantBudgetExceeded", err)
}

// checkRelease holds Release to returning a hold to every counter it
// counted against: the capability's, each ancestor's and the tenant's.
func checkRelease(t *testing.T, f fixture) {
	r, err := f.Usage.Reserve(f.Ctx, meter.ReserveRequest{Subject: f.childSubject(), Amount: 15 * one})
	expect.Must(t, err == nil, "%v", err)
	err = f.Usage.Release(f.Ctx, meter.ReleaseRequest{Subject: f.childSubject(), ReservationID: r.ID})
	expect.Must(t, err == nil, "%v", err)
	for _, id := range []uuid.UUID{f.child, f.root} {
		u, err := f.Usage.GetUsage(f.Ctx, id)
		expect.That(t, !(err == nil && u.ReservedAmount != 0), "%s still holds %s after Release", id, u.ReservedAmount)
	}
	b, err := f.Usage.GetTenantBudget(f.Ctx, f.Tenant)
	expect.That(t, !(err != nil || b.ReservedAmount != 0), "the tenant still holds %s after Release, %v", b.ReservedAmount, err)
	_, err = f.charge(meter.ChargeRequest{Amount: 10 * one}, nil)
	expect.That(t, err == nil, "a charge the released hold made room for: %v", err)
}

// checkReleaseIsTheOwners holds Release to the copy that reserved: under
// another capability, another tenant, another copy or none, the hold stays
// in place, as Settle refuses it, so a copy cannot free a sibling's hold to
// spend past its own limits. A request naming no subject is refused.
func checkReleaseIsTheOwners(t *testing.T, f fixture) {
	const held = 3 * one
	copies := []limes.CopyCeiling{{RevocationID: []byte("holder"), Budget: &limes.BudgetCeiling{Max: childBudget, UnitCode: unit}}}
	owner := newSubject(subjectSpec{capID: f.child, tenant: f.Tenant, budget: budgetOf(childBudget), unit: unit, copies: copies})
	r, err := f.Usage.Reserve(f.Ctx, meter.ReserveRequest{Subject: owner, Amount: held})
	expect.Must(t, err == nil, "%v", err)
	stillHeld := func(by string) {
		t.Helper()
		_, err := f.Usage.GetReservation(f.Ctx, r.ID)
		expect.That(t, err == nil, "after a release by %s: GetReservation = %v, want the hold", by, err)
		u, err := f.Usage.GetUsage(f.Ctx, f.child)
		expect.That(t, !(err != nil || u.ReservedAmount != held), "after a release by %s: the child holds %+v, %v; want %s", by, u, err, held)
	}
	sibling := []limes.CopyCeiling{{RevocationID: []byte("sibling"), Budget: &limes.BudgetCeiling{Max: childBudget, UnitCode: unit}}}
	for name, subject := range map[string]limes.MeterSubject{
		"another capability": newSubject(subjectSpec{capID: f.root, tenant: f.Tenant, budget: budgetOf(rootBudget), unit: unit}),
		"another tenant":     newSubject(subjectSpec{capID: f.child, tenant: uuid.New(), budget: budgetOf(childBudget), unit: unit, copies: copies}),
		"another copy":       newSubject(subjectSpec{capID: f.child, tenant: f.Tenant, budget: budgetOf(childBudget), unit: unit, copies: sibling}),
		"no copy":            f.childSubject(),
	} {
		err := f.Usage.Release(f.Ctx, meter.ReleaseRequest{Subject: subject, ReservationID: r.ID})
		expect.That(t, !(err != nil && !errors.Is(err, limes.ErrScopeMismatch)), "a release by %s: %v", name, err)
		stillHeld(name)
	}
	err = f.Usage.Release(f.Ctx, meter.ReleaseRequest{ReservationID: r.ID})
	expect.That(t, errors.Is(err, limes.ErrInvalidRequest), "a release naming no subject: err = %v, want ErrInvalidRequest", err)
	stillHeld("no subject")
	err = f.Usage.Release(f.Ctx, meter.ReleaseRequest{Subject: owner, ReservationID: r.ID})
	expect.Must(t, err == nil, "the owner's release: %v", err)
	_, err = f.Usage.GetReservation(f.Ctx, r.ID)
	expect.That(t, errors.Is(err, meter.ErrReservationNotFound), "after the owner's release: GetReservation = %v, want ErrReservationNotFound", err)
	u, err := f.Usage.GetUsage(f.Ctx, f.child)
	expect.That(t, !(err != nil || u.ReservedAmount != 0), "after the owner's release: the child holds %+v, %v; want nothing", u, err)
}

// checkResetKeepsHolds holds a store to ResetSpend resetting the spend only:
// a hold still open counts against the tenant's new period.
func checkResetKeepsHolds(t *testing.T, f fixture) {
	const held = 10 * one
	_, err := f.Usage.Reserve(f.Ctx, meter.ReserveRequest{Subject: f.childSubject(), Amount: held})
	expect.Must(t, err == nil, "%v", err)
	b, err := f.Usage.GetTenantBudget(f.Ctx, f.Tenant)
	expect.Must(t, err == nil, "%v", err)
	_, err = f.Usage.SetTenantBudget(f.Ctx, meter.SetTenantBudgetRequest{
		TenantID: f.Tenant, Ceiling: limes.BudgetCeiling{Max: tenantBudget, UnitCode: unit}, ResetSpend: true, ExpectedVersion: b.ResourceVersion,
	})
	expect.Must(t, err == nil, "%v", err)
	b, err = f.Usage.GetTenantBudget(f.Ctx, f.Tenant)
	expect.That(t, !(err != nil || b.ReservedAmount != held), "after a reset the tenant holds %s, %v; want %s", b.ReservedAmount, err, held)
	other, err := f.newCapability(uuid.Nil(), unlimitedBudget)
	expect.Must(t, err == nil, "%v", err)
	// 10 held + 25 is past the tenant's 30.
	_, err = f.charge(meter.ChargeRequest{Subject: newSubject(subjectSpec{capID: other}), Amount: 25 * one}, nil)
	expect.That(t, errors.Is(err, limes.ErrTenantBudgetExceeded), "a charge past the tenant's ceiling with the hold: err = %v, want ErrTenantBudgetExceeded", err)
}

// checkDescendantHolds holds an ancestor's ceiling to counting what its
// descendants hold, not only what they spent.
func checkDescendantHolds(t *testing.T, f fixture) {
	sibling, err := f.newCapability(f.root, limes.BudgetCeiling{Max: childBudget})
	expect.Must(t, err == nil, "%v", err)
	_, err = f.Usage.Reserve(f.Ctx, meter.ReserveRequest{Subject: f.childSubject(), Amount: 15 * one})
	expect.Must(t, err == nil, "%v", err)
	// 15 held by the child + 15 is past the root's 25.
	_, err = f.charge(meter.ChargeRequest{Subject: newSubject(subjectSpec{capID: sibling, budget: budgetOf(childBudget)}), Amount: 15 * one}, nil)
	expect.That(t, errors.Is(err, limes.ErrBudgetExceeded), "a sibling's charge past the root's ceiling with the child's hold: err = %v, want ErrBudgetExceeded", err)
}

// checkSettleAboveHold holds Settle to counting the hold once: settling 20
// of a 19 hold adds 1 to every counter, so it fits a ceiling of 20 — the
// capability's, a copy's — with nothing to spare.
func checkSettleAboveHold(t *testing.T, f fixture) {
	copies := []limes.CopyCeiling{{RevocationID: []byte("settle-copy"), Budget: &limes.BudgetCeiling{Max: childBudget, UnitCode: unit}}}
	subject := newSubject(subjectSpec{capID: f.child, tenant: f.Tenant, budget: budgetOf(childBudget), copies: copies})
	r, err := f.Usage.Reserve(f.Ctx, meter.ReserveRequest{Subject: subject, Amount: childBudget - one})
	expect.Must(t, err == nil, "%v", err)
	got, err := f.Usage.Settle(f.Ctx, meter.SettleRequest{Subject: subject, ReservationID: r.ID, Amount: childBudget}, nil)
	expect.Must(t, !(err != nil || got.Overrun), "settling the whole ceiling over a hold just under it = %+v, %v; want accepted, no overrun", got, err)
	spent := f.spent(t, f.child)
	expect.That(t, spent == childBudget, "the child spent %s, want %s", spent, childBudget)
	for _, id := range []uuid.UUID{f.child, f.root} {
		u, err := f.Usage.GetUsage(f.Ctx, id)
		expect.That(t, !(err != nil || u.ReservedAmount != 0), "%s holds %v after the settle, %v; want nothing", id, u.ReservedAmount, err)
	}
}

// checkNestedCopySettle holds Settle to every copy of a hold, not only the
// outermost: copies run innermost first, so a child copy C of P presents
// [C, P], and settling P's hold under [C, P] — or C's under [P] — would move
// spend past a copy's own limits.
func checkNestedCopySettle(t *testing.T, f fixture) {
	p := limes.CopyCeiling{RevocationID: []byte("copy-p"), Budget: &limes.BudgetCeiling{Max: childBudget, UnitCode: unit}}
	c := limes.CopyCeiling{RevocationID: []byte("copy-c"), Budget: &limes.BudgetCeiling{Max: one, UnitCode: unit}}
	under := func(copies ...limes.CopyCeiling) limes.MeterSubject {
		return newSubject(subjectSpec{capID: f.child, tenant: f.Tenant, budget: budgetOf(childBudget), copies: copies})
	}
	for name, tc := range map[string]struct{ held, settling limes.MeterSubject }{
		"the parent's hold by the child": {under(p), under(c, p)},
		"the child's hold by the parent": {under(c, p), under(p)},
	} {
		r, err := f.Usage.Reserve(f.Ctx, meter.ReserveRequest{Subject: tc.held, Amount: one})
		expect.Must(t, err == nil, "%v", err)
		_, err = f.Usage.Settle(f.Ctx, meter.SettleRequest{Subject: tc.settling, ReservationID: r.ID, Amount: one}, nil)
		expect.That(t, errors.Is(err, meter.ErrReservationNotFound), "%s: err = %v, want ErrReservationNotFound", name, err)
	}
}

// checkUnitWithHolds holds a store to keeping a tenant's unit while a hold in
// it is open: the hold could never settle in another.
func checkUnitWithHolds(t *testing.T, f fixture) {
	_, err := f.Usage.Reserve(f.Ctx, meter.ReserveRequest{Subject: f.childSubject(), Amount: one})
	expect.Must(t, err == nil, "%v", err)
	b, err := f.Usage.GetTenantBudget(f.Ctx, f.Tenant)
	expect.Must(t, err == nil, "%v", err)
	_, err = f.Usage.SetTenantBudget(f.Ctx, meter.SetTenantBudgetRequest{
		TenantID: f.Tenant, Ceiling: limes.BudgetCeiling{Max: tenantBudget, UnitCode: otherUnit}, ResetSpend: true, ExpectedVersion: b.ResourceVersion,
	})
	expect.That(t, errors.Is(err, limes.ErrUnitCodeMismatch), "a unit change with a hold open: err = %v, want ErrUnitCodeMismatch", err)
}

// checkChainUnits holds a store to every counter on the chain keeping its
// unit, not only the tenant's: once the tenant moves to another unit, a
// capability that counted in the first — or whose ancestor did — refuses an
// amount in the second.
func checkChainUnits(t *testing.T, f fixture) {
	parent, err := f.newCapability(uuid.Nil(), unlimitedBudget)
	expect.Must(t, err == nil, "%v", err)
	child, err := f.newCapability(parent, unlimitedBudget)
	expect.Must(t, err == nil, "%v", err)
	_, err = f.charge(meter.ChargeRequest{Subject: newSubject(subjectSpec{capID: parent}), Amount: one}, nil)
	expect.Must(t, err == nil, "%v", err)
	b, err := f.Usage.GetTenantBudget(f.Ctx, f.Tenant)
	expect.Must(t, err == nil, "%v", err)
	_, err = f.Usage.SetTenantBudget(f.Ctx, meter.SetTenantBudgetRequest{
		TenantID: f.Tenant, Ceiling: limes.BudgetCeiling{Max: tenantBudget, UnitCode: otherUnit}, ResetSpend: true, ExpectedVersion: b.ResourceVersion,
	})
	expect.Must(t, err == nil, "%v", err)
	for name, id := range map[string]uuid.UUID{"its own counter": parent, "its ancestor's counter": child} {
		_, err := f.Usage.Charge(f.Ctx, meter.ChargeRequest{Subject: newSubject(subjectSpec{capID: id, tenant: f.Tenant, unit: otherUnit}), Amount: one}, nil)
		expect.That(t, errors.Is(err, limes.ErrUnitCodeMismatch), "an amount in %s against %s in %s: err = %v, want ErrUnitCodeMismatch", otherUnit, name, unit, err)
	}
}

// checkUnclampedRanking holds ListTenantBudgets to ranking by spend over the
// ceiling unclamped: of two tenants past their ceilings, the further leads,
// whatever their ids.
func checkUnclampedRanking(t *testing.T, f fixture) {
	const ceiling = 10 * one
	type overspender struct{ tenant, capability uuid.UUID }
	newOverspender := func() overspender {
		t.Helper()
		tenant, err := f.newTenant()
		expect.Must(t, err == nil, "%v", err)
		_, err = f.Usage.SetTenantBudget(f.tenantCtx(tenant), meter.SetTenantBudgetRequest{TenantID: tenant, Ceiling: limes.BudgetCeiling{Max: ceiling, UnitCode: unit}})
		expect.Must(t, err == nil, "%v", err)
		id, err := f.NewCapability(CapabilitySpec{Tenant: tenant, Requests: limes.RequestCeiling{Unlimited: true}, Budget: unlimitedBudget})
		expect.Must(t, err == nil, "%v", err)
		return overspender{tenant, id}
	}
	nearer, further := newOverspender(), newOverspender()
	// The further gets the higher id: clamped, the two would tie and the
	// id would put it second.
	if bytes.Compare(further.tenant[:], nearer.tenant[:]) < 0 {
		nearer, further = further, nearer
	}
	for o, spent := range map[overspender]limes.Nanos{nearer: 2 * ceiling, further: 3 * ceiling} {
		_, err := f.Usage.Charge(f.tenantCtx(o.tenant), meter.ChargeRequest{
			Subject: newSubject(subjectSpec{capID: o.capability, tenant: o.tenant}), Amount: spent, Overrun: meter.OverrunPolicyRecord,
		}, nil)
		expect.Must(t, err == nil, "%v", err)
	}
	page, err := f.Usage.ListTenantBudgets(f.listCtx(), meter.ListTenantBudgetsRequest{Filter: meter.TenantBudgetFilterAtOrPast, ThresholdBasisPoints: meter.BasisPointsPerWhole})
	expect.Must(t, err == nil, "%v", err)
	at := func(tenant uuid.UUID) int {
		return slices.IndexFunc(page.Items, func(r meter.TenantBudget) bool { return r.TenantID == tenant })
	}
	fi, ni := at(further.tenant), at(nearer.tenant)
	expect.That(t, !(fi < 0 || ni < 0 || fi > ni), "listed the tenant at 300%% at %d and the one at 200%% at %d; want the further first", fi, ni)
}

// checkThresholdBoundary holds ThresholdBasisPoints to spent / max ≥
// threshold exactly: 29 of 100 is at 29%, which a float quotient reads as
// just under, and one nano under it is not.
func checkThresholdBoundary(t *testing.T, f fixture) {
	// 29 of 100 at 29%, 11 of 1000 at 1.1%, and a nano short of 29% under
	// it.
	for _, tc := range []struct {
		ceiling, spent limes.Nanos
		threshold      int
		listed         bool
	}{
		{100 * one, 29 * one, 2900, true},
		{1000 * one, 11 * one, 110, true},
		{100 * one, 29*one - 1, 2900, false},
	} {
		tenant, err := f.newTenant()
		expect.Must(t, err == nil, "%v", err)
		_, err = f.Usage.SetTenantBudget(f.tenantCtx(tenant), meter.SetTenantBudgetRequest{TenantID: tenant, Ceiling: limes.BudgetCeiling{Max: tc.ceiling, UnitCode: unit}})
		expect.Must(t, err == nil, "%v", err)
		id, err := f.NewCapability(CapabilitySpec{Tenant: tenant, Requests: limes.RequestCeiling{Unlimited: true}, Budget: unlimitedBudget})
		expect.Must(t, err == nil, "%v", err)
		_, err = f.Usage.Charge(f.tenantCtx(tenant), meter.ChargeRequest{Subject: newSubject(subjectSpec{capID: id, tenant: tenant}), Amount: tc.spent}, nil)
		expect.Must(t, err == nil, "%v", err)
		page, err := f.Usage.ListTenantBudgets(f.listCtx(), meter.ListTenantBudgetsRequest{Filter: meter.TenantBudgetFilterAtOrPast, ThresholdBasisPoints: tc.threshold})
		expect.Must(t, err == nil, "%v", err)
		got := slices.ContainsFunc(page.Items, func(r meter.TenantBudget) bool { return r.TenantID == tenant })
		expect.That(t, got == tc.listed, "%s spent of %s at a threshold of %d basis points: listed %v, want %v", tc.spent, tc.ceiling, tc.threshold, got, tc.listed)
	}
}

// checkExactRanking holds ListTenantBudgets to ranking by spent / max
// exactly: a third and a nano past a third are one float64, and the second
// must still lead, whatever the tenant ids.
func checkExactRanking(t *testing.T, f fixture) {
	const third = limes.MaxNanos / 3
	type spec struct {
		tenant         uuid.UUID
		ceiling, spent limes.Nanos
	}
	tenants := make([]uuid.UUID, 2)
	for i := range tenants {
		id, err := f.newTenant()
		expect.Must(t, err == nil, "%v", err)
		tenants[i] = id
	}
	// The further gets the higher id: ranked by a float, the two tie and the
	// id puts it second.
	slices.SortFunc(tenants, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
	nearer, further := spec{tenants[0], 3, 1}, spec{tenants[1], 3 * third, third + 1}
	for _, s := range []spec{nearer, further} {
		_, err := f.Usage.SetTenantBudget(f.tenantCtx(s.tenant), meter.SetTenantBudgetRequest{TenantID: s.tenant, Ceiling: limes.BudgetCeiling{Max: s.ceiling, UnitCode: unit}})
		expect.Must(t, err == nil, "%v", err)
		id, err := f.NewCapability(CapabilitySpec{Tenant: s.tenant, Requests: limes.RequestCeiling{Unlimited: true}, Budget: unlimitedBudget})
		expect.Must(t, err == nil, "%v", err)
		_, err = f.Usage.Charge(f.tenantCtx(s.tenant), meter.ChargeRequest{Subject: newSubject(subjectSpec{capID: id, tenant: s.tenant}), Amount: s.spent}, nil)
		expect.Must(t, err == nil, "%v", err)
	}
	page, err := f.Usage.ListTenantBudgets(f.listCtx(), meter.ListTenantBudgetsRequest{Filter: meter.TenantBudgetFilterAtOrPast})
	expect.Must(t, err == nil, "%v", err)
	at := func(tenant uuid.UUID) int {
		return slices.IndexFunc(page.Items, func(r meter.TenantBudget) bool { return r.TenantID == tenant })
	}
	fi, ni := at(further.tenant), at(nearer.tenant)
	expect.That(t, !(fi < 0 || ni < 0 || fi > ni), "listed a nano past a third at %d and a third at %d; want the further first", fi, ni)
}

// checkHousekeepingBatches holds ReleaseExpired and PurgeOrphans to their
// Limit: a batch of one does one.
func checkHousekeepingBatches(t *testing.T, f fixture) {
	if f.Advance == nil {
		t.Skip("Env.Advance is not set")
	}
	const rows, batch = 2, 1
	for range rows {
		_, err := f.Usage.Reserve(f.Ctx, meter.ReserveRequest{Subject: f.childSubject(), Amount: one, TTL: time.Minute})
		expect.Must(t, err == nil, "%v", err)
	}
	f.Advance(time.Hour)
	r, err := f.Usage.ReleaseExpired(f.Ctx, meter.ReleaseExpiredRequest{Limit: batch})
	expect.That(t, !(err != nil || r.Released != batch), "ReleaseExpired with a limit of %d = %+v, %v", batch, r, err)
	if f.DeleteCapability == nil {
		return
	}
	var gone []uuid.UUID
	for range rows {
		id, err := f.newCapability(uuid.Nil(), unlimitedBudget)
		expect.Must(t, err == nil, "%v", err)
		_, err = f.charge(meter.ChargeRequest{Subject: newSubject(subjectSpec{capID: id}), Amount: one}, nil)
		expect.Must(t, err == nil, "%v", err)
		gone = append(gone, id)
	}
	for _, id := range gone {
		err := f.DeleteCapability(id)
		expect.Must(t, err == nil, "%v", err)
	}
	purged, err := f.Usage.PurgeOrphans(f.Ctx, meter.PurgeOrphansRequest{Limit: batch})
	expect.That(t, !(err != nil || purged.Purged != batch), "PurgeOrphans with a limit of %d = %+v, %v", batch, purged, err)
}

// checkCopySettleRefund holds a store's copy counters to following Settle and
// Refund as every other counter does: a settle moves the hold to spend, a
// refund returns it.
func checkCopySettleRefund(t *testing.T, f fixture) {
	reader, ok := f.Usage.(meter.CopyUsageReader)
	if !ok {
		t.Skip("the store reads no copy counters back")
	}
	copyID := []byte("copy-settle-refund")
	copies := []limes.CopyCeiling{{RevocationID: copyID, Budget: &limes.BudgetCeiling{Max: childBudget, UnitCode: unit}}}
	subject := newSubject(subjectSpec{capID: f.child, tenant: f.Tenant, budget: budgetOf(childBudget), copies: copies})
	r, err := f.Usage.Reserve(f.Ctx, meter.ReserveRequest{Subject: subject, Amount: 2 * one})
	expect.Must(t, err == nil, "%v", err)
	settled, err := f.Usage.Settle(f.Ctx, meter.SettleRequest{Subject: subject, ReservationID: r.ID, Amount: 3 * one}, nil)
	expect.Must(t, err == nil, "%v", err)
	_, err = f.Usage.Refund(f.Ctx, meter.RefundRequest{ChargeID: settled.ChargeID, Amount: one})
	expect.Must(t, err == nil, "%v", err)
	res, err := reader.CopyUsage(f.Ctx, meter.CopyUsageRequest{CapabilityID: f.child, RevocationIDs: [][]byte{copyID}})
	got := res.Copies
	expect.That(t, !(err != nil || len(got) != 1 || got[0].SpentAmount != 2*one || got[0].ReservedAmount != 0), "the copy after a settle of 3 and a refund of 1 = %+v, %v; want 2 spent, nothing held", got, err)
}

// checkTenantCeiling holds a store to the tenant aggregate: capabilities each
// within their own budgets must not sum past the tenant's.
func checkTenantCeiling(t *testing.T, f fixture) {
	other, err := f.newCapability(uuid.Nil(), limes.BudgetCeiling{Max: rootBudget})
	expect.Must(t, err == nil, "%v", err)
	_, err = f.charge(meter.ChargeRequest{Subject: newSubject(subjectSpec{capID: f.root, budget: budgetOf(rootBudget)}), Amount: 20 * one}, nil)
	expect.Must(t, err == nil, "%v", err)
	// 20 + 20 is within each root's 25, past the tenant's 30.
	_, err = f.charge(meter.ChargeRequest{Subject: newSubject(subjectSpec{capID: other, budget: budgetOf(rootBudget)}), Amount: 20 * one}, nil)
	expect.Must(t, errors.Is(err, limes.ErrTenantBudgetExceeded), "a charge past the tenant's ceiling: err = %v, want ErrTenantBudgetExceeded", err)
	expect.That(t, !(f.spent(t, other) != 0 || f.tenantSpent(t) != 20*one), "a charge refused by the tenant's ceiling moved spend")
}

// checkCallbackRollback holds a store to the atomicity of onCharged: its
// error is returned as it is, and no counter, ledger row or ExternalRef
// survives it.
func checkCallbackRollback(t *testing.T, f fixture) {
	boom := errors.New("side effect failed")
	_, err := f.charge(meter.ChargeRequest{Amount: one, ExternalRef: "call-1"}, func(context.Context, meter.Charged) error { return boom })
	expect.Must(t, errors.Is(err, boom), "charge with a failing callback: err = %v, want the callback's", err)
	expect.That(t, !(f.spent(t, f.child) != 0 || f.spent(t, f.root) != 0 || f.tenantSpent(t) != 0), "a charge whose callback failed moved spend")
	_, err = f.Usage.ChargeByRef(f.Ctx, meter.ChargeByRefRequest{CapabilityID: f.child, ExternalRef: "call-1"})
	expect.That(t, errors.Is(err, meter.ErrChargeNotFound), "the failed charge's ref: err = %v, want ErrChargeNotFound", err)
	// The ref is free again: a retry charges.
	r, err := f.charge(meter.ChargeRequest{Amount: one, ExternalRef: "call-1"}, nil)
	expect.That(t, !(err != nil || r.Replayed), "the retry = %+v, %v; want a fresh charge", r, err)

	res, err := f.Usage.Reserve(f.Ctx, meter.ReserveRequest{
		Subject: f.childSubject(),
		Amount:  2 * one,
	})
	expect.Must(t, err == nil, "%v", err)
	_, err = f.Usage.Settle(f.Ctx, meter.SettleRequest{Subject: f.childSubject(), ReservationID: res.ID, Amount: 2 * one},
		func(context.Context, meter.Charged) error { return boom })
	expect.Must(t, errors.Is(err, boom), "settle with a failing callback: err = %v, want the callback's", err)
	_, err = f.Usage.GetReservation(f.Ctx, res.ID)
	expect.That(t, err == nil, "the reservation after a failed settle: %v; want it still open", err)
	got := f.spent(t, f.child)
	expect.That(t, got == one, "child spent %v after a failed settle, want 1", got)
}

// checkCallbackSeesTheReceipt holds a store to handing onCharged the charge
// it runs inside: the receipt the call then returns, so a side effect such as
// an outbox row can name the ChargeID, the spend and an overrun.
func checkCallbackSeesTheReceipt(t *testing.T, f fixture) {
	var seen meter.ChargeReceipt
	runs := 0
	onCharged := func(_ context.Context, c meter.Charged) error {
		seen = c.Receipt
		runs++
		return nil
	}
	same := func(how string, got meter.ChargeReceipt) {
		t.Helper()
		expect.Must(t, runs == 1, "%s: onCharged ran %d times, want once", how, runs)
		expect.That(t, !(seen.ChargeID == uuid.Nil() || seen.ChargeID != got.ChargeID || seen.SpentAmount != got.SpentAmount ||
			seen.Overrun != got.Overrun || seen.Replayed || seen.TenantID != got.TenantID || seen.UnitCode != got.UnitCode), "%s: onCharged saw %+v, the call returned %+v", how, seen, got)
		runs = 0
	}

	got, err := f.charge(meter.ChargeRequest{Amount: one}, onCharged)
	expect.Must(t, err == nil, "%v", err)
	same("a charge", got)

	res, err := f.Usage.Reserve(f.Ctx, meter.ReserveRequest{Subject: f.childSubject(), Amount: one})
	expect.Must(t, err == nil, "%v", err)
	got, err = f.Usage.Settle(f.Ctx, meter.SettleRequest{Subject: f.childSubject(), ReservationID: res.ID, Amount: one}, onCharged)
	expect.Must(t, err == nil, "%v", err)
	same("a settle", got)

	got, err = f.charge(meter.ChargeRequest{Amount: childBudget, Overrun: meter.OverrunPolicyRecord}, onCharged)
	expect.Must(t, !(err != nil || !got.Overrun), "a charge past the ceiling = %+v, %v; want it recorded as an overrun", got, err)
	same("an overrun", got)
}

// checkBump holds a store to counting requests on the capability and every
// ancestor, and to refusing past the capability's own limit without counting.
func checkBump(t *testing.T, f fixture) {
	const limit = 2
	bump := func() (int64, error) {
		r, err := f.Usage.Bump(f.Ctx, meter.BumpRequest{Subject: newSubject(subjectSpec{capID: f.child, tenant: f.Tenant, requests: requestsOf(limit)})})
		return r.RequestCount, err
	}
	for want := int64(1); want <= limit; want++ {
		n, err := bump()
		expect.Must(t, !(err != nil || n != want), "bump = %d, %v; want %d", n, err, want)
	}
	_, err := bump()
	expect.Must(t, errors.Is(err, limes.ErrRequestLimitExceeded), "a bump past the limit: err = %v, want ErrRequestLimitExceeded", err)
	// Counting requests alone records the subject's unit, so a reader
	// shows the counters in it rather than in none.
	for _, id := range []uuid.UUID{f.child, f.root} {
		u, err := f.Usage.GetUsage(f.Ctx, id)
		expect.That(t, !(err != nil || u.RequestCount != limit || u.UnitCode != unit), "usage of %s = %+v, %v; want %d requests in %s", id, u, err, limit, unit)
	}
}

// checkRefund holds a store to the Refund contract: back to every counter
// the charge moved, never past what it took, and a full refund idempotent.
func checkRefund(t *testing.T, f fixture) {
	r, err := f.charge(meter.ChargeRequest{Amount: 3 * one}, nil)
	expect.Must(t, err == nil, "%v", err)
	expect.That(t, !(r.TenantID != f.Tenant || r.UnitCode != unit), "charge receipt names tenant %s in %q, want %s in %q", r.TenantID, r.UnitCode, f.Tenant, unit)
	refund := func(amount limes.Nanos) (meter.RefundReceipt, error) {
		return f.Usage.Refund(f.Ctx, meter.RefundRequest{ChargeID: r.ChargeID, Amount: amount})
	}
	got, err := refund(one)
	expect.Must(t, !(err != nil || got.Amount != one || got.ChargeID != r.ChargeID ||
		got.TenantID != f.Tenant || got.UnitCode != unit), "partial refund = %+v, %v; want 1 of charge %s, tenant %s in %q", got, err, r.ChargeID, f.Tenant, unit)
	_, err = refund(3 * one)
	expect.That(t, errors.Is(err, meter.ErrRefundExceedsCharge), "refunding past what is left: err = %v, want ErrRefundExceedsCharge", err)
	for name, req := range map[string]meter.RefundRequest{
		"neither an amount nor Remaining": {ChargeID: r.ChargeID},
		"an amount and Remaining":         {ChargeID: r.ChargeID, Amount: one, Remaining: true},
	} {
		_, err := f.Usage.Refund(f.Ctx, req)
		expect.That(t, errors.Is(err, limes.ErrInvalidRequest), "refund of %s: err = %v, want ErrInvalidRequest", name, err)
	}
	for range 2 { // the rest, then nothing: a full refund is idempotent
		_, err := f.Usage.Refund(f.Ctx, meter.RefundRequest{ChargeID: r.ChargeID, Remaining: true})
		expect.Must(t, err == nil, "full refund: %v", err)
	}
	expect.That(t, !(f.spent(t, f.child) != 0 || f.spent(t, f.root) != 0 || f.tenantSpent(t) != 0), "a full refund left spend on a counter the charge moved")
	_, err = f.Usage.Refund(f.Ctx, meter.RefundRequest{ChargeID: uuid.New(), Remaining: true})
	expect.That(t, errors.Is(err, meter.ErrChargeNotFound), "refund of an unknown charge: err = %v, want ErrChargeNotFound", err)
}

// checkHoldBlocksCharge holds a store to counting holds against charges as
// spend is counted.
func checkHoldBlocksCharge(t *testing.T, f fixture) {
	_, err := f.Usage.Reserve(f.Ctx, meter.ReserveRequest{
		Subject: f.childSubject(),
		Amount:  15 * one,
	})
	expect.Must(t, err == nil, "%v", err)
	_, err = f.charge(meter.ChargeRequest{Amount: 10 * one}, nil)
	expect.That(t, errors.Is(err, limes.ErrBudgetExceeded), "a charge past the ceiling with the hold: err = %v, want ErrBudgetExceeded", err)
	_, err = f.charge(meter.ChargeRequest{Amount: 5 * one}, nil)
	expect.That(t, err == nil, "a charge within the ceiling with the hold: %v", err)
}

// checkExpiry holds a store to letting a reservation lapse: past its TTL it
// cannot be settled, still counts until ReleaseExpired runs, and then does not.
func checkExpiry(t *testing.T, f fixture) {
	if f.Advance == nil {
		t.Skip("Env.Advance is not set")
	}
	const ttl = time.Minute
	res, err := f.Usage.Reserve(f.Ctx, meter.ReserveRequest{
		Subject: f.childSubject(),
		Amount:  15 * one,
		TTL:     ttl,
	})
	expect.Must(t, err == nil, "%v", err)
	// A hold that outlives the clock's step: ReleaseExpired leaves it.
	long, err := f.Usage.Reserve(f.Ctx, meter.ReserveRequest{
		Subject: newSubject(subjectSpec{capID: f.root, tenant: f.Tenant, budget: budgetOf(rootBudget)}),
		Amount:  one,
		TTL:     time.Hour,
	})
	expect.Must(t, err == nil, "%v", err)
	f.Advance(2 * ttl)
	_, err = f.Usage.Settle(f.Ctx, meter.SettleRequest{Subject: f.childSubject(), ReservationID: res.ID, Amount: one}, nil)
	expect.That(t, errors.Is(err, meter.ErrReservationNotFound), "settling an expired reservation: err = %v, want ErrReservationNotFound", err)
	_, err = f.Usage.GetReservation(f.Ctx, res.ID)
	expect.That(t, err == nil, "an expired hold not yet released still reads back: %v", err)
	_, err = f.charge(meter.ChargeRequest{Amount: 10 * one}, nil)
	expect.That(t, errors.Is(err, limes.ErrBudgetExceeded), "before ReleaseExpired the hold still counts: err = %v, want ErrBudgetExceeded", err)
	r, err := f.Usage.ReleaseExpired(f.Ctx, meter.ReleaseExpiredRequest{})
	expect.Must(t, !(err != nil || r.Released != 1), "ReleaseExpired = %+v, %v; want 1 released", r, err)
	_, err = f.Usage.GetReservation(f.Ctx, long.ID)
	expect.That(t, err == nil, "ReleaseExpired released a hold that has not expired: %v", err)
	_, err = f.charge(meter.ChargeRequest{Amount: 10 * one}, nil)
	expect.That(t, err == nil, "after ReleaseExpired: %v", err)
}

// checkListTenantBudgets holds a store to ListTenantBudgets' order, filters
// and paging: most at risk first, then by tenant id, a page at a time.
func checkListTenantBudgets(t *testing.T, f fixture) {
	checkListTenantBudgetsRefusesMalformed(t, f)
	unlimited, frozen := seedListedTenants(t, f)
	// One page per tenant listed, and one more to see the end: a cursor that
	// does not advance fails here rather than looping forever.
	const listed = 1 + tiedTenants + 2
	order, all := listTenantBudgetsOneByOne(t, f, listed)
	expect.Must(t, !(!all[f.Tenant] || !all[unlimited] || !all[frozen]), "paging skipped a tenant: %v", all)
	want := listed
	expect.That(t, len(order) == want, "listed %d tenants, want %d", len(order), want)
	expect.That(t, !(order[0].TenantID != frozen || order[len(order)-1].TenantID != unlimited), "listed %s first and %s last; want the ceiling of 0 %s first and the unlimited %s last", order[0].TenantID, order[len(order)-1].TenantID, frozen, unlimited)
	checkRiskOrder(t, order)
	checkAtOrPastFilter(t, f, frozen, unlimited)
	checkUnlimitedFilter(t, f, unlimited)
	checkFiltersPaged(t, f, order, unlimited, listed)
}

// checkListTenantBudgetsRefusesMalformed holds ListTenantBudgets to refusing
// a malformed request as ErrInvalidRequest.
func checkListTenantBudgetsRefusesMalformed(t *testing.T, f fixture) {
	for name, req := range map[string]meter.ListTenantBudgetsRequest{
		"a negative limit":             {Limit: -1},
		"a threshold past 100%":        {Filter: meter.TenantBudgetFilterAtOrPast, ThresholdBasisPoints: meter.BasisPointsPerWhole + 1},
		"a negative threshold":         {Filter: meter.TenantBudgetFilterAtOrPast, ThresholdBasisPoints: -1},
		"a threshold with no filter":   {ThresholdBasisPoints: 1},
		"a threshold beside unlimited": {Filter: meter.TenantBudgetFilterUnlimited, ThresholdBasisPoints: 1},
		"a filter this package lacks":  {Filter: meter.TenantBudgetFilterUnlimited + 1},
	} {
		_, err := f.Usage.ListTenantBudgets(f.listCtx(), req)
		expect.That(t, errors.Is(err, limes.ErrInvalidRequest), "ListTenantBudgets with %s: err = %v, want ErrInvalidRequest", name, err)
	}
}

// seedListedTenants gives the listing its tenants: the fixture's tenant has
// a ceiling of 30 and spends half of it; tiedTenants more have ceilings and
// spend nothing, so they tie and their ids order them; one more is
// unlimited, and one has a ceiling of 0.
func seedListedTenants(t *testing.T, f fixture) (unlimited, frozen uuid.UUID) {
	t.Helper()
	_, err := f.charge(meter.ChargeRequest{Subject: newSubject(subjectSpec{capID: f.root, budget: budgetOf(rootBudget)}), Amount: 15 * one}, nil)
	expect.Must(t, err == nil, "%v", err)
	newTenant := func(ceiling limes.BudgetCeiling) uuid.UUID {
		t.Helper()
		id, err := f.newTenant()
		expect.Must(t, err == nil, "%v", err)
		_, err = f.Usage.SetTenantBudget(f.tenantCtx(id), meter.SetTenantBudgetRequest{TenantID: id, Ceiling: inUnit(ceiling)})
		expect.Must(t, err == nil, "%v", err)
		return id
	}
	for range tiedTenants {
		newTenant(limes.BudgetCeiling{Max: tenantBudget})
	}
	return newTenant(unlimitedBudget), newTenant(limes.BudgetCeiling{})
}

// listTenantBudgetsOneByOne lists every tenant budget a page of one at a
// time, in the order listed, and the tenants seen, failing a page past
// listed or a tenant listed twice.
func listTenantBudgetsOneByOne(t *testing.T, f fixture, listed int) ([]meter.TenantBudget, map[uuid.UUID]bool) {
	t.Helper()
	all := map[uuid.UUID]bool{}
	var order []meter.TenantBudget
	cursor := ""
	for page := 0; ; page++ {
		expect.Must(t, !(page > listed), "paging did not end after %d pages of one tenant each", listed)
		pagePage, err := f.Usage.ListTenantBudgets(f.listCtx(), meter.ListTenantBudgetsRequest{Limit: 1, Cursor: cursor})
		page, next := pagePage.Items, pagePage.Next
		expect.Must(t, err == nil, "%v", err)
		expect.Must(t, !(len(page) > 1), "a page of %d rows under a limit of 1", len(page))
		for _, row := range page {
			expect.That(t, !(all[row.TenantID]), "tenant %s listed twice while nothing was spent", row.TenantID)
			all[row.TenantID] = true
			order = append(order, row)
		}
		if next == "" {
			return order, all
		}
		cursor = next
	}
}

// checkRiskOrder holds a listing to most at risk first, ties by ascending
// tenant id.
func checkRiskOrder(t *testing.T, order []meter.TenantBudget) {
	for i := 1; i < len(order); i++ {
		cur, prev := riskOf(order[i]), riskOf(order[i-1])
		switch {
		case prev.after(cur):
			t.Errorf("row %d (%+v) follows one at %+v; want most at risk first", i, cur, prev)
		case cur == prev && bytes.Compare(order[i].TenantID[:], order[i-1].TenantID[:]) < 0:
			t.Errorf("row %d (%s) follows %s at the same risk; want ascending tenant id", i, order[i].TenantID, order[i-1].TenantID)
		}
	}
}

// checkAtOrPastFilter holds TenantBudgetFilterAtOrPast to the tenants at or
// past its threshold, and at a threshold of 0 to every tenant with a
// ceiling and none without.
func checkAtOrPastFilter(t *testing.T, f fixture, frozen, unlimited uuid.UUID) {
	const half = 50
	riskyPage, err := f.Usage.ListTenantBudgets(f.listCtx(), meter.ListTenantBudgetsRequest{Filter: meter.TenantBudgetFilterAtOrPast, ThresholdBasisPoints: half * basisPointsPerPercent})
	risky := riskyPage.Items
	expect.That(t, !(err != nil || len(risky) != 2 || risky[0].TenantID != frozen || risky[1].TenantID != f.Tenant || riskyPct(risky[1]) != half), "tenants at 50%% or more = %+v, %v; want %s, at a ceiling of 0, then %s at 50%%", risky, err, frozen, f.Tenant)
	ceiled, err := f.Usage.ListTenantBudgets(f.listCtx(), meter.ListTenantBudgetsRequest{Filter: meter.TenantBudgetFilterAtOrPast})
	expect.That(t, !(err != nil || slices.ContainsFunc(ceiled.Items, func(s meter.TenantBudget) bool { return s.TenantID == unlimited }) ||
		!slices.ContainsFunc(ceiled.Items, func(s meter.TenantBudget) bool { return s.TenantID == f.Tenant })), "tenants with a ceiling = %+v, %v; want %s and not %s", ceiled.Items, err, f.Tenant, unlimited)
}

// checkUnlimitedFilter holds TenantBudgetFilterUnlimited to the tenants
// without a ceiling.
func checkUnlimitedFilter(t *testing.T, f fixture, unlimited uuid.UUID) {
	unlPage, err := f.Usage.ListTenantBudgets(f.listCtx(), meter.ListTenantBudgetsRequest{Filter: meter.TenantBudgetFilterUnlimited})
	unl := unlPage.Items
	expect.That(t, !(err != nil || slices.ContainsFunc(unl, func(s meter.TenantBudget) bool { return s.TenantID == f.Tenant }) ||
		!slices.ContainsFunc(unl, func(s meter.TenantBudget) bool { return s.TenantID == unlimited })), "unlimited tenants = %+v, %v; want %s and not %s", unl, err, unlimited, f.Tenant)
}

// checkFiltersPaged holds each filter, paged one row at a time, to exactly
// its rows and in the order of the list with no filter: a store that maps a
// filter to the wrong rows, or loses it between pages, fails here. And
// TenantBudgetFilterAll names the filter a request that names none takes.
func checkFiltersPaged(t *testing.T, f fixture, order []meter.TenantBudget, unlimited uuid.UUID, listed int) {
	var withCeiling []uuid.UUID
	for _, row := range order {
		if row.TenantID != unlimited {
			withCeiling = append(withCeiling, row.TenantID)
		}
	}
	for name, c := range map[string]struct {
		req  meter.ListTenantBudgetsRequest
		want []uuid.UUID
	}{
		"with a ceiling": {meter.ListTenantBudgetsRequest{Filter: meter.TenantBudgetFilterAtOrPast}, withCeiling},
		"unlimited":      {meter.ListTenantBudgetsRequest{Filter: meter.TenantBudgetFilterUnlimited}, []uuid.UUID{unlimited}},
	} {
		got := listedOneByOne(t, f, c.req, listed)
		expect.That(t, slices.Equal(got, c.want), "tenants %s, a page of one at a time = %v, want %v", name, got, c.want)
	}
	named, err := f.Usage.ListTenantBudgets(f.listCtx(), meter.ListTenantBudgetsRequest{Filter: meter.TenantBudgetFilterAll, Limit: listed})
	expect.Must(t, !(err != nil || len(named.Items) != len(order)), "every tenant, by name = %d rows, %v; want %d", len(named.Items), err, len(order))
	for i, row := range named.Items {
		expect.That(t, row.TenantID == order[i].TenantID, "every tenant, by name: row %d is %s, want %s as with no filter", i, row.TenantID, order[i].TenantID)
	}
}

// listedOneByOne is every tenant req lists, read a page of one row at a
// time; past pages pages, the cursor is taken not to advance.
func listedOneByOne(t *testing.T, f fixture, req meter.ListTenantBudgetsRequest, pages int) []uuid.UUID {
	t.Helper()
	var got []uuid.UUID
	req.Limit = 1
	for page := 0; ; page++ {
		expect.Must(t, !(page > pages), "paging %+v did not end after %d pages of one tenant each", req, pages)
		rows, err := f.Usage.ListTenantBudgets(f.listCtx(), req)
		expect.Must(t, err == nil, "%v", err)
		for _, row := range rows.Items {
			got = append(got, row.TenantID)
		}
		if rows.Next == "" {
			return got
		}
		req.Cursor = rows.Next
	}
}

// longestTTL is the longest hold a ReserveRequest can ask for: it would
// expire centuries out, past what a count of nanoseconds since 1970 can hold,
// and the hold ends with its capability instead.
const longestTTL = time.Duration(math.MaxInt64)

// checkReservationsTied holds a store to its order among holds that expire
// at the same instant: ascending ID, bytewise, page after page. Ties form
// only when the store's clock stands still between the holds; without one
// there is nothing to order and the check is skipped.
func checkReservationsTied(t *testing.T, f fixture) {
	const holds = 4
	var want []meter.Reservation
	for range holds {
		r, err := f.Usage.Reserve(f.Ctx, meter.ReserveRequest{Subject: f.childSubject(), Amount: one, TTL: time.Minute})
		expect.Must(t, err == nil, "%v", err)
		want = append(want, r)
	}
	slices.SortFunc(want, func(a, b meter.Reservation) int {
		return cmp.Or(a.ExpiresAt.Compare(b.ExpiresAt), bytes.Compare(a.ID[:], b.ID[:]))
	})
	if !slices.ContainsFunc(want[1:], func(r meter.Reservation) bool { return r.ExpiresAt.Equal(want[0].ExpiresAt) }) {
		t.Skip("the store's clock moved between the holds; none expire together")
	}
	var got []uuid.UUID
	cursor := ""
	for range holds + 1 {
		page, err := f.Usage.ListReservations(f.Ctx, meter.ListReservationsRequest{CapabilityID: f.child, Limit: 1, Cursor: cursor})
		expect.Must(t, err == nil, "%v", err)
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
	expect.That(t, slices.Equal(got, wantIDs), "holds expiring together, paged one at a time = %v, want %v in ascending ID", got, wantIDs)
}

// clockSlack is how far apart a store's clock may read for two calls made
// one after the other: far below any TTL the suite tells apart.
const clockSlack = time.Second

// checkDefaultReservationTTL holds a store to giving a hold with no TTL
// DefaultReservationTTL: it expires when one asking for that TTL outright
// does.
func checkDefaultReservationTTL(t *testing.T, f fixture) {
	reserve := func(ttl time.Duration) time.Time {
		t.Helper()
		r, err := f.Usage.Reserve(f.Ctx, meter.ReserveRequest{Subject: f.childSubject(), Amount: one, TTL: ttl})
		expect.Must(t, err == nil, "%v", err)
		return r.ExpiresAt
	}
	defaulted, explicit := reserve(0), reserve(meter.DefaultReservationTTL)
	gap := explicit.Sub(defaulted)
	expect.That(t, !(gap < 0 || gap > clockSlack), "a hold with no TTL expires at %v, %v before one of DefaultReservationTTL; want within %v", defaulted, gap, clockSlack)
}

// checkReservationsPage holds a store to paging a capability's reservations
// in their order — the last two expiring past year 2262, where a cursor
// counting Unix nanoseconds overflows — and to refusing a negative page size.
func checkReservationsPage(t *testing.T, f fixture) {
	// Soonest first; the last one ends with its capability, after the others.
	ttls := []time.Duration{time.Minute, 2 * time.Minute, longestTTL}
	holds := len(ttls)
	var want []uuid.UUID
	for _, ttl := range ttls {
		r, err := f.Usage.Reserve(f.Ctx, meter.ReserveRequest{
			Subject: f.childSubject(),
			Amount:  one,
			TTL:     ttl,
		})
		expect.Must(t, err == nil, "%v", err)
		want = append(want, r.ID)
	}
	var got []uuid.UUID
	cursor := ""
	for range holds + 1 {
		page, err := f.Usage.ListReservations(f.Ctx, meter.ListReservationsRequest{CapabilityID: f.child, Limit: 1, Cursor: cursor})
		expect.Must(t, err == nil, "%v", err)
		for _, r := range page.Items {
			got = append(got, r.ID)
		}
		if page.Next == "" {
			break
		}
		cursor = page.Next
	}
	expect.That(t, slices.Equal(got, want), "paged one at a time, the holds = %v, want %v soonest first", got, want)
	_, err := f.Usage.ListReservations(f.Ctx, meter.ListReservationsRequest{CapabilityID: f.child, Limit: -1})
	expect.That(t, errors.Is(err, limes.ErrInvalidRequest), "ListReservations with a negative limit: err = %v, want ErrInvalidRequest", err)
}

// checkHousekeepingLimits holds a store to refusing a negative batch, and a
// DeleteUsage naming no capability.
func checkHousekeepingLimits(t *testing.T, f fixture) {
	_, err := f.Usage.ReleaseExpired(f.Ctx, meter.ReleaseExpiredRequest{Limit: -1})
	expect.That(t, errors.Is(err, limes.ErrInvalidRequest), "ReleaseExpired with a negative limit: err = %v, want ErrInvalidRequest", err)
	_, err = f.Usage.PurgeOrphans(f.Ctx, meter.PurgeOrphansRequest{Limit: -1})
	expect.That(t, errors.Is(err, limes.ErrInvalidRequest), "PurgeOrphans with a negative limit: err = %v, want ErrInvalidRequest", err)
	err = f.Usage.DeleteUsage(f.Ctx, meter.DeleteUsageRequest{})
	expect.That(t, errors.Is(err, limes.ErrInvalidRequest), "DeleteUsage naming no capability: err = %v, want ErrInvalidRequest", err)
}

// checkLineageAfterRemoval holds a store to counting only the ancestors
// still on record: a chain MaxDelegationDepth deep charges, takes no child,
// and once its top is removed takes one, which charges like any other.
func checkLineageAfterRemoval(t *testing.T, f fixture) {
	if f.DeleteCapability == nil {
		t.Skip("Env.DeleteCapability not set")
	}
	top, err := f.newCapability(uuid.Nil(), unlimitedBudget)
	expect.Must(t, err == nil, "%v", err)
	leaf := top
	for range limes.MaxDelegationDepth {
		leaf, err = f.newCapability(leaf, unlimitedBudget)
		expect.Must(t, err == nil, "%v", err)
	}
	charge := func(capID uuid.UUID) error {
		_, err := f.Usage.Charge(f.Ctx, meter.ChargeRequest{Subject: newSubject(subjectSpec{capID: capID, tenant: f.Tenant}), Amount: one}, nil)
		return err
	}
	err = charge(leaf)
	expect.Must(t, err == nil, "a charge with %d ancestors on record: %v", limes.MaxDelegationDepth, err)
	_, err = f.newCapability(leaf, unlimitedBudget)
	expect.Must(t, errors.Is(err, limes.ErrDelegationTooDeep), "a capability with %d ancestors on record: err = %v, want ErrDelegationTooDeep", limes.MaxDelegationDepth+1, err)
	err = f.DeleteCapability(top)
	expect.Must(t, err == nil, "%v", err)
	deeper, err := f.newCapability(leaf, unlimitedBudget)
	expect.Must(t, err == nil, "a capability with %d ancestors on record, the top removed: %v", limes.MaxDelegationDepth, err)
	err = charge(deeper)
	expect.That(t, err == nil, "a charge with %d ancestors on record, the top removed: %v", limes.MaxDelegationDepth, err)
}

// checkPurgeOrphans holds a store to deleting the usage of a capability no
// longer on record, and to returning what it held to its ancestor.
func checkPurgeOrphans(t *testing.T, f fixture) {
	if f.DeleteCapability == nil {
		t.Skip("Env.DeleteCapability not set")
	}
	_, err := f.charge(meter.ChargeRequest{Amount: one}, nil)
	expect.Must(t, err == nil, "%v", err)
	_, err = f.Usage.Reserve(f.Ctx, meter.ReserveRequest{
		Subject: f.childSubject(),
		Amount:  one,
	})
	expect.Must(t, err == nil, "%v", err)
	err = f.DeleteCapability(f.child)
	expect.Must(t, err == nil, "%v", err)
	got, err := f.Usage.PurgeOrphans(f.Ctx, meter.PurgeOrphansRequest{})
	expect.Must(t, !(err != nil || got.Purged < 1), "PurgeOrphans = %+v, %v; want the removed capability's row", got, err)
	_, err = f.Usage.GetUsage(f.Ctx, f.child)
	expect.That(t, errors.Is(err, meter.ErrUsageNotFound), "the removed capability's usage outlived PurgeOrphans: %v", err)
	u, err := f.Usage.GetUsage(f.Ctx, f.root)
	expect.That(t, !(err != nil || u.ReservedAmount != 0), "the root still holds %v for its removed child, %v; want 0", u.ReservedAmount, err)
}

// checkNotOnRecord holds a store to refusing a capability whose record is
// gone: its ancestors can no longer be read, and metering it alone would let
// it spend past its live root's ceiling.
func checkNotOnRecord(t *testing.T, f fixture) {
	if f.DeleteCapability == nil {
		t.Skip("Env.DeleteCapability not set")
	}
	held, err := f.Usage.Reserve(f.Ctx, meter.ReserveRequest{Subject: f.childSubject(), Amount: one})
	expect.Must(t, err == nil, "%v", err)
	err = f.DeleteCapability(f.child)
	expect.Must(t, err == nil, "%v", err)
	subject := f.childSubject()
	_, err = f.Usage.Settle(f.Ctx, meter.SettleRequest{Subject: subject, ReservationID: held.ID, Amount: one}, nil)
	expect.That(t, errors.Is(err, limes.ErrNotFound), "a settle of a capability not on record: err = %v, want ErrNotFound", err)
	_, err = f.Usage.Bump(f.Ctx, meter.BumpRequest{Subject: subject})
	expect.That(t, errors.Is(err, limes.ErrNotFound), "a bump of a capability not on record: err = %v, want ErrNotFound", err)
	_, err = f.Usage.Charge(f.Ctx, meter.ChargeRequest{Subject: subject, Amount: one}, nil)
	expect.That(t, errors.Is(err, limes.ErrNotFound), "a charge of a capability not on record: err = %v, want ErrNotFound", err)
	_, err = f.Usage.Reserve(f.Ctx, meter.ReserveRequest{Subject: subject, Amount: one})
	expect.That(t, errors.Is(err, limes.ErrNotFound), "a hold of a capability not on record: err = %v, want ErrNotFound", err)
	u, err := f.Usage.GetUsage(f.Ctx, f.root)
	expect.That(t, !(err == nil && (u.SpentAmount != 0 || u.RequestCount != 0)), "the root counted %+v for a child not on record", u)
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

func riskOf(b meter.TenantBudget) risk {
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

// fullPct is a whole, in percent; basisPointsPerPercent turns a percentage
// into basis points.
const (
	fullPct               = 100
	basisPointsPerPercent = meter.BasisPointsPerWhole / fullPct
)

func riskyPct(s meter.TenantBudget) float64 {
	pct, limited := s.Utilization()
	if !limited {
		return noUtilization
	}
	return pct
}

// checkPeriodEnd holds a store to keeping a tenant's PeriodEnd as set, and a
// zero one as zero: an open-ended window.
func checkPeriodEnd(t *testing.T, f fixture) {
	// Microseconds: the finest a timestamp column keeps.
	end := time.Now().UTC().Add(periodLength).Truncate(time.Microsecond)
	for _, want := range []time.Time{end, {}} {
		cur, err := f.Usage.GetTenantBudget(f.Ctx, f.Tenant)
		expect.Must(t, err == nil, "%v", err)
		set, err := f.Usage.SetTenantBudget(f.Ctx, meter.SetTenantBudgetRequest{
			TenantID: f.Tenant, Ceiling: limes.BudgetCeiling{Max: tenantBudget, UnitCode: unit}, PeriodEnd: want, ExpectedVersion: cur.ResourceVersion,
		})
		expect.Must(t, !(err != nil || !set.PeriodEnd.Equal(want)), "SetTenantBudget(PeriodEnd %v) = %v, %v", want, set.PeriodEnd, err)
		got, err := f.Usage.GetTenantBudget(f.Ctx, f.Tenant)
		expect.That(t, !(err != nil || !got.PeriodEnd.Equal(want)), "GetTenantBudget after PeriodEnd %v = %v, %v", want, got.PeriodEnd, err)
	}
}

// checkPeriodStart holds a store to rolling a tenant's PeriodStart to its
// clock's now on ResetSpend, and only then: a new ceiling mid-window keeps
// the window it is in.
func checkPeriodStart(t *testing.T, f fixture) {
	if f.Advance == nil {
		t.Skip("Env.Advance is not set")
	}
	const step = time.Hour
	set := func(reset bool) meter.TenantBudget {
		t.Helper()
		f.Advance(step)
		cur, err := f.Usage.GetTenantBudget(f.Ctx, f.Tenant)
		expect.Must(t, err == nil, "%v", err)
		_, err = f.Usage.SetTenantBudget(f.Ctx, meter.SetTenantBudgetRequest{
			TenantID: f.Tenant, Ceiling: limes.BudgetCeiling{Max: tenantBudget, UnitCode: unit}, ResetSpend: reset, ExpectedVersion: cur.ResourceVersion,
		})
		expect.Must(t, err == nil, "%v", err)
		got, err := f.Usage.GetTenantBudget(f.Ctx, f.Tenant)
		expect.Must(t, err == nil, "%v", err)
		return got
	}
	before, err := f.Usage.GetTenantBudget(f.Ctx, f.Tenant)
	expect.Must(t, err == nil, "%v", err)
	kept := set(false)
	expect.That(t, kept.PeriodStart.Equal(before.PeriodStart), "PeriodStart after a new ceiling without ResetSpend = %v, want %v kept", kept.PeriodStart, before.PeriodStart)
	// Two steps on: the one before the kept ceiling and the one before this.
	rolled := set(true)
	expect.That(t, !(rolled.PeriodStart.Before(before.PeriodStart.Add(2 * step))), "PeriodStart after ResetSpend = %v, want at least %v, the store's now", rolled.PeriodStart, before.PeriodStart.Add(2*step))
}

// checkSettleWithinHold holds a store to Settle's contract: only an amount
// above the hold must fit the ceilings. A settle at or below it adds nothing
// to what is already counted, so a ceiling crossed meanwhile — by an
// OverrunPolicyRecord charge elsewhere in the chain, or a tenant ceiling
// lowered — does not refuse it, and the whole hold is released everywhere it counted.
func checkSettleWithinHold(t *testing.T, f fixture) {
	const held, actual = 10 * one, 4 * one
	r, err := f.Usage.Reserve(f.Ctx, meter.ReserveRequest{Subject: f.childSubject(), Amount: held})
	expect.Must(t, err == nil, "%v", err)
	again, err := f.Usage.Reserve(f.Ctx, meter.ReserveRequest{Subject: f.childSubject(), Amount: one})
	expect.Must(t, err == nil, "%v", err)
	// The root spends past its ceiling, its child's hold still counted.
	_, err = f.Usage.Charge(f.Ctx, meter.ChargeRequest{
		Subject: newSubject(subjectSpec{capID: f.root, tenant: f.Tenant, budget: budgetOf(rootBudget)}),
		Amount:  rootBudget,
		Overrun: meter.OverrunPolicyRecord,
	}, nil)
	expect.Must(t, err == nil, "%v", err)
	got, err := f.Usage.Settle(f.Ctx, meter.SettleRequest{Subject: f.childSubject(), ReservationID: r.ID, Amount: actual}, nil)
	expect.Must(t, err == nil, "settle below the hold after the root crossed its ceiling: %v", err)
	expect.That(t, !(got.Overrun), "a settle below its hold reported an overrun")
	for name, id := range map[string]uuid.UUID{"child": f.child, "root": f.root} {
		u, err := f.Usage.GetUsage(f.Ctx, id)
		expect.That(t, !(err != nil || u.ReservedAmount != one), "%s after the settle = %+v, %v; want only the other hold, %v", name, u, err, one)
	}
	b, err := f.Usage.GetTenantBudget(f.Ctx, f.Tenant)
	expect.That(t, !(err != nil || b.ReservedAmount != one), "tenant after the settle = %+v, %v; want only the other hold, %v", b, err, one)
	expect.That(t, f.spent(t, f.child) == actual, "child spent %v, want %v", f.spent(t, f.child), actual)

	// A tenant ceiling lowered under a hold refuses nothing the hold covers.
	cur, err := f.Usage.GetTenantBudget(f.Ctx, f.Tenant)
	expect.Must(t, err == nil, "%v", err)
	_, err = f.Usage.SetTenantBudget(f.Ctx, meter.SetTenantBudgetRequest{
		TenantID: f.Tenant, Ceiling: limes.BudgetCeiling{Max: cent, UnitCode: unit}, ExpectedVersion: cur.ResourceVersion,
	})
	expect.Must(t, err == nil, "%v", err)
	_, err = f.Usage.Settle(f.Ctx, meter.SettleRequest{Subject: f.childSubject(), ReservationID: again.ID, Amount: one}, nil)
	expect.That(t, err == nil, "settle at the hold after the tenant ceiling was lowered under it: %v", err)
}

// checkZeroSubject holds a store to refusing the zero MeterSubject — one no
// verifier or record made, naming no capability and no tenant — rather than
// counting it under the nil id.
func checkZeroSubject(t *testing.T, f fixture) {
	var zero limes.MeterSubject
	_, err := f.Usage.Bump(f.Ctx, meter.BumpRequest{Subject: zero})
	expect.That(t, errors.Is(err, limes.ErrInvalidRequest), "Bump: err = %v, want ErrInvalidRequest", err)
	_, err = f.Usage.Charge(f.Ctx, meter.ChargeRequest{Subject: zero, Amount: one}, nil)
	expect.That(t, errors.Is(err, limes.ErrInvalidRequest), "Charge: err = %v, want ErrInvalidRequest", err)
	_, err = f.Usage.Reserve(f.Ctx, meter.ReserveRequest{Subject: zero, Amount: one})
	expect.That(t, errors.Is(err, limes.ErrInvalidRequest), "Reserve: err = %v, want ErrInvalidRequest", err)
	_, err = f.Usage.Settle(f.Ctx, meter.SettleRequest{Subject: zero, ReservationID: uuid.New(), Amount: one}, nil)
	expect.That(t, errors.Is(err, limes.ErrInvalidRequest), "Settle: err = %v, want ErrInvalidRequest", err)
	_, err = f.Usage.GetUsage(f.Ctx, uuid.Nil())
	expect.That(t, errors.Is(err, meter.ErrUsageNotFound), "GetUsage(nil id) = %v; want ErrUsageNotFound, nothing counted", err)
}

// A call naming Tenant, made under another tenant's context, is refused as
// one: the rows are there, so answering as if they were not would read as a
// bad request or a miss. Nothing it would have moved moves.
func checkScopeMismatch(t *testing.T, f fixture) {
	if !f.ScopesTenants {
		t.Skip("Env.ScopesTenants not set")
	}
	other, err := f.newTenant()
	expect.Must(t, err == nil, "NewTenant: %v", err)
	held, err := f.Usage.Reserve(f.Ctx, meter.ReserveRequest{Subject: f.childSubject(), Amount: one})
	expect.Must(t, err == nil, "reserve: %v", err)
	before, err := f.Usage.GetUsage(f.Ctx, f.child)
	expect.Must(t, err == nil, "usage: %v", err)
	wrong := f.tenantCtx(other)
	for name, call := range map[string]func() error{
		"Bump": func() error {
			_, err := f.Usage.Bump(wrong, meter.BumpRequest{Subject: f.childSubject()})
			return err
		},
		"Charge": func() error {
			_, err := f.Usage.Charge(wrong, meter.ChargeRequest{Subject: f.childSubject(), Amount: one}, nil)
			return err
		},
		"Reserve": func() error {
			_, err := f.Usage.Reserve(wrong, meter.ReserveRequest{Subject: f.childSubject(), Amount: one})
			return err
		},
		"Settle": func() error {
			_, err := f.Usage.Settle(wrong, meter.SettleRequest{Subject: f.childSubject(), ReservationID: held.ID, Amount: one}, nil)
			return err
		},
		"GetTenantBudget": func() error {
			_, err := f.Usage.GetTenantBudget(wrong, f.Tenant)
			return err
		},
		"SetTenantBudget": func() error {
			_, err := f.Usage.SetTenantBudget(wrong, meter.SetTenantBudgetRequest{
				TenantID: f.Tenant, Ceiling: limes.BudgetCeiling{Unlimited: true, UnitCode: unit},
			})
			return err
		},
	} {
		err := call()
		expect.That(t, errors.Is(err, limes.ErrScopeMismatch), "%s for Tenant under another tenant's context: err = %v, want ErrScopeMismatch", name, err)
	}
	after, err := f.Usage.GetUsage(f.Ctx, f.child)
	expect.Must(t, err == nil, "usage: %v", err)
	expect.That(t, reflect.DeepEqual(after, before), "usage after the refused calls = %+v, want %+v", after, before)
	_, err = f.Usage.GetReservation(f.Ctx, held.ID)
	expect.That(t, err == nil, "the hold after a refused Settle: %v; want it open", err)
	b, err := f.Usage.GetTenantBudget(f.Ctx, f.Tenant)
	expect.Must(t, err == nil, "tenant budget: %v", err)
	want := (limes.BudgetCeiling{Max: tenantBudget, UnitCode: unit})
	expect.That(t, b.Ceiling == want, "tenant ceiling after a refused SetTenantBudget = %+v, want %v", b.Ceiling, want)
}

// A read-only scope reads Tenant's usage and writes none: every call that
// writes, housekeeping included, is refused as out of scope rather than let
// through as no rows, runs no onCharged, and moves no counter, hold or
// ceiling.
func checkReadOnlyScope(t *testing.T, f fixture) {
	ro := f.ReadOnlyCtx
	if ro == nil {
		t.Skip("Env.ReadOnlyCtx not set")
	}
	charged, err := f.Usage.Charge(f.Ctx, meter.ChargeRequest{Subject: f.childSubject(), Amount: one}, nil)
	expect.Must(t, err == nil, "charge: %v", err)
	held, err := f.Usage.Reserve(f.Ctx, meter.ReserveRequest{Subject: f.childSubject(), Amount: one})
	expect.Must(t, err == nil, "reserve: %v", err)
	before, err := f.Usage.GetUsage(ro, f.child)
	expect.Must(t, err == nil, "GetUsage under the read-only scope: %v", err)
	ran := false
	onCharged := func(context.Context, meter.Charged) error {
		ran = true
		return nil
	}
	writes := map[string]func() error{
		"Bump": func() error {
			_, err := f.Usage.Bump(ro, meter.BumpRequest{Subject: f.childSubject()})
			return err
		},
		"Charge": func() error {
			_, err := f.Usage.Charge(ro, meter.ChargeRequest{Subject: f.childSubject(), Amount: one}, onCharged)
			return err
		},
		"Reserve": func() error {
			_, err := f.Usage.Reserve(ro, meter.ReserveRequest{Subject: f.childSubject(), Amount: one})
			return err
		},
		"Settle": func() error {
			_, err := f.Usage.Settle(ro, meter.SettleRequest{Subject: f.childSubject(), ReservationID: held.ID, Amount: one}, onCharged)
			return err
		},
		"Release": func() error {
			return f.Usage.Release(ro, meter.ReleaseRequest{Subject: f.childSubject(), ReservationID: held.ID})
		},
		"Refund": func() error {
			_, err := f.Usage.Refund(ro, meter.RefundRequest{ChargeID: charged.ChargeID, Remaining: true})
			return err
		},
		"SetTenantBudget": func() error {
			_, err := f.Usage.SetTenantBudget(ro, meter.SetTenantBudgetRequest{
				TenantID: f.Tenant, Ceiling: limes.BudgetCeiling{Unlimited: true, UnitCode: unit},
			})
			return err
		},
		"ReleaseExpired": func() error {
			_, err := f.Usage.ReleaseExpired(ro, meter.ReleaseExpiredRequest{})
			return err
		},
		"PurgeOrphans": func() error {
			_, err := f.Usage.PurgeOrphans(ro, meter.PurgeOrphansRequest{})
			return err
		},
		"DeleteUsage": func() error { return f.Usage.DeleteUsage(ro, meter.DeleteUsageRequest{CapabilityID: f.child}) },
	}
	if eraser, ok := f.Usage.(meter.TenantUsageEraser); ok {
		writes["EraseTenantUsage"] = func() error {
			return eraser.EraseTenantUsage(ro, meter.EraseTenantUsageRequest{TenantID: f.Tenant})
		}
	}
	for name, write := range writes {
		err := write()
		expect.That(t, errors.Is(err, limes.ErrScopeMismatch), "%s under the read-only scope: err = %v, want ErrScopeMismatch", name, err)
	}
	expect.That(t, !(ran), "a write under the read-only scope ran onCharged")
	after, err := f.Usage.GetUsage(f.Ctx, f.child)
	expect.Must(t, err == nil, "usage: %v", err)
	expect.That(t, reflect.DeepEqual(after, before), "usage after the refused calls = %+v, want %+v", after, before)
	_, err = f.Usage.GetReservation(f.Ctx, held.ID)
	expect.That(t, err == nil, "the hold after a refused Settle or Release: %v; want it open", err)
	b, err := f.Usage.GetTenantBudget(f.Ctx, f.Tenant)
	expect.Must(t, err == nil, "tenant budget: %v", err)
	want := (limes.BudgetCeiling{Max: tenantBudget, UnitCode: unit})
	expect.That(t, b.Ceiling == want, "tenant ceiling after a refused SetTenantBudget = %+v, want %v", b.Ceiling, want)
}

// Erasing a tenant's usage drops its counters, holds, copies' counters,
// charges and aggregate, and no other tenant's.
func checkEraseTenantUsage(t *testing.T, f fixture) {
	eraser, ok := f.Usage.(meter.TenantUsageEraser)
	if !ok {
		t.Skip("the UsageStore does not implement meter.TenantUsageEraser")
	}
	other, err := f.newTenant()
	expect.Must(t, err == nil, "NewTenant: %v", err)
	theirCap, err := f.NewCapability(CapabilitySpec{Tenant: other, Requests: limes.RequestCeiling{Unlimited: true}, Budget: unlimitedBudget})
	expect.Must(t, err == nil, "capability of another tenant: %v", err)
	theirCtx := f.tenantCtx(other)
	_, err = f.Usage.SetTenantBudget(theirCtx, meter.SetTenantBudgetRequest{
		TenantID: other, Ceiling: limes.BudgetCeiling{Unlimited: true, UnitCode: unit},
	})
	expect.Must(t, err == nil, "budget of another tenant: %v", err)
	theirSubject := newSubject(subjectSpec{capID: theirCap, tenant: other})
	theirCharge, err := f.Usage.Charge(theirCtx, meter.ChargeRequest{Subject: theirSubject, Amount: one}, nil)
	expect.Must(t, err == nil, "charge of another tenant: %v", err)
	copyID := []byte("erased-tenant-copy")
	mine := newSubject(subjectSpec{capID: f.child, tenant: f.Tenant, budget: budgetOf(childBudget),
		copies: []limes.CopyCeiling{{RevocationID: copyID, Budget: &limes.BudgetCeiling{Max: childBudget, UnitCode: unit}}}})
	charged, err := f.Usage.Charge(f.Ctx, meter.ChargeRequest{Subject: mine, Amount: one, ExternalRef: "erased"}, nil)
	expect.Must(t, err == nil, "charge: %v", err)
	held, err := f.Usage.Reserve(f.Ctx, meter.ReserveRequest{Subject: f.childSubject(), Amount: one})
	expect.Must(t, err == nil, "reserve: %v", err)
	req := meter.EraseTenantUsageRequest{TenantID: f.Tenant}
	if f.ScopesTenants {
		err := eraser.EraseTenantUsage(theirCtx, req)
		expect.That(t, errors.Is(err, limes.ErrScopeMismatch), "EraseTenantUsage under another tenant's context: err = %v, want ErrScopeMismatch", err)
	}
	err = eraser.EraseTenantUsage(f.Ctx, meter.EraseTenantUsageRequest{})
	expect.That(t, errors.Is(err, limes.ErrInvalidRequest), "EraseTenantUsage naming no tenant: err = %v, want ErrInvalidRequest", err)
	err = eraser.EraseTenantUsage(f.Ctx, req)
	expect.Must(t, err == nil, "EraseTenantUsage: %v", err)
	for _, id := range []uuid.UUID{f.root, f.child} {
		u, err := f.Usage.GetUsage(f.Ctx, id)
		expect.That(t, errors.Is(err, meter.ErrUsageNotFound), "usage of %s after the erase = %+v, %v; want ErrUsageNotFound", id, u, err)
	}
	_, err = f.Usage.GetCharge(f.Ctx, charged.ChargeID)
	expect.That(t, errors.Is(err, meter.ErrChargeNotFound), "GetCharge after the erase: err = %v, want ErrChargeNotFound", err)
	_, err = f.Usage.ChargeByRef(f.Ctx, meter.ChargeByRefRequest{CapabilityID: f.child, ExternalRef: "erased"})
	expect.That(t, errors.Is(err, meter.ErrChargeNotFound), "ChargeByRef after the erase: err = %v, want ErrChargeNotFound", err)
	_, err = f.Usage.GetReservation(f.Ctx, held.ID)
	expect.That(t, errors.Is(err, meter.ErrReservationNotFound), "GetReservation after the erase: err = %v, want ErrReservationNotFound", err)
	_, err = f.Usage.GetTenantBudget(f.Ctx, f.Tenant)
	expect.That(t, errors.Is(err, meter.ErrTenantBudgetNotFound), "GetTenantBudget after the erase: err = %v, want ErrTenantBudgetNotFound", err)
	if copies, ok := f.Usage.(meter.CopyUsageReader); ok {
		got, err := copies.CopyUsage(f.Ctx, meter.CopyUsageRequest{CapabilityID: f.child, RevocationIDs: [][]byte{copyID}})
		expect.That(t, !(err != nil || len(got.Copies) != 0), "CopyUsage after the erase = %+v, %v; want no copy", got, err)
	}
	u, err := f.Usage.GetUsage(theirCtx, theirCap)
	expect.That(t, !(err != nil || u.SpentAmount != one), "another tenant's usage after the erase = %+v, %v; want %v spent", u, err, one)
	_, err = f.Usage.GetCharge(theirCtx, theirCharge.ChargeID)
	expect.That(t, err == nil, "another tenant's charge after the erase: %v", err)
	b, err := f.Usage.GetTenantBudget(theirCtx, other)
	expect.That(t, !(err != nil || b.SpentAmount != one), "another tenant's budget after the erase = %+v, %v; want %v spent", b, err, one)

	// A call made after the erase — a request verified before it, a late
	// cost — counts nothing of the tenant again: its capabilities are
	// still on record, revoked, but no row of it comes back.
	afterErase := map[string]func() error{
		"Bump": func() error {
			_, err := f.Usage.Bump(f.Ctx, meter.BumpRequest{Subject: f.childSubject()})
			return err
		},
		"Charge": func() error {
			_, err := f.Usage.Charge(f.Ctx, meter.ChargeRequest{Subject: f.childSubject(), Amount: one}, nil)
			return err
		},
		"Reserve": func() error {
			_, err := f.Usage.Reserve(f.Ctx, meter.ReserveRequest{Subject: f.childSubject(), Amount: one})
			return err
		},
		"SetTenantBudget": func() error {
			_, err := f.Usage.SetTenantBudget(f.Ctx, meter.SetTenantBudgetRequest{TenantID: f.Tenant, Ceiling: limes.BudgetCeiling{Unlimited: true, UnitCode: unit}})
			return err
		},
		// A record inserted after the erase would be one its revocation
		// missed, metered from nothing.
		"Insert": func() error {
			_, err := f.NewCapability(CapabilitySpec{Tenant: f.Tenant, Requests: limes.RequestCeiling{Unlimited: true}, Budget: unlimitedBudget})
			return err
		},
	}
	for name, call := range afterErase {
		err := call()
		expect.That(t, !(!errors.Is(err, limes.ErrTenantErased) || !errors.Is(err, limes.ErrRevoked)), "%s after the erase: err = %v, want ErrTenantErased, matching ErrRevoked", name, err)
	}
	u, err = f.Usage.GetUsage(f.Ctx, f.child)
	expect.That(t, errors.Is(err, meter.ErrUsageNotFound), "usage after metering an erased tenant = %+v, %v; want ErrUsageNotFound", u, err)
	_, err = f.Usage.GetTenantBudget(f.Ctx, f.Tenant)
	expect.That(t, errors.Is(err, meter.ErrTenantBudgetNotFound), "GetTenantBudget after metering an erased tenant: err = %v, want ErrTenantBudgetNotFound", err)
	_, err = f.Usage.Bump(theirCtx, meter.BumpRequest{Subject: theirSubject})
	expect.That(t, err == nil, "another tenant's request after the erase: %v", err)
}

// sumWindow is how far either side of a check's clock SumCharges looks when
// the Env reads no clock: past any store's offset from the wall clock.
const sumWindow = 24 * time.Hour

// sumPeriod is a period holding every charge a check takes.
func (f fixture) sumPeriod() (from, to time.Time) {
	now := time.Now()
	if f.Now != nil {
		now = f.Now()
	}
	return now.Add(-sumWindow), now.Add(sumWindow)
}

// SumCharges sums Tenant's charges alone, grouped as asked, the largest
// first, a refund in its charge's group, and pages them.
func checkSumCharges(t *testing.T, f fixture) {
	if f.Ledger == nil {
		t.Skip("Env.Ledger not set")
	}
	other, err := f.newTenant()
	expect.Must(t, err == nil, "NewTenant: %v", err)
	theirCap, err := f.NewCapability(CapabilitySpec{Tenant: other, Requests: limes.RequestCeiling{Unlimited: true}, Budget: unlimitedBudget})
	expect.Must(t, err == nil, "capability of another tenant: %v", err)
	_, err = f.Usage.Charge(f.tenantCtx(other), meter.ChargeRequest{Subject: newSubject(subjectSpec{capID: theirCap, tenant: other}), Amount: 7 * one}, nil)
	expect.Must(t, err == nil, "charge of another tenant: %v", err)
	root := newSubject(subjectSpec{capID: f.root, tenant: f.Tenant, budget: budgetOf(rootBudget)})
	for _, c := range []meter.ChargeRequest{
		{Amount: 2 * one, Op: limes.OpGet, Actor: "a"},
		{Amount: 3 * one, Op: limes.OpPut, Actor: "b"},
		{Subject: root, Amount: 6 * one, Op: limes.OpGet, Actor: "a"},
	} {
		r, err := f.charge(c, nil)
		expect.Must(t, err == nil, "charge: %v", err)
		if c.Op == limes.OpPut {
			_, err := f.Usage.Refund(f.Ctx, meter.RefundRequest{ChargeID: r.ChargeID, Amount: one})
			expect.Must(t, err == nil, "refund: %v", err)
		}
	}
	from, to := f.sumPeriod()
	sums := func(req meter.SumChargesRequest) []meter.ChargeSum {
		t.Helper()
		req.TenantID, req.From, req.To = f.Tenant, from, to
		page, err := f.Ledger.SumCharges(f.Ctx, req)
		expect.Must(t, err == nil, "SumCharges(%+v): %v", req, err)
		return page.Items
	}
	for name, tc := range map[string]struct {
		req  meter.SumChargesRequest
		want []meter.ChargeSum
	}{
		"the whole period": {meter.SumChargesRequest{}, []meter.ChargeSum{
			{UnitCode: unit, Charged: 11 * one, Refunded: one, Charges: 3},
		}},
		"by operation": {meter.SumChargesRequest{GroupBy: []meter.ChargeDimension{meter.ChargeDimensionOp}}, []meter.ChargeSum{
			{UnitCode: unit, Op: limes.OpGet, Charged: 8 * one, Charges: 2},
			{UnitCode: unit, Op: limes.OpPut, Charged: 3 * one, Refunded: one, Charges: 1},
		}},
		"by actor": {meter.SumChargesRequest{GroupBy: []meter.ChargeDimension{meter.ChargeDimensionActor}}, []meter.ChargeSum{
			{UnitCode: unit, Actor: "a", Charged: 8 * one, Charges: 2},
			{UnitCode: unit, Actor: "b", Charged: 3 * one, Refunded: one, Charges: 1},
		}},
		"by capability": {meter.SumChargesRequest{GroupBy: []meter.ChargeDimension{meter.ChargeDimensionCapability}}, []meter.ChargeSum{
			{UnitCode: unit, CapabilityID: f.root, Charged: 6 * one, Charges: 1},
			{UnitCode: unit, CapabilityID: f.child, Charged: 5 * one, Refunded: one, Charges: 2},
		}},
	} {
		got := sums(tc.req)
		expect.That(t, reflect.DeepEqual(got, tc.want), "%s: SumCharges = %+v, want %+v", name, got, tc.want)
	}
	// The top one, then the rest after its cursor.
	req := meter.SumChargesRequest{TenantID: f.Tenant, From: from, To: to, GroupBy: []meter.ChargeDimension{meter.ChargeDimensionCapability}, Limit: 1}
	first, err := f.Ledger.SumCharges(f.Ctx, req)
	expect.Must(t, !(err != nil || len(first.Items) != 1 || first.Items[0].CapabilityID != f.root || first.Next == ""), "the first page of one = %+v, %v; want the root and a cursor", first, err)
	req.Cursor = first.Next
	second, err := f.Ledger.SumCharges(f.Ctx, req)
	expect.That(t, !(err != nil || len(second.Items) != 1 || second.Items[0].CapabilityID != f.child || second.Next != ""), "the page after it = %+v, %v; want the child, and the last page", second, err)
	// A period that ends before the charges holds none of them.
	page, err := f.Ledger.SumCharges(f.Ctx, meter.SumChargesRequest{TenantID: f.Tenant, From: from, To: from.Add(time.Second)})
	expect.That(t, !(err != nil || len(page.Items) != 0), "SumCharges of a period before the charges = %+v, %v; want none", page, err)
	for name, bad := range map[string]meter.SumChargesRequest{
		"no tenant":                       {From: from, To: to},
		"a tenant and every tenant":       {TenantID: f.Tenant, AllTenants: true, From: from, To: to},
		"a tenant filter beside a tenant": {TenantID: f.Tenant, From: from, To: to, Filter: meter.ChargeFilter{Tenants: []uuid.UUID{f.Tenant}}},
		"a dimension named twice":         {TenantID: f.Tenant, From: from, To: to, GroupBy: []meter.ChargeDimension{meter.ChargeDimensionOp, meter.ChargeDimensionOp}},
		"no dimension":                    {TenantID: f.Tenant, From: from, To: to, GroupBy: []meter.ChargeDimension{meter.ChargeDimensionUnspecified}},
		"an unknown dimension":            {TenantID: f.Tenant, From: from, To: to, GroupBy: []meter.ChargeDimension{meter.ChargeDimension(math.MaxUint8)}},
		"a nil id in a filter":            {TenantID: f.Tenant, From: from, To: to, Filter: meter.ChargeFilter{Capabilities: []uuid.UUID{uuid.Nil()}}},
		"an empty actor in a filter":      {TenantID: f.Tenant, From: from, To: to, Filter: meter.ChargeFilter{Actors: []string{""}}},
		"a filter past its bound":         {TenantID: f.Tenant, From: from, To: to, Filter: meter.ChargeFilter{Actors: slices.Repeat([]string{"a"}, meter.MaxChargeFilterValues+1)}},
		"an empty period":                 {TenantID: f.Tenant, From: to, To: from},
		"no period":                       {TenantID: f.Tenant},
		"an unknown bucket":               {TenantID: f.Tenant, From: from, To: to, Bucket: meter.ChargeBucket(math.MaxUint8)},
		"a negative limit":                {TenantID: f.Tenant, From: from, To: to, Limit: -1},
		"a foreign cursor":                {TenantID: f.Tenant, From: from, To: to, Cursor: "not a cursor"},
	} {
		_, err := f.Ledger.SumCharges(f.Ctx, bad)
		expect.That(t, errors.Is(err, limes.ErrInvalidRequest), "SumCharges with %s: err = %v, want ErrInvalidRequest", name, err)
	}
	if f.ScopesTenants {
		_, err := f.Ledger.SumCharges(f.tenantCtx(other), meter.SumChargesRequest{TenantID: f.Tenant, From: from, To: to})
		expect.That(t, errors.Is(err, limes.ErrScopeMismatch), "SumCharges under another tenant's context: err = %v, want ErrScopeMismatch", err)
	}
}

// A sum across tenants is the sum of each tenant's: grouped by tenant it
// gives each one's on its own row, the largest first, without mixing units;
// a filter keeps the charges it names, of a tenant, an operation, an actor
// or a capability; a walk a row at a time yields what one page does; and a
// store that scopes calls to a tenant refuses it under one tenant's scope.
func checkSumChargesAcrossTenants(t *testing.T, f fixture) {
	if f.Ledger == nil {
		t.Skip("Env.Ledger not set")
	}
	other, err := f.newTenant()
	expect.Must(t, err == nil, "NewTenant: %v", err)
	theirCap, err := f.NewCapability(CapabilitySpec{Tenant: other, Requests: limes.RequestCeiling{Unlimited: true}, Budget: unlimitedBudget})
	expect.Must(t, err == nil, "capability of another tenant: %v", err)
	theirs := newSubject(subjectSpec{capID: theirCap, tenant: other})
	for _, c := range []meter.ChargeRequest{
		{Subject: theirs, Amount: 7 * one, Op: limes.OpGet, Actor: "c"},
		{Subject: theirs, Amount: 4 * one, Op: limes.OpPut, Actor: "a"},
	} {
		_, err := f.Usage.Charge(f.tenantCtx(other), c, nil)
		expect.Must(t, err == nil, "charge of another tenant: %v", err)
	}
	for _, c := range []meter.ChargeRequest{
		{Amount: 2 * one, Op: limes.OpGet, Actor: "a"},
		{Amount: 3 * one, Op: limes.OpPut, Actor: "b"},
	} {
		_, err := f.charge(c, nil)
		expect.Must(t, err == nil, "charge: %v", err)
	}
	from, to := f.sumPeriod()
	byTenant := []meter.ChargeDimension{meter.ChargeDimensionTenant}
	every := func(req meter.SumChargesRequest) []meter.ChargeSum {
		t.Helper()
		req.AllTenants, req.From, req.To = true, from, to
		page, err := f.Ledger.SumCharges(f.listCtx(), req)
		expect.Must(t, err == nil, "SumCharges(%+v) across tenants: %v", req, err)
		return page.Items
	}
	// Each tenant's sum on its own row, the largest first, as each one's
	// own request gives it.
	want := []meter.ChargeSum{
		{UnitCode: unit, TenantID: other, Charged: 11 * one, Charges: 2},
		{UnitCode: unit, TenantID: f.Tenant, Charged: 5 * one, Charges: 2},
	}
	got := every(meter.SumChargesRequest{GroupBy: byTenant})
	expect.That(t, reflect.DeepEqual(got, want), "SumCharges across tenants by tenant = %+v, want %+v", got, want)
	for _, w := range want {
		page, err := f.Ledger.SumCharges(f.tenantCtx(w.TenantID), meter.SumChargesRequest{TenantID: w.TenantID, From: from, To: to})
		expect.That(t, !(err != nil || len(page.Items) != 1 || page.Items[0].Charged != w.Charged || page.Items[0].Charges != w.Charges), "SumCharges of tenant %s = %+v, %v; want %s in %d charges, as across tenants", w.TenantID, page.Items, err, w.Charged, w.Charges)
	}
	for name, tc := range map[string]struct {
		req  meter.SumChargesRequest
		want []meter.ChargeSum
	}{
		"every tenant whole": {meter.SumChargesRequest{}, []meter.ChargeSum{
			{UnitCode: unit, Charged: 16 * one, Charges: 4},
		}},
		"a tenant filter": {meter.SumChargesRequest{GroupBy: byTenant, Filter: meter.ChargeFilter{Tenants: []uuid.UUID{other}}}, []meter.ChargeSum{
			{UnitCode: unit, TenantID: other, Charged: 11 * one, Charges: 2},
		}},
		"an operation filter": {meter.SumChargesRequest{GroupBy: byTenant, Filter: meter.ChargeFilter{Ops: []limes.Op{limes.OpGet}}}, []meter.ChargeSum{
			{UnitCode: unit, TenantID: other, Charged: 7 * one, Charges: 1},
			{UnitCode: unit, TenantID: f.Tenant, Charged: 2 * one, Charges: 1},
		}},
		"an actor filter, by actor": {meter.SumChargesRequest{GroupBy: []meter.ChargeDimension{meter.ChargeDimensionActor}, Filter: meter.ChargeFilter{Actors: []string{"a"}}}, []meter.ChargeSum{
			{UnitCode: unit, Actor: "a", Charged: 6 * one, Charges: 2},
		}},
		"a capability filter": {meter.SumChargesRequest{GroupBy: byTenant, Filter: meter.ChargeFilter{Capabilities: []uuid.UUID{theirCap}}}, []meter.ChargeSum{
			{UnitCode: unit, TenantID: other, Charged: 11 * one, Charges: 2},
		}},
	} {
		got := every(tc.req)
		expect.That(t, reflect.DeepEqual(got, tc.want), "%s: SumCharges across tenants = %+v, want %+v", name, got, tc.want)
	}
	// A row at a time, across tenants and their operations, with two rows
	// that differ by tenant alone: the tenants' puts, four each.
	_, err = f.charge(meter.ChargeRequest{Amount: one, Op: limes.OpPut, Actor: "b"}, nil)
	expect.Must(t, err == nil, "charge: %v", err)
	req := meter.SumChargesRequest{AllTenants: true, From: from, To: to, Limit: 1,
		GroupBy: []meter.ChargeDimension{meter.ChargeDimensionOp, meter.ChargeDimensionTenant}}
	var walked []meter.ChargeSum
	for sum, err := range meter.AllChargeSums(f.listCtx(), f.Ledger, req) {
		expect.Must(t, err == nil, "AllChargeSums across tenants: %v", err)
		walked = append(walked, sum)
	}
	whole := every(meter.SumChargesRequest{GroupBy: req.GroupBy})
	expect.That(t, !(len(whole) != len(want)*2 || !reflect.DeepEqual(walked, whole)), "AllChargeSums a row at a time = %+v, want the page %+v", walked, whole)
	if f.ScopesTenants {
		_, err := f.Ledger.SumCharges(f.tenantCtx(f.Tenant), meter.SumChargesRequest{AllTenants: true, From: from, To: to})
		expect.That(t, errors.Is(err, limes.ErrScopeMismatch), "SumCharges across tenants under one tenant's context: err = %v, want ErrScopeMismatch", err)
	}
}

// SumCharges orders sums by amount, not by their digits: sums of different
// lengths come largest first, whole and within each bucket, and a walk one
// row at a time yields what one page does. A refund stays in its charge's
// group throughout.
func checkSumChargesOrderByAmount(t *testing.T, f fixture) {
	if f.Ledger == nil {
		t.Skip("Env.Ledger not set")
	}
	// The charges outgrow the fixture's ceilings: an unlimited capability
	// under an unlimited tenant.
	b, err := f.Usage.GetTenantBudget(f.Ctx, f.Tenant)
	expect.Must(t, err == nil, "tenant budget: %v", err)
	_, err = f.Usage.SetTenantBudget(f.Ctx, meter.SetTenantBudgetRequest{
		TenantID: f.Tenant, Ceiling: unlimitedBudget, ExpectedVersion: b.ResourceVersion,
	})
	expect.Must(t, err == nil, "tenant budget: %v", err)
	capID, err := f.NewCapability(CapabilitySpec{Requests: limes.RequestCeiling{Unlimited: true}, Budget: unlimitedBudget})
	expect.Must(t, err == nil, "capability: %v", err)
	subject := newSubject(subjectSpec{capID: capID, tenant: f.Tenant})
	charge := func(actor string, amount, refund limes.Nanos) {
		t.Helper()
		r, err := f.charge(meter.ChargeRequest{Subject: subject, Amount: amount, Actor: actor}, nil)
		expect.Must(t, err == nil, "charge of %s by %s: %v", amount, actor, err)
		if refund == 0 {
			return
		}
		_, err = f.Usage.Refund(f.Ctx, meter.RefundRequest{ChargeID: r.ChargeID, Amount: refund})
		expect.Must(t, err == nil, "refund: %v", err)
	}
	from, to := f.sumPeriod()
	walk := func(req meter.SumChargesRequest, want []meter.ChargeSum) {
		t.Helper()
		req.TenantID, req.From, req.To, req.GroupBy = f.Tenant, from, to, []meter.ChargeDimension{meter.ChargeDimensionActor}
		page, err := f.Ledger.SumCharges(f.Ctx, req)
		expect.That(t, !(err != nil || page.Next != "" || !sameSums(page.Items, want)), "SumCharges by %s = %+v, %q, %v; want %+v on one page", req.Bucket, page.Items, page.Next, err, want)
		req.Limit = 1
		var walked []meter.ChargeSum
		for range len(want) + 1 {
			page, err := f.Ledger.SumCharges(f.Ctx, req)
			expect.Must(t, err == nil, "SumCharges by %s, a page of one: %v", req.Bucket, err)
			walked = append(walked, page.Items...)
			if req.Cursor = page.Next; req.Cursor == "" {
				break
			}
		}
		expect.That(t, sameSums(walked, want), "SumCharges by %s a row at a time = %+v, want %+v", req.Bucket, walked, want)
	}
	// Sums of nine, ten and eleven digits in nanos, whose text order is the
	// reverse of their order by amount.
	charge("x", 12*one, one)
	charge("y", 2*one, 0)
	charge("z", one/2, 0)
	walk(meter.SumChargesRequest{}, []meter.ChargeSum{
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
	walk(meter.SumChargesRequest{}, []meter.ChargeSum{
		{UnitCode: unit, Actor: "z", Charged: 100*one + one/2, Charges: 2},
		{UnitCode: unit, Actor: "x", Charged: 19 * one, Refunded: one, Charges: 2},
		{UnitCode: unit, Actor: "y", Charged: 2*one + 9*cent, Charges: 2},
	})
	walk(meter.SumChargesRequest{Bucket: meter.ChargeBucketDay}, []meter.ChargeSum{
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
// one at midnight in the next. No bucket sums them together.
func checkSumChargeBuckets(t *testing.T, f fixture) {
	if f.Ledger == nil || f.Now == nil || f.Advance == nil {
		t.Skip("Env.Ledger, Now or Advance not set")
	}
	const tick = time.Microsecond
	from := f.Now()
	y, m, d := from.UTC().Date()
	midnight := time.Date(y, m, d+1, 0, 0, 0, 0, time.UTC)
	f.Advance(midnight.Sub(from) - tick)
	_, err := f.charge(meter.ChargeRequest{Amount: 2 * one}, nil)
	expect.Must(t, err == nil, "charge before midnight: %v", err)
	f.Advance(tick)
	_, err = f.charge(meter.ChargeRequest{Amount: 3 * one}, nil)
	expect.Must(t, err == nil, "charge at midnight: %v", err)
	to := midnight.Add(time.Second)
	for b, before := range map[meter.ChargeBucket]time.Time{
		meter.ChargeBucketHour: midnight.Add(-time.Hour),
		meter.ChargeBucketDay:  midnight.AddDate(0, 0, -1),
	} {
		page, err := f.Ledger.SumCharges(f.Ctx, meter.SumChargesRequest{TenantID: f.Tenant, From: from, To: to, Bucket: b})
		want := []meter.ChargeSum{
			{BucketStart: before, UnitCode: unit, Charged: 2 * one, Charges: 1},
			{BucketStart: midnight, UnitCode: unit, Charged: 3 * one, Charges: 1},
		}
		expect.That(t, !(err != nil || !sameSums(page.Items, want)), "SumCharges by %s = %+v, %v; want %+v", b, page.Items, err, want)
	}
	// No bucket, named or left zero, sums the period whole.
	for _, b := range []meter.ChargeBucket{meter.ChargeBucketNone, 0} {
		page, err := f.Ledger.SumCharges(f.Ctx, meter.SumChargesRequest{TenantID: f.Tenant, From: from, To: to, Bucket: b})
		want := []meter.ChargeSum{{UnitCode: unit, Charged: 5 * one, Charges: 2}}
		expect.That(t, !(err != nil || !sameSums(page.Items, want)), "SumCharges by %s = %+v, %v; want %+v", b, page.Items, err, want)
	}
	page, err := f.Ledger.SumCharges(f.Ctx, meter.SumChargesRequest{TenantID: f.Tenant, From: from, To: to, Bucket: meter.ChargeBucketMonth})
	months := map[time.Time]bool{}
	for _, s := range page.Items {
		months[s.BucketStart] = true
	}
	want := map[time.Time]bool{
		time.Date(y, m, 1, 0, 0, 0, 0, time.UTC):                              true,
		time.Date(midnight.Year(), midnight.Month(), 1, 0, 0, 0, 0, time.UTC): true,
	}
	expect.That(t, !(err != nil || !reflect.DeepEqual(months, want)), "SumCharges by month = %+v, %v; want the months %v", page.Items, err, want)
}

// A week bucket is an ISO week: a charge a microsecond before Monday 00:00
// UTC falls in the week before, one at it in the week it starts.
func checkSumChargeWeeks(t *testing.T, f fixture) {
	if f.Ledger == nil || f.Now == nil || f.Advance == nil {
		t.Skip("Env.Ledger, Now or Advance not set")
	}
	const tick = time.Microsecond
	from := f.Now()
	y, m, d := from.UTC().Date()
	monday := time.Date(y, m, d+1, 0, 0, 0, 0, time.UTC)
	for monday.Weekday() != time.Monday {
		monday = monday.AddDate(0, 0, 1)
	}
	f.Advance(monday.Sub(from) - tick)
	_, err := f.charge(meter.ChargeRequest{Amount: 2 * one}, nil)
	expect.Must(t, err == nil, "charge before Monday: %v", err)
	f.Advance(tick)
	_, err = f.charge(meter.ChargeRequest{Amount: 3 * one}, nil)
	expect.Must(t, err == nil, "charge at Monday: %v", err)
	page, err := f.Ledger.SumCharges(f.Ctx, meter.SumChargesRequest{TenantID: f.Tenant, From: from, To: monday.Add(time.Second), Bucket: meter.ChargeBucketWeek})
	want := []meter.ChargeSum{
		{BucketStart: monday.AddDate(0, 0, -7), UnitCode: unit, Charged: 2 * one, Charges: 1},
		{BucketStart: monday, UnitCode: unit, Charged: 3 * one, Charges: 1},
	}
	expect.That(t, !(err != nil || !sameSums(page.Items, want)), "SumCharges by week = %+v, %v; want %+v", page.Items, err, want)
}

// sameSums compares sums, their bucket starts as instants.
// The period is [From, To): a charge taken at From is in it, and one taken
// at To is not.
func checkSumChargesPeriodEdges(t *testing.T, f fixture) {
	if f.Ledger == nil || f.Now == nil || f.Advance == nil {
		t.Skip("Env.Ledger, Now or Advance not set")
	}
	// A whole second, which every store keeps exactly.
	now := f.Now()
	f.Advance(now.Truncate(time.Second).Add(time.Second).Sub(now))
	at := f.Now()
	_, err := f.charge(meter.ChargeRequest{Amount: 2 * one}, nil)
	expect.Must(t, err == nil, "charge: %v", err)
	for name, tc := range map[string]struct {
		from, to time.Time
		want     []meter.ChargeSum
	}{
		"From at the charge": {at, at.Add(time.Microsecond), []meter.ChargeSum{{UnitCode: unit, Charged: 2 * one, Charges: 1}}},
		"To at the charge":   {at.Add(-time.Second), at, nil},
	} {
		page, err := f.Ledger.SumCharges(f.Ctx, meter.SumChargesRequest{TenantID: f.Tenant, From: tc.from, To: tc.to})
		expect.That(t, !(err != nil || !sameSums(page.Items, tc.want)), "%s: SumCharges = %+v, %v; want %+v", name, page.Items, err, tc.want)
	}
}

// A refund made once its charge's bucket has closed still counts in that
// bucket, and opens none of its own in the bucket it was made in: a closed
// period's sums still move.
func checkSumChargesLateRefund(t *testing.T, f fixture) {
	if f.Ledger == nil || f.Now == nil || f.Advance == nil {
		t.Skip("Env.Ledger, Now or Advance not set")
	}
	from := f.Now()
	y, m, d := from.UTC().Date()
	midnight := time.Date(y, m, d+1, 0, 0, 0, 0, time.UTC)
	r, err := f.charge(meter.ChargeRequest{Amount: 3 * one}, nil)
	expect.Must(t, err == nil, "charge: %v", err)
	f.Advance(midnight.Sub(from))
	_, err = f.Usage.Refund(f.Ctx, meter.RefundRequest{ChargeID: r.ChargeID, Amount: one})
	expect.Must(t, err == nil, "refund the next day: %v", err)
	page, err := f.Ledger.SumCharges(f.Ctx, meter.SumChargesRequest{
		TenantID: f.Tenant, From: from, To: midnight.Add(time.Second), Bucket: meter.ChargeBucketDay,
	})
	want := []meter.ChargeSum{{BucketStart: midnight.AddDate(0, 0, -1), UnitCode: unit, Charged: 3 * one, Refunded: one, Charges: 1}}
	expect.That(t, !(err != nil || !sameSums(page.Items, want)), "SumCharges by day = %+v, %v; want %+v", page.Items, err, want)
}

// A sum past MaxNanos is ErrCounterOverflow, never a wrapped or clipped
// total: two charges of MaxNanos, the tenant's spend reset between them, sum
// to twice what a counter holds, while each capability's group holds one.
func checkSumChargesOverflow(t *testing.T, f fixture) {
	if f.Ledger == nil {
		t.Skip("Env.Ledger not set")
	}
	const charges = 2
	for range charges {
		b, err := f.Usage.GetTenantBudget(f.Ctx, f.Tenant)
		expect.Must(t, err == nil, "tenant budget: %v", err)
		_, err = f.Usage.SetTenantBudget(f.Ctx, meter.SetTenantBudgetRequest{
			TenantID: f.Tenant, Ceiling: unlimitedBudget, ResetSpend: true, ExpectedVersion: b.ResourceVersion,
		})
		expect.Must(t, err == nil, "tenant budget: %v", err)
		capID, err := f.NewCapability(CapabilitySpec{Requests: limes.RequestCeiling{Unlimited: true}, Budget: unlimitedBudget})
		expect.Must(t, err == nil, "capability: %v", err)
		_, err = f.charge(meter.ChargeRequest{Subject: newSubject(subjectSpec{capID: capID, tenant: f.Tenant}), Amount: limes.MaxNanos}, nil)
		expect.Must(t, err == nil, "charge of MaxNanos: %v", err)
	}
	from, to := f.sumPeriod()
	req := meter.SumChargesRequest{TenantID: f.Tenant, From: from, To: to}
	page, err := f.Ledger.SumCharges(f.Ctx, req)
	expect.That(t, errors.Is(err, meter.ErrCounterOverflow), "SumCharges of the whole period = %+v, %v; want ErrCounterOverflow", page.Items, err)
	req.GroupBy = []meter.ChargeDimension{meter.ChargeDimensionCapability}
	page, err = f.Ledger.SumCharges(f.Ctx, req)
	expect.Must(t, !(err != nil || len(page.Items) != charges), "SumCharges by capability = %+v, %v; want %d sums", page.Items, err, charges)
	for _, s := range page.Items {
		expect.That(t, s.Charged == limes.MaxNanos, "a capability's sum = %+v, want %v charged", s, limes.MaxNanos)
	}
}

// GetTenantBudget of a tenant with no row — no ceiling set, nothing charged
// or held — is ErrTenantBudgetNotFound, which matches ErrNotFound.
func checkTenantBudgetNotFound(t *testing.T, f fixture) {
	other, err := f.newTenant()
	expect.Must(t, err == nil, "NewTenant: %v", err)
	b, err := f.Usage.GetTenantBudget(f.tenantCtx(other), other)
	expect.That(t, !(!errors.Is(err, meter.ErrTenantBudgetNotFound) || !errors.Is(err, limes.ErrNotFound)), "GetTenantBudget of a tenant with no row = %+v, %v; want ErrTenantBudgetNotFound", b, err)
}

func sameSums(got, want []meter.ChargeSum) bool {
	return slices.EqualFunc(got, want, func(a, b meter.ChargeSum) bool {
		a.BucketStart, b.BucketStart = a.BucketStart.UTC(), b.BucketStart.UTC()
		return reflect.DeepEqual(a, b)
	})
}

// purgeStep is how far apart in time checkPurgeCharges takes its charges, so
// each has an age of its own.
const purgeStep = time.Second

// checkPurgeCharges holds a ledger that purges to its retention: a charge
// exactly as old stays, older ones go oldest first in batches of the limit,
// a younger one stays, and each removed charge takes its promises with it —
// read, found by reference, refunded, its hold settled again, summed. It
// needs Env.Ledger to be a meter.LedgerHousekeeping, and Env.Advance and
// Env.Now.
func checkPurgeCharges(t *testing.T, f fixture) {
	housekeeping, ok := f.Ledger.(meter.LedgerHousekeeping)
	if !ok || f.Advance == nil || f.Now == nil {
		t.Skip("Env.Ledger is no meter.LedgerHousekeeping, or Env.Advance or Env.Now is not set")
	}
	for name, bad := range map[string]meter.PurgeChargesRequest{
		"a retention under the floor": {OlderThan: meter.MinChargeRetention - purgeStep},
		"a negative retention":        {OlderThan: -purgeStep},
		"a negative limit":            {Limit: -1},
	} {
		_, err := housekeeping.PurgeCharges(f.Ctx, bad)
		expect.That(t, errors.Is(err, limes.ErrInvalidRequest), "PurgeCharges with %s: err = %v, want ErrInvalidRequest", name, err)
	}
	from := f.Now()
	// Another tenant's charge, as old as the first: a purge under Tenant's
	// scope does not reach it.
	var otherCharge uuid.UUID
	var otherCtx context.Context
	if f.ScopesTenants {
		other, err := f.newTenant()
		expect.Must(t, err == nil, "%v", err)
		otherCap, err := f.NewCapability(CapabilitySpec{Tenant: other, Requests: limes.RequestCeiling{Unlimited: true}, Budget: limes.BudgetCeiling{Unlimited: true, UnitCode: unit}})
		expect.Must(t, err == nil, "%v", err)
		otherCtx = f.tenantCtx(other)
		r, err := f.Usage.Charge(otherCtx, meter.ChargeRequest{Subject: newSubject(subjectSpec{capID: otherCap, tenant: other, unit: unit}), Amount: one}, nil)
		expect.Must(t, err == nil, "%v", err)
		otherCharge = r.ChargeID
	}
	first, err := f.charge(meter.ChargeRequest{Subject: f.childSubject(), Amount: one, ExternalRef: "purge-1"}, nil)
	expect.Must(t, err == nil, "%v", err)
	f.Advance(purgeStep)
	second, err := f.charge(meter.ChargeRequest{Subject: f.childSubject(), Amount: one, ExternalRef: "purge-2"}, nil)
	expect.Must(t, err == nil, "%v", err)
	f.Advance(purgeStep)
	hold, err := f.Usage.Reserve(f.Ctx, meter.ReserveRequest{Subject: f.childSubject(), Amount: one})
	expect.Must(t, err == nil, "%v", err)
	settle := meter.SettleRequest{Subject: f.childSubject(), ReservationID: hold.ID, Amount: one}
	settled, err := f.Usage.Settle(f.Ctx, settle, nil)
	expect.Must(t, err == nil, "%v", err)
	purge := func(req meter.PurgeChargesRequest) int64 {
		t.Helper()
		r, err := housekeeping.PurgeCharges(f.Ctx, req)
		expect.Must(t, err == nil, "PurgeCharges(%+v): %v", req, err)
		return r.Purged
	}
	kept := func(what string, id uuid.UUID, want bool) {
		t.Helper()
		_, err := f.Usage.GetCharge(f.Ctx, id)
		got := err == nil
		expect.That(t, !(got != want || (!want && !errors.Is(err, meter.ErrChargeNotFound))), "%s: GetCharge = %v; want kept %v, or ErrChargeNotFound", what, err, want)
	}

	// The first charge is exactly the retention old: it stays.
	f.Advance(meter.MinChargeRetention - 2*purgeStep)
	n := purge(meter.PurgeChargesRequest{})
	expect.That(t, n == 0, "a charge exactly the retention old: %d purged, want none", n)
	// Two are older now, the settled one is not; a batch of one takes the
	// oldest.
	f.Advance(purgeStep + purgeStep/2)
	n = purge(meter.PurgeChargesRequest{Limit: 1})
	expect.Must(t, n == 1, "a batch of one purged %d", n)
	kept("the oldest charge", first.ChargeID, false)
	kept("the second charge", second.ChargeID, true)
	n = purge(meter.PurgeChargesRequest{})
	expect.That(t, n == 1, "the next batch purged %d, want the second charge alone", n)
	n = purge(meter.PurgeChargesRequest{})
	expect.That(t, n == 0, "a batch with nothing older purged %d", n)
	kept("the settled charge, younger than the retention", settled.ChargeID, true)
	got, err := f.Usage.Settle(f.Ctx, settle, nil)
	expect.That(t, !(err != nil || !got.Replayed), "a settle replayed while its charge is kept = %+v, %v; want the receipt, replayed", got, err)

	// A removed charge's promises are gone with it.
	_, err = f.Usage.ChargeByRef(f.Ctx, meter.ChargeByRefRequest{CapabilityID: f.child, ExternalRef: "purge-1"})
	expect.That(t, errors.Is(err, meter.ErrChargeNotFound), "ChargeByRef of a purged charge: err = %v, want ErrChargeNotFound", err)
	// Its reference charges anew: a charge of its own, no replay.
	again, err := f.charge(meter.ChargeRequest{Subject: f.childSubject(), Amount: one, ExternalRef: "purge-1"}, nil)
	expect.That(t, !(err != nil || again.Replayed || again.ChargeID == first.ChargeID), "the purged charge's reference again = %+v, %v; want a new charge, not replayed", again, err)
	if f.ScopesTenants {
		_, err := f.Usage.GetCharge(otherCtx, otherCharge)
		expect.That(t, err == nil, "another tenant's charge after a purge under Tenant's scope: %v; want it kept", err)
	}
	_, err = f.Usage.Refund(f.Ctx, meter.RefundRequest{ChargeID: second.ChargeID, Remaining: true})
	expect.That(t, errors.Is(err, meter.ErrChargeNotFound), "Refund of a purged charge: err = %v, want ErrChargeNotFound", err)
	f.Advance(purgeStep)
	n = purge(meter.PurgeChargesRequest{})
	expect.That(t, n == 1, "the settled charge, past the retention now: %d purged, want 1", n)
	_, err = f.Usage.Settle(f.Ctx, settle, nil)
	expect.That(t, errors.Is(err, meter.ErrReservationNotFound), "a settle of a hold whose charge is purged: err = %v, want ErrReservationNotFound", err)
	page, err := f.Ledger.SumCharges(f.Ctx, meter.SumChargesRequest{TenantID: f.Tenant, From: from, To: from.Add(3 * purgeStep)})
	expect.That(t, !(err != nil || len(page.Items) != 0), "SumCharges of the purged charges' period = %+v, %v; want nothing", page.Items, err)
}
