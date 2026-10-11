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
// again: rejection that moves nothing — by the capability's, an ancestor's or
// the tenant's ceiling, by a failed onCharged, under concurrent charges —
// idempotency on ExternalRef and on a settled reservation, ChargeByRef,
// OverrunRecord past every ceiling, refunds, holds and their expiry, request
// counts, units, the tenant aggregate and its listing, and the validation of
// the request. They need a real ledger, not a fake.
package metertest

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes"
)

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
	// NewCapability records a capability of Tenant with the given parent
	// (uuid.Nil for a root) and budget, so the Meter finds its lineage and
	// its ceiling, and returns its id.
	NewCapability func(parent uuid.UUID, maxBudget limes.Nanos) (uuid.UUID, error)
	// Advance moves the store's clock forward by d. Optional: without it,
	// the checks of reservation expiry are skipped.
	Advance func(d time.Duration)
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
	if f.root, err = env.NewCapability(uuid.Nil, rootBudget); err != nil {
		t.Fatalf("root capability: %v", err)
	}
	if f.child, err = env.NewCapability(f.root, childBudget); err != nil {
		t.Fatalf("child capability: %v", err)
	}
	if _, err := env.Usage.SetTenantBudget(env.Ctx, limes.SetTenantBudgetRequest{
		TenantID: env.Tenant, MaxBudgetAmount: tenantBudget, UnitCode: unit,
	}); err != nil {
		t.Fatalf("tenant budget: %v", err)
	}
	return f
}

