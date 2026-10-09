// Package metertest checks a limes.Meter against the module's contract,
// the way testing/fstest checks an fs.FS. A Meter is the consumer's to write,
// and most of its contract lives in request fields — a store that ignores
// ExternalRef or Overrun still compiles, and then charges a repeated cost
// twice. Run it from the store's own tests:
//
//	func TestMeterContract(t *testing.T) {
//		metertest.Run(t, func(t *testing.T) metertest.Env[pgx.Tx] { … })
//	}
//
// The checks cover what a charge does at its ceilings and when it arrives
// again: rejection that moves nothing, idempotency on ExternalRef and on a
// settled reservation, ChargeByRef, OverrunRecord past every ceiling, and the
// validation of the request. They need a real ledger, not a fake.
package metertest

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes"
)

// Env is one store under test, fresh for each check.
type Env[TX any] struct {
	// Ctx is the context every call runs under — scoped to Tenant, if the
	// store needs that.
	Ctx context.Context
	// Usage is the store under test, with no usage recorded.
	Usage limes.UsageStore[TX]
	// Tenant is a tenant the store can charge, with no ceiling yet.
	Tenant uuid.UUID
	// NewCapability records a capability of Tenant with the given parent
	// (uuid.Nil for a root) and budget, so the Meter finds its lineage and
	// its ceiling, and returns its id.
	NewCapability func(parent uuid.UUID, maxBudget limes.Nanos) (uuid.UUID, error)
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
	// unit is the currency every amount is in.
	unit = limes.DefaultUnitCode
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
		{"OverrunRecordCrossesACopyBudget", checkCopyOverrun[TX]},
		{"TenantBudgetReadsAgree", checkTenantBudgetReads[TX]},
		{"ReservationsReadBack", checkReservations[TX]},
		{"SumsExactly", checkSumsExactly[TX]},
	}
	for _, c := range checks {
		t.Run(c.name, func(t *testing.T) { c.run(t, newFixture(t, setup)) })
	}
}

func checkRejectionMovesNothing[TX any](t *testing.T, f fixture[TX]) {
	ran := false
	_, err := f.charge(limes.ChargeRequest{Amount: childBudget + one}, func(context.Context, TX) error { ran = true; return nil })
	if !errors.Is(err, limes.ErrBudgetExceeded) {
		t.Fatalf("charge past the ceiling: err = %v, want ErrBudgetExceeded", err)
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
	again, err := f.charge(limes.ChargeRequest{Amount: 5 * one, ExternalRef: "call-1"}, onCharged)
	if err != nil || !again.Replayed || again.ChargeID != first.ChargeID || again.Spent != 2*one {
		t.Fatalf("repeated charge = %+v, %v; want a replay of %s at spend 2", again, err, first.ChargeID)
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
	listed, _, err := f.Usage.ListTenantBudgets(f.Ctx, limes.ListTenantBudgetsRequest{})
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