func (f fixture[TX]) charge(req limes.ChargeRequest, onCharged func(context.Context, TX) error) (limes.ChargeReceipt, error) {
	if req.CapabilityID == uuid.Nil {
		req.CapabilityID = f.child
	}
	req.TenantID, req.UnitCode = f.Tenant, unit
	if req.MaxBudget == 0 {
		req.MaxBudget = childBudget
	}
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
		{"RefundOfAClosedPeriodLeavesTheTenantPeriod", checkRefundAcrossReset[TX]},
		{"EveryCounterKeepsOneUnit", checkOneUnit[TX]},
		{"DeleteReleasesHolds", checkDeleteReleasesHolds[TX]},
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
		{"TenantBudgetReadsAgree", checkTenantBudgetReads[TX]},
		{"ReservationsReadBack", checkReservations[TX]},
		{"SumsExactly", checkSumsExactly[TX]},
	}
	for _, c := range checks {
		t.Run(c.name, func(t *testing.T) { c.run(t, newFixture(t, setup)) })
	}
	// Checks that need the tenant as setup leaves it, with no ceiling yet.
	bare := []struct {
		name string
		run  func(*testing.T, Env[TX])
	}{
		{"TenantAggregateCountsBeforeACeiling", checkTenantCountsBeforeACeiling[TX]},
	}
	for _, c := range bare {
		t.Run(c.name, func(t *testing.T) { c.run(t, setup(t)) })
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
	id, err := env.NewCapability(uuid.Nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.Usage.Charge(env.Ctx, limes.ChargeRequest{
		CapabilityID: id, TenantID: env.Tenant, Amount: spent, UnitCode: unit,
	}, nil); err != nil {
		t.Fatalf("charge: %v", err)
	}
	reserve := func() error {
		_, err := env.Usage.Reserve(env.Ctx, limes.ReserveRequest{
			CapabilityID: id, TenantID: env.Tenant, Amount: held, UnitCode: unit,
		})
		return err
	}
	if err := reserve(); err != nil {
		t.Fatalf("reserve: %v", err)
	}
	b, err := env.Usage.GetTenantBudget(env.Ctx, env.Tenant)
	if err != nil || b.SpentAmount != spent || b.ReservedAmount != held || b.MaxBudgetAmount != 0 || b.ResourceVersion != 0 {
		t.Fatalf("GetTenantBudget before any ceiling = %+v, %v; want %v spent, %v held, no ceiling, version 0", b, err, spent, held)
	}
	set, err := env.Usage.SetTenantBudget(env.Ctx, limes.SetTenantBudgetRequest{
		TenantID: env.Tenant, MaxBudgetAmount: ceiling, UnitCode: unit,
	})
	if err != nil || set.SpentAmount != spent || set.ReservedAmount != held {
		t.Fatalf("SetTenantBudget = %+v, %v; want the ceiling over %v spent and %v held", set, err, spent, held)
	}
	if err := reserve(); !errors.Is(err, limes.ErrTenantBudgetExceeded) {
		t.Errorf("a hold past the new ceiling: err = %v, want ErrTenantBudgetExceeded", err)
	}
	if _, err := env.Usage.Charge(env.Ctx, limes.ChargeRequest{
		CapabilityID: id, TenantID: env.Tenant, Amount: one, UnitCode: unit,
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
	if err != nil || !again.Replayed || again.ChargeID != first.ChargeID || again.Spent != 2*one {
		t.Fatalf("repeated charge = %+v, %v; want a replay of %s at spend 2", again, err, first.ChargeID)
	}
	// The same name for a different cost is the caller's bug; replaying the
	// first receipt would hide it.
	if r, err := f.charge(limes.ChargeRequest{Amount: 5 * one, ExternalRef: "call-1"}, onCharged); !errors.Is(err, limes.ErrExternalRefConflict) {
		t.Fatalf("the ref again for another amount = %+v, %v; want ErrExternalRefConflict", r, err)
	}
	if fanOuts != 1 {
		t.Errorf("onCharged ran %d times, want once", fanOuts)
	}
	// The name is per capability, and no name never deduplicates.
	if r, err := f.charge(limes.ChargeRequest{CapabilityID: f.root, MaxBudget: rootBudget, Amount: one, ExternalRef: "call-1"}, nil); err != nil || r.Replayed {
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
	if _, err := f.Usage.ChargeByRef(f.Ctx, f.child, "call-1"); !errors.Is(err, limes.ErrChargeNotFound) {
		t.Fatalf("before any charge: err = %v, want ErrChargeNotFound", err)
	}
	r, err := f.charge(limes.ChargeRequest{Amount: 3 * one, ExternalRef: "call-1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Usage.Refund(f.Ctx, limes.RefundRequest{ChargeID: r.ChargeID, Amount: one}); err != nil {
		t.Fatal(err)
	}
	got, err := f.Usage.ChargeByRef(f.Ctx, f.child, "call-1")
	if err != nil || got.ChargeID != r.ChargeID || got.CapabilityID != f.child || got.Amount != 3*one ||
		got.Refunded != one || got.ExternalRef != "call-1" || got.UnitCode != unit || got.Overrun {
		t.Fatalf("ChargeByRef = %+v, %v; want charge %s of 3 with 1 refunded", got, err, r.ChargeID)
	}
	if byID, err := f.Usage.GetCharge(f.Ctx, r.ChargeID); err != nil || byID != got {
		t.Errorf("GetCharge = %+v, %v; want what ChargeByRef read, %+v", byID, err, got)
	}
	if _, err := f.Usage.GetCharge(f.Ctx, uuid.New()); !errors.Is(err, limes.ErrChargeNotFound) {
		t.Errorf("GetCharge of an unknown id: err = %v, want ErrChargeNotFound", err)
	}
	if _, err := f.Usage.ChargeByRef(f.Ctx, f.root, "call-1"); !errors.Is(err, limes.ErrChargeNotFound) {
		t.Errorf("the ref on another capability: err = %v, want ErrChargeNotFound", err)
	}
	if _, err := f.Usage.ChargeByRef(f.Ctx, f.child, ""); !errors.Is(err, limes.ErrChargeNotFound) {
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
		ReservationID: uuid.New(), Amount: one, Overrun: limes.OverrunRecord + 1,
	}, nil); !errors.Is(err, limes.ErrInvalidRequest) {
		t.Errorf("settle with an unknown overrun: err = %v, want ErrInvalidRequest", err)
	}
	if _, err := f.Usage.Reserve(f.Ctx, limes.ReserveRequest{
		CapabilityID: f.child, TenantID: f.Tenant, Amount: one, MaxBudget: childBudget, UnitCode: unit,
		TTL: -time.Minute,
	}); !errors.Is(err, limes.ErrInvalidRequest) {
		t.Errorf("reserve with a negative TTL: err = %v, want ErrInvalidRequest", err)
	}
	if f.spent(t, f.child) != 0 {
		t.Error("a refused request moved spend")
	}
}

func checkOverrunRecord[TX any](t *testing.T, f fixture[TX]) {
	// 40 crosses the child's 20, the root's 25 and the tenant's 30.
	const amount = 40 * one
	if _, err := f.charge(limes.ChargeRequest{Amount: amount}, nil); err == nil {
		t.Fatal("a rejecting charge past every ceiling was accepted")
	}
	r, err := f.charge(limes.ChargeRequest{Amount: amount, Overrun: limes.OverrunRecord}, nil)
	if err != nil || !r.Overrun || r.Replayed || r.Spent != amount {
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
		CapabilityID: f.child, TenantID: f.Tenant, Amount: cent, MaxBudget: childBudget, UnitCode: unit,
	}); !errors.Is(err, limes.ErrBudgetExceeded) {
		t.Errorf("after an overrun a reservation: err = %v, want ErrBudgetExceeded", err)
	}
}

func checkSettle[TX any](t *testing.T, f fixture[TX]) {
	res, err := f.Usage.Reserve(f.Ctx, limes.ReserveRequest{
		CapabilityID: f.child, TenantID: f.Tenant, Amount: 10 * one, MaxBudget: childBudget, UnitCode: unit,
	})
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	settle := func(amount limes.Nanos, p limes.OverrunPolicy) (limes.ChargeReceipt, error) {
		return f.Usage.Settle(f.Ctx, limes.SettleRequest{
			ReservationID: res.ID, Amount: amount, MaxBudget: childBudget, Overrun: p,
		}, nil)
	}
	if _, err := settle(24*one, limes.OverrunReject); !errors.Is(err, limes.ErrBudgetExceeded) {
		t.Fatalf("rejecting settle past the ceiling: err = %v, want ErrBudgetExceeded", err)
	}
	r, err := settle(24*one, limes.OverrunRecord)
	if err != nil || !r.Overrun || r.Spent != 24*one {
		t.Fatalf("recording settle = %+v, %v; want 24 spent, overrun", r, err)
	}
	if u, err := f.Usage.GetUsage(f.Ctx, f.root); err != nil || u.ReservedAmount != 0 {
		t.Errorf("root after settling = %+v, %v; want nothing held", u, err)
	}
	again, err := settle(one, limes.OverrunReject)
	if err != nil || !again.Replayed || !again.Overrun || again.ChargeID != r.ChargeID || again.Spent != 24*one {
		t.Errorf("settling again = %+v, %v; want a replay of %s", again, err, r.ChargeID)
	}
	if err := f.Usage.Release(f.Ctx, res.ID); err != nil {
		t.Errorf("releasing a settled reservation: %v", err)
	}
	other, err := f.Usage.Reserve(f.Ctx, limes.ReserveRequest{
		CapabilityID: f.root, TenantID: f.Tenant, Amount: 0, MaxBudget: rootBudget, UnitCode: unit,
	})
	if err != nil {
		t.Fatalf("reserve nothing: %v", err)
	}
	if err := f.Usage.Release(f.Ctx, other.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Usage.Settle(f.Ctx, limes.SettleRequest{ReservationID: other.ID, Amount: 0}, nil); !errors.Is(err, limes.ErrReservationNotFound) {
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
	r, err := f.Usage.Settle(f.Ctx, limes.SettleRequest{ReservationID: uuid.Nil, Amount: one, MaxBudget: childBudget}, nil)
	if !errors.Is(err, limes.ErrReservationNotFound) {
		t.Fatalf("Settle of the zero reservation id = %+v, %v; want ErrReservationNotFound", r, err)
	}
	if got := f.spent(t, f.child); got != one {
		t.Errorf("child spent %v after a refused settle, want 1", got)
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
		TenantID: f.Tenant, MaxBudgetAmount: tenantBudget, UnitCode: unit,
		ResetSpend: true, ExpectedVersion: b.ResourceVersion,
	}); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if _, err := f.charge(limes.ChargeRequest{Amount: amount}, nil); err != nil {
		t.Fatal(err)
	}
	if r, err := f.Usage.Refund(f.Ctx, limes.RefundRequest{ChargeID: before.ChargeID}); err != nil || r.Refunded != amount {
		t.Fatalf("refund of the earlier period's charge = %+v, %v; want %v", r, err, amount)
	}
	if got := f.tenantSpent(t); got != amount {
		t.Errorf("tenant spent %v in the new period, want %v: the refund came out of a period that never counted it", got, amount)
	}
	if got := f.spent(t, f.child); got != amount {
		t.Errorf("child spent %v, want %v: the refund still returns to the capability", got, amount)
	}
}

// checkOneUnit holds a store to counting each counter in one unit: an amount
// in another is not comparable with what is there, and adding it would sum
// two currencies as one.
func checkOneUnit[TX any](t *testing.T, f fixture[TX]) {
	if _, err := f.charge(limes.ChargeRequest{Amount: one}, nil); err != nil {
		t.Fatal(err)
	}
	other := limes.ChargeRequest{CapabilityID: f.child, TenantID: f.Tenant, Amount: one, MaxBudget: childBudget, UnitCode: otherUnit}
	if _, err := f.Usage.Charge(f.Ctx, other, nil); !errors.Is(err, limes.ErrUnitCodeMismatch) {
		t.Errorf("a charge in %s on a %s counter: err = %v, want ErrUnitCodeMismatch", otherUnit, unit, err)
	}
	if _, err := f.Usage.Reserve(f.Ctx, limes.ReserveRequest{
		CapabilityID: f.child, TenantID: f.Tenant, Amount: one, MaxBudget: childBudget, UnitCode: otherUnit,
	}); !errors.Is(err, limes.ErrUnitCodeMismatch) {
		t.Errorf("a hold in %s on a %s counter: err = %v, want ErrUnitCodeMismatch", otherUnit, unit, err)
	}
	// A fresh capability has no unit of its own yet, but its tenant does.
	fresh, err := f.NewCapability(uuid.Nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	other.CapabilityID, other.MaxBudget = fresh, 0
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
		TenantID: f.Tenant, MaxBudgetAmount: tenantBudget, UnitCode: otherUnit, ExpectedVersion: b.ResourceVersion,
	}); !errors.Is(err, limes.ErrUnitCodeMismatch) {
		t.Errorf("re-denominating a tenant with %s spent: err = %v, want ErrUnitCodeMismatch", unit, err)
	}
}

// checkDeleteReleasesHolds holds a store to releasing what a reaped
// capability held: nothing can settle or release a hold of a capability that
// is gone, so a hold left behind counts against its ancestors and its tenant
// until the store is rebuilt.
func checkDeleteReleasesHolds[TX any](t *testing.T, f fixture[TX]) {
	res, err := f.Usage.Reserve(f.Ctx, limes.ReserveRequest{
		CapabilityID: f.child, TenantID: f.Tenant, Amount: 3 * one, MaxBudget: childBudget, UnitCode: unit,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Usage.Delete(f.Ctx, f.child); err != nil {
		t.Fatalf("Delete: %v", err)
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
		CapabilityID: f.child, TenantID: f.Tenant, Amount: held, MaxBudget: childBudget, UnitCode: unit,
	}); err != nil {
		t.Fatal(err)
	}
	got, err := f.Usage.GetTenantBudget(f.Ctx, f.Tenant)
	if err != nil || got.ReservedAmount != held || got.ResourceVersion == 0 {
		t.Fatalf("GetTenantBudget = %+v, %v; want %v held and a version", got, err, held)
	}
	set, err := f.Usage.SetTenantBudget(f.Ctx, limes.SetTenantBudgetRequest{
		TenantID: f.Tenant, MaxBudgetAmount: tenantBudget, UnitCode: unit, ExpectedVersion: got.ResourceVersion,
	})
	if err != nil || set.ReservedAmount != held || set.ResourceVersion != got.ResourceVersion+1 {
		t.Errorf("SetTenantBudget = %+v, %v; want %v held and the next version", set, err, held)
	}
	for _, ceiling := range []limes.Nanos{-1, limes.MaxNanos + 1} {
		if _, err := f.Usage.SetTenantBudget(f.Ctx, limes.SetTenantBudgetRequest{
			TenantID: f.Tenant, MaxBudgetAmount: ceiling, UnitCode: unit, ExpectedVersion: set.ResourceVersion,
		}); !errors.Is(err, limes.ErrInvalidAmount) {
			t.Errorf("SetTenantBudget(%s): err = %v, want ErrInvalidAmount", ceiling, err)
		}
	}
	listed, _, err := f.Usage.ListTenantBudgets(f.listCtx(), limes.ListTenantBudgetsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(listed, func(s limes.TenantBudgetSummary) bool { return s.TenantID == f.Tenant })
	if i < 0 {
		t.Fatalf("ListTenantBudgets left out the tenant: %+v", listed)
	}
	if b := listed[i].Budget; b.ReservedAmount != held || b.ResourceVersion != set.ResourceVersion {
		t.Errorf("listed budget = %+v; want %v held at version %d", b, held, set.ResourceVersion)
	}
}

// sameReservation names how got differs from want, or returns "".
func sameReservation(got, want limes.Reservation) string {
	switch {
	case got.ID != want.ID, got.CapabilityID != want.CapabilityID, got.TenantID != want.TenantID:
		return "ids differ"
	case got.Amount != want.Amount, got.UnitCode != want.UnitCode:
		return "amount differs"
	case got.Op != want.Op, got.Actor != want.Actor:
		return "op or actor differs"
	case !got.ExpiresAt.Equal(want.ExpiresAt):
		return "expiry differs"
	case len(got.Copies) != len(want.Copies):
		return "copies differ"
	}
	for i := range got.Copies {
		if !bytes.Equal(got.Copies[i].RevocationID, want.Copies[i].RevocationID) || got.Copies[i].MaxBudget != want.Copies[i].MaxBudget || got.Copies[i].MaxRequests != 0 {
			return "copies differ"
		}
	}
	return ""
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
	copies := []limes.CopyCeiling{{RevocationID: []byte("copy-1"), MaxRequests: copyLimit, MaxBudget: copyLimit * one}}
	reserve := func(capID uuid.UUID, ceiling limes.Nanos, ttl time.Duration) limes.Reservation {
		t.Helper()
		r, err := f.Usage.Reserve(f.Ctx, limes.ReserveRequest{
			CapabilityID: capID, TenantID: f.Tenant, Amount: held, MaxBudget: ceiling, UnitCode: unit,
			TTL: ttl, Op: "llm", Actor: "agent", Copies: copies,
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
		first.Op != "llm" || first.Actor != "agent" || len(first.Copies) != 1 || first.Copies[0].MaxRequests != 0 {
		t.Errorf("Reserve returned %+v; want the whole hold, copies with their budgets alone", first)
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
		rs, err := f.Usage.ListReservations(f.Ctx, capID)
		if err != nil {
			t.Fatalf("ListReservations: %v", err)
		}
		ids := []uuid.UUID{}
		for _, r := range rs {
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

	if _, err := f.Usage.Settle(f.Ctx, limes.SettleRequest{ReservationID: first.ID, Amount: held, MaxBudget: childBudget}, nil); err != nil {
		t.Fatal(err)
	}
	if err := f.Usage.Release(f.Ctx, second.ID); err != nil {
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

func checkCopyOverrun[TX any](t *testing.T, f fixture[TX]) {
	copyID := []byte("copy-1")
	copies := []limes.CopyCeiling{{RevocationID: copyID, MaxBudget: one}}
	if _, err := f.charge(limes.ChargeRequest{Amount: 2 * one, Copies: copies}, nil); !errors.Is(err, limes.ErrBudgetExceeded) {
		t.Fatalf("rejecting charge past the copy's budget: err = %v, want ErrBudgetExceeded", err)
	}
	r, err := f.charge(limes.ChargeRequest{Amount: 2 * one, Copies: copies, Overrun: limes.OverrunRecord}, nil)
	if err != nil || !r.Overrun {
		t.Fatalf("recording charge = %+v, %v; want an overrun", r, err)
	}
	reader, ok := f.Usage.(limes.CopyUsageReader)
	if !ok {
		return // a Meter that does not read copies back is checked no further
	}
	got, err := reader.CopyUsage(f.Ctx, [][]byte{copyID})
	if err != nil || len(got) != 1 || got[0].SpentAmount != 2*one {
		t.Errorf("copy usage = %+v, %v; want 2 spent", got, err)
	}
}

// checkSumsExactly holds a store to exact amounts: a tenth and two tenths fit
// under a ceiling of three tenths with not a nano to spare, and seven tenths
// add up to seven tenths — the sums a float64 gets wrong.
func checkSumsExactly[TX any](t *testing.T, f fixture[TX]) {
	tenth, threeTenths := one/10, 3*one/10
	id, err := f.NewCapability(uuid.Nil, threeTenths)
	if err != nil {
		t.Fatal(err)
	}
	for _, amount := range []limes.Nanos{tenth, 2 * tenth} {
		if _, err := f.charge(limes.ChargeRequest{CapabilityID: id, Amount: amount, MaxBudget: threeTenths}, nil); err != nil {
			t.Fatalf("charging %s under a ceiling of %s: %v", amount, threeTenths, err)
		}
	}
	if got := f.spent(t, id); got != threeTenths {
		t.Errorf("spent %s, want exactly %s", got, threeTenths)
	}
	if _, err := f.charge(limes.ChargeRequest{CapabilityID: id, Amount: 1, MaxBudget: threeTenths}, nil); !errors.Is(err, limes.ErrBudgetExceeded) {
		t.Errorf("a nano past the ceiling: err = %v, want ErrBudgetExceeded", err)
	}
	for range 7 {
		if _, err := f.charge(limes.ChargeRequest{CapabilityID: f.root, MaxBudget: rootBudget, Amount: tenth}, nil); err != nil {
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
	sibling, err := f.NewCapability(f.root, childBudget)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.charge(limes.ChargeRequest{Amount: 15 * one}, nil); err != nil {
		t.Fatal(err)
	}
	// 15 + 15 is within each child's 20, past the root's 25.
	_, err = f.charge(limes.ChargeRequest{CapabilityID: sibling, Amount: 15 * one}, nil)
	if !errors.Is(err, limes.ErrBudgetExceeded) {
		t.Fatalf("a sibling's charge past the root's ceiling: err = %v, want ErrBudgetExceeded", err)
	}
	if f.spent(t, sibling) != 0 || f.spent(t, f.root) != 15*one {
		t.Error("a charge refused by an ancestor's ceiling moved spend")
	}
}

// checkTenantCeiling holds a store to the tenant aggregate: capabilities each
// within their own budgets must not sum past the tenant's.
func checkTenantCeiling[TX any](t *testing.T, f fixture[TX]) {
	other, err := f.NewCapability(uuid.Nil, rootBudget)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.charge(limes.ChargeRequest{CapabilityID: f.root, MaxBudget: rootBudget, Amount: 20 * one}, nil); err != nil {
		t.Fatal(err)
	}
	// 20 + 20 is within each root's 25, past the tenant's 30.
	_, err = f.charge(limes.ChargeRequest{CapabilityID: other, MaxBudget: rootBudget, Amount: 20 * one}, nil)
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
	if _, err := f.Usage.ChargeByRef(f.Ctx, f.child, "call-1"); !errors.Is(err, limes.ErrChargeNotFound) {
		t.Errorf("the failed charge's ref: err = %v, want ErrChargeNotFound", err)
	}
	// The ref is free again: a retry charges.
	if r, err := f.charge(limes.ChargeRequest{Amount: one, ExternalRef: "call-1"}, nil); err != nil || r.Replayed {
		t.Errorf("the retry = %+v, %v; want a fresh charge", r, err)
	}

	res, err := f.Usage.Reserve(f.Ctx, limes.ReserveRequest{
		CapabilityID: f.child, TenantID: f.Tenant, Amount: 2 * one, MaxBudget: childBudget, UnitCode: unit,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Usage.Settle(f.Ctx, limes.SettleRequest{ReservationID: res.ID, Amount: 2 * one, MaxBudget: childBudget},
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
		return f.Usage.Bump(f.Ctx, limes.BumpRequest{CapabilityID: f.child, TenantID: f.Tenant, MaxRequests: limit})
	}
	for want := int64(1); want <= limit; want++ {
		if n, err := bump(); err != nil || n != want {
			t.Fatalf("bump = %d, %v; want %d", n, err, want)
		}
	}
	if _, err := bump(); !errors.Is(err, limes.ErrRequestLimitExceeded) {
		t.Fatalf("a bump past the limit: err = %v, want ErrRequestLimitExceeded", err)
	}
	for _, id := range []uuid.UUID{f.child, f.root} {
		if u, err := f.Usage.GetUsage(f.Ctx, id); err != nil || u.RequestCount != limit {
			t.Errorf("requests of %s = %+v, %v; want %d", id, u, err, limit)
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
	if got, err := refund(one); err != nil || got.Refunded != one || got.ChargeID != r.ChargeID ||
		got.TenantID != f.Tenant || got.UnitCode != unit {
		t.Fatalf("partial refund = %+v, %v; want 1 of charge %s, tenant %s in %q", got, err, r.ChargeID, f.Tenant, unit)
	}
	if _, err := refund(3 * one); !errors.Is(err, limes.ErrRefundExceedsCharge) {
		t.Errorf("refunding past what is left: err = %v, want ErrRefundExceedsCharge", err)
	}
	for range 2 { // the rest, then nothing: a full refund is idempotent
		if _, err := refund(0); err != nil {
			t.Fatalf("full refund: %v", err)
		}
	}
	if f.spent(t, f.child) != 0 || f.spent(t, f.root) != 0 || f.tenantSpent(t) != 0 {
		t.Error("a full refund left spend on a counter the charge moved")
	}
	if _, err := f.Usage.Refund(f.Ctx, limes.RefundRequest{ChargeID: uuid.New()}); !errors.Is(err, limes.ErrChargeNotFound) {
		t.Errorf("refund of an unknown charge: err = %v, want ErrChargeNotFound", err)
	}
}

// checkHoldBlocksCharge holds a store to counting holds against charges as
// spend is counted.
func checkHoldBlocksCharge[TX any](t *testing.T, f fixture[TX]) {
	if _, err := f.Usage.Reserve(f.Ctx, limes.ReserveRequest{
		CapabilityID: f.child, TenantID: f.Tenant, Amount: 15 * one, MaxBudget: childBudget, UnitCode: unit,
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
		CapabilityID: f.child, TenantID: f.Tenant, Amount: 15 * one, MaxBudget: childBudget, UnitCode: unit, TTL: ttl,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.Advance(2 * ttl)
	if _, err := f.Usage.Settle(f.Ctx, limes.SettleRequest{ReservationID: res.ID, Amount: one, MaxBudget: childBudget}, nil); !errors.Is(err, limes.ErrReservationNotFound) {
		t.Errorf("settling an expired reservation: err = %v, want ErrReservationNotFound", err)
	}
	if _, err := f.charge(limes.ChargeRequest{Amount: 10 * one}, nil); !errors.Is(err, limes.ErrBudgetExceeded) {
		t.Errorf("before ReleaseExpired the hold still counts: err = %v, want ErrBudgetExceeded", err)
	}
	if n, err := f.Usage.ReleaseExpired(f.Ctx); err != nil || n != 1 {
		t.Fatalf("ReleaseExpired = %d, %v; want 1", n, err)
	}
	if _, err := f.charge(limes.ChargeRequest{Amount: 10 * one}, nil); err != nil {
		t.Errorf("after ReleaseExpired: %v", err)
	}
}

// checkListTenantBudgets holds a store to ListTenantBudgets' order, filters
// and paging: most at risk first, then by tenant id, a page at a time.
func checkListTenantBudgets[TX any](t *testing.T, f fixture[TX]) {
	// The fixture's tenant has a ceiling of 30 and nothing spent; three more
	// have ceilings, and one of them is unlimited.
	if _, err := f.charge(limes.ChargeRequest{CapabilityID: f.root, MaxBudget: rootBudget, Amount: 15 * one}, nil); err != nil {
		t.Fatal(err)
	}
	unlimited, err := f.newTenant()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Usage.SetTenantBudget(f.tenantCtx(unlimited), limes.SetTenantBudgetRequest{TenantID: unlimited, UnitCode: unit}); err != nil {
		t.Fatal(err)
	}
	all := map[uuid.UUID]bool{}
	var order []limes.TenantBudgetSummary
	cursor := ""
	for {
		page, next, err := f.Usage.ListTenantBudgets(f.listCtx(), limes.ListTenantBudgetsRequest{Limit: 1, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		if len(page) > 1 {
			t.Fatalf("a page of %d rows under a limit of 1", len(page))
		}
		for _, row := range page {
			all[row.TenantID] = true
			order = append(order, row)
		}
		if next == "" {
			break
		}
		cursor = next
	}
	if !all[f.Tenant] || !all[unlimited] {
		t.Fatalf("paging skipped a tenant: %v", all)
	}
	for i := 1; i < len(order); i++ {
		if order[i].UtilisationPct > order[i-1].UtilisationPct {
			t.Errorf("row %d at %v%% follows one at %v%%; want most at risk first", i, order[i].UtilisationPct, order[i-1].UtilisationPct)
		}
	}
	const half = 50
	risky, _, err := f.Usage.ListTenantBudgets(f.listCtx(), limes.ListTenantBudgetsRequest{ThresholdPct: half})
	if err != nil || len(risky) != 1 || risky[0].TenantID != f.Tenant || risky[0].UtilisationPct != half {
		t.Errorf("tenants at 50%% or more = %+v, %v; want only %s at 50%%", risky, err, f.Tenant)
	}
	unl, _, err := f.Usage.ListTenantBudgets(f.listCtx(), limes.ListTenantBudgetsRequest{UnlimitedOnly: true})
	if err != nil || slices.ContainsFunc(unl, func(s limes.TenantBudgetSummary) bool { return s.TenantID == f.Tenant }) ||
		!slices.ContainsFunc(unl, func(s limes.TenantBudgetSummary) bool { return s.TenantID == unlimited }) {
		t.Errorf("unlimited tenants = %+v, %v; want %s and not %s", unl, err, unlimited, f.Tenant)
	}
}
