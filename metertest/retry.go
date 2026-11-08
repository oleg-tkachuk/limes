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

// The amounts and attribution the checks of a keyed Reserve and a keyed
// Refund work with.
const (
	// keyedHold is what a keyed Reserve holds.
	keyedHold = 2 * one
	// keyedCharge is what the charge a keyed Refund refunds from took, and
	// keyedPart the part of it one refund returns.
	keyedCharge = 5 * one
	keyedPart   = one
	// keyedOp and keyedActor attribute a keyed hold.
	keyedOp    limes.Op = "llm"
	keyedActor string   = "agent"
	// keyedCopyRequests is the request ceiling of the copy a conflicting
	// Reserve names: any, as a hold counts no requests.
	keyedCopyRequests = 10
)

// keyedReserve is the request of a hold of keyedHold of the child under id.
func (f fixture) keyedReserve(id uuid.UUID) meter.ReserveRequest {
	return meter.ReserveRequest{Subject: f.childSubject(), Amount: keyedHold, Op: keyedOp, Actor: keyedActor, ReservationID: id}
}

// A Reserve under an id holds once: sent again for the same request it
// returns the hold on record, Replayed, and holds nothing more.
func checkReserveReplaysItsID(t *testing.T, f fixture) {
	id := uuid.New()
	first, err := f.Usage.Reserve(f.Ctx, f.keyedReserve(id))
	expect.Must(t, err == nil && first.ID == id && !first.Replayed, "first Reserve under %s = %+v, %v; want a fresh hold under the id", id, first, err)
	again, err := f.Usage.Reserve(f.Ctx, f.keyedReserve(id))
	expect.Must(t, err == nil && again.Replayed && again.ID == id, "Reserve again under %s = %+v, %v; want the hold replayed", id, again, err)
	again.Replayed = false
	expect.That(t, sameReservation(again, first) == "", "the replay = %+v, the first returned %+v: %s", again, first, sameReservation(again, first))
	got := f.usageOf(t, f.child).ReservedAmount
	expect.That(t, got == keyedHold, "child holds %v after a replay, want %v once", got, keyedHold)
	read, err := f.Usage.GetReservation(f.Ctx, id)
	expect.That(t, err == nil && !read.Replayed, "GetReservation = %+v, %v; want the hold, no replay", read, err)
	fresh, err := f.Usage.Reserve(f.Ctx, f.keyedReserve(uuid.Nil()))
	expect.That(t, err == nil && fresh.ID != uuid.Nil() && fresh.ID != id && !fresh.Replayed, "Reserve with no id = %+v, %v; want a hold under an id of the store's", fresh, err)
}

// An id names one hold: sent again for another request, a Reserve is
// ErrReserveConflict, a conflict, and holds nothing.
func checkReserveIDConflicts(t *testing.T, f fixture) {
	id := uuid.New()
	_, err := f.Usage.Reserve(f.Ctx, f.keyedReserve(id))
	expect.Must(t, err == nil, "Reserve: %v", err)
	withSubject := func(s subjectSpec) meter.ReserveRequest {
		r := f.keyedReserve(id)
		r.Subject = newSubject(s)
		return r
	}
	other := func(change func(*meter.ReserveRequest)) meter.ReserveRequest {
		r := f.keyedReserve(id)
		change(&r)
		return r
	}
	for name, req := range map[string]meter.ReserveRequest{
		"another amount":     other(func(r *meter.ReserveRequest) { r.Amount = keyedHold + one }),
		"another op":         other(func(r *meter.ReserveRequest) { r.Op = chargedOp }),
		"another actor":      other(func(r *meter.ReserveRequest) { r.Actor = "reporter" }),
		"another unit":       withSubject(subjectSpec{capID: f.child, tenant: f.Tenant, budget: budgetOf(childBudget), unit: otherUnit}),
		"another capability": withSubject(subjectSpec{capID: f.root, tenant: f.Tenant, budget: budgetOf(rootBudget)}),
		"under a Biscuit copy": withSubject(subjectSpec{capID: f.child, tenant: f.Tenant, budget: budgetOf(childBudget),
			copies: []limes.CopyCeiling{{RevocationID: []byte("keyed-copy"), Requests: &limes.RequestCeiling{Max: keyedCopyRequests}}}}),
	} {
		r, err := f.Usage.Reserve(f.Ctx, req)
		expect.That(t, errors.Is(err, meter.ErrReserveConflict) && errors.Is(err, limes.ErrConflict), "Reserve under %s for %s = %+v, %v; want ErrReserveConflict, a conflict", id, name, r, err)
	}
	for _, capID := range []uuid.UUID{f.child, f.root} {
		got := f.usageOf(t, capID).ReservedAmount
		expect.That(t, got == keyedHold, "%s holds %v after the conflicts, want %v", capID, got, keyedHold)
	}
}

// The id of a hold settled names its charge: a Reserve under it is
// ErrReserveConflict. The id of one released is free, and holds anew.
func checkReserveIDAfterItsHold(t *testing.T, f fixture) {
	settled := uuid.New()
	_, err := f.Usage.Reserve(f.Ctx, f.keyedReserve(settled))
	expect.Must(t, err == nil, "Reserve: %v", err)
	_, err = f.Usage.Settle(f.Ctx, meter.SettleRequest{Subject: f.childSubject(), ReservationID: settled, Amount: keyedHold}, nil)
	expect.Must(t, err == nil, "Settle: %v", err)
	r, err := f.Usage.Reserve(f.Ctx, f.keyedReserve(settled))
	expect.That(t, errors.Is(err, meter.ErrReserveConflict), "Reserve under a settled hold's id = %+v, %v; want ErrReserveConflict", r, err)
	expect.That(t, f.usageOf(t, f.child).ReservedAmount == 0, "a Reserve under a settled id held something")
	released := uuid.New()
	_, err = f.Usage.Reserve(f.Ctx, f.keyedReserve(released))
	expect.Must(t, err == nil, "Reserve: %v", err)
	err = f.Usage.Release(f.Ctx, meter.ReleaseRequest{Subject: f.childSubject(), ReservationID: released})
	expect.Must(t, err == nil, "Release: %v", err)
	again, err := f.Usage.Reserve(f.Ctx, f.keyedReserve(released))
	expect.That(t, err == nil && again.ID == released && !again.Replayed, "Reserve under a released hold's id = %+v, %v; want a fresh hold", again, err)
	expect.That(t, f.usageOf(t, f.child).ReservedAmount == keyedHold, "the fresh hold under a released id is not held")
}

// Many deliveries of one keyed Reserve at once hold once: one is fresh,
// every other a replay of it.
func checkReserveIDConcurrent(t *testing.T, f fixture) {
	id := uuid.New()
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		fresh int
	)
	for range concurrentDeliveries {
		wg.Go(func() {
			r, err := f.Usage.Reserve(f.Ctx, f.keyedReserve(id))
			if err != nil || r.ID != id {
				t.Errorf("Reserve under %s = %+v, %v", id, r, err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if !r.Replayed {
				fresh++
			}
		})
	}
	wg.Wait()
	expect.Must(t, fresh == 1, "%d fresh holds under one id, want one", fresh)
	for _, capID := range []uuid.UUID{f.child, f.root} {
		got := f.usageOf(t, capID).ReservedAmount
		expect.That(t, got == keyedHold, "%s holds %v, want %v once", capID, got, keyedHold)
	}
}

// keyedCharged charges the child keyedCharge, for a keyed refund.
func (f fixture) keyedCharged(t *testing.T) meter.ChargeReceipt {
	t.Helper()
	r, err := f.charge(meter.ChargeRequest{Amount: keyedCharge}, nil)
	expect.Must(t, err == nil, "charge: %v", err)
	return r
}

// A Refund under an id refunds once: sent again for the same request it
// returns the first receipt, Replayed, and returns nothing more. An unkeyed
// partial refund sent twice refunds twice.
func checkRefundReplaysItsID(t *testing.T, f fixture) {
	c := f.keyedCharged(t)
	part := meter.RefundRequest{ChargeID: c.ChargeID, Amount: keyedPart, RefundID: uuid.New()}
	first, err := f.Usage.Refund(f.Ctx, part, nil)
	expect.Must(t, err == nil && first.Amount == keyedPart && !first.Replayed, "keyed refund = %+v, %v; want %v returned", first, err, keyedPart)
	again, err := f.Usage.Refund(f.Ctx, part, nil)
	expect.Must(t, err == nil && again.Replayed && again.Amount == keyedPart && again.ChargeID == c.ChargeID && again.UnitCode == unit && again.TenantID == f.Tenant,
		"keyed refund again = %+v, %v; want the first receipt replayed", again, err)
	expect.That(t, f.spent(t, f.child) == keyedCharge-keyedPart, "child spent %v after a replayed refund, want %v", f.spent(t, f.child), keyedCharge-keyedPart)
	rest := meter.RefundRequest{ChargeID: c.ChargeID, Remaining: true, RefundID: uuid.New()}
	all, err := f.Usage.Refund(f.Ctx, rest, nil)
	expect.Must(t, err == nil && all.Amount == keyedCharge-keyedPart, "keyed refund of the rest = %+v, %v", all, err)
	all, err = f.Usage.Refund(f.Ctx, rest, nil)
	expect.That(t, err == nil && all.Replayed && all.Amount == keyedCharge-keyedPart, "keyed refund of the rest again = %+v, %v; want what it returned, replayed", all, err)
	unkeyed := f.keyedCharged(t)
	for range 2 {
		_, err := f.Usage.Refund(f.Ctx, meter.RefundRequest{ChargeID: unkeyed.ChargeID, Amount: keyedPart}, nil)
		expect.Must(t, err == nil, "unkeyed refund: %v", err)
	}
	expect.That(t, f.spent(t, f.child) == keyedCharge-2*keyedPart, "child spent %v after two unkeyed refunds, want %v", f.spent(t, f.child), keyedCharge-2*keyedPart)
}

// An id names one refund: sent again for another charge or another amount,
// a Refund is ErrRefundConflict, a conflict, and changes nothing.
func checkRefundIDConflicts(t *testing.T, f fixture) {
	c, other := f.keyedCharged(t), f.keyedCharged(t)
	id := uuid.New()
	_, err := f.Usage.Refund(f.Ctx, meter.RefundRequest{ChargeID: c.ChargeID, Amount: keyedPart, RefundID: id}, nil)
	expect.Must(t, err == nil, "keyed refund: %v", err)
	for name, req := range map[string]meter.RefundRequest{
		"another amount":     {ChargeID: c.ChargeID, Amount: 2 * keyedPart, RefundID: id},
		"the rest":           {ChargeID: c.ChargeID, Remaining: true, RefundID: id},
		"another charge":     {ChargeID: other.ChargeID, Amount: keyedPart, RefundID: id},
		"another charge all": {ChargeID: other.ChargeID, Remaining: true, RefundID: id},
	} {
		r, err := f.Usage.Refund(f.Ctx, req, nil)
		expect.That(t, errors.Is(err, meter.ErrRefundConflict) && errors.Is(err, limes.ErrConflict), "refund under %s for %s = %+v, %v; want ErrRefundConflict, a conflict", id, name, r, err)
	}
	for _, charge := range []uuid.UUID{c.ChargeID, other.ChargeID} {
		rec, err := f.Usage.GetCharge(f.Ctx, charge)
		want := limes.Nanos(0)
		if charge == c.ChargeID {
			want = keyedPart
		}
		expect.That(t, err == nil && rec.RefundedAmount == want, "charge %s refunded %v, %v after the conflicts; want %v", charge, rec.RefundedAmount, err, want)
	}
}

// A refund's id goes with its charge: once PurgeCharges removes the charge,
// a refund of another charge under the id refunds, fresh.
func checkRefundIDGoesWithItsCharge(t *testing.T, f fixture) {
	housekeeping, ok := f.Ledger.(meter.LedgerHousekeeping)
	if !ok || f.Advance == nil {
		t.Skip("Env.Ledger is no meter.LedgerHousekeeping, or Env.Advance is not set")
	}
	c := f.keyedCharged(t)
	id := uuid.New()
	_, err := f.Usage.Refund(f.Ctx, meter.RefundRequest{ChargeID: c.ChargeID, Amount: keyedPart, RefundID: id}, nil)
	expect.Must(t, err == nil, "keyed refund: %v", err)
	f.Advance(meter.MinChargeRetention + purgeStep)
	n := purgeCharges(t, f, housekeeping, meter.PurgeChargesRequest{})
	expect.Must(t, n == 1, "%d charges purged, want the one refunded", n)
	later := f.keyedCharged(t)
	r, err := f.Usage.Refund(f.Ctx, meter.RefundRequest{ChargeID: later.ChargeID, Amount: keyedPart, RefundID: id}, nil)
	expect.That(t, err == nil && !r.Replayed && r.Amount == keyedPart, "refund under the id of a purged charge's refund = %+v, %v; want a fresh refund", r, err)
}

// A refund's id goes with its tenant's usage: once EraseTenantUsage removes
// the charge, a refund of another tenant's charge under the id refunds,
// fresh.
func checkRefundIDGoesWithItsTenant(t *testing.T, f fixture) {
	eraser, ok := f.Usage.(meter.TenantUsageEraser)
	if !ok {
		t.Skip("the UsageStore does not implement meter.TenantUsageEraser")
	}
	other, err := f.newTenant()
	expect.Must(t, err == nil, "NewTenant: %v", err)
	theirCap, err := f.NewCapability(CapabilitySpec{Tenant: other, Requests: limes.RequestCeiling{Unlimited: true}, Budget: unlimitedBudget})
	expect.Must(t, err == nil, "capability of another tenant: %v", err)
	theirCtx := f.tenantCtx(other)
	theirs, err := f.Usage.Charge(theirCtx, meter.ChargeRequest{Subject: newSubject(subjectSpec{capID: theirCap, tenant: other}), Amount: keyedCharge}, nil)
	expect.Must(t, err == nil, "charge of another tenant: %v", err)
	c := f.keyedCharged(t)
	id := uuid.New()
	_, err = f.Usage.Refund(f.Ctx, meter.RefundRequest{ChargeID: c.ChargeID, Amount: keyedPart, RefundID: id}, nil)
	expect.Must(t, err == nil, "keyed refund: %v", err)
	_, err = eraser.EraseTenantUsage(f.Ctx, meter.EraseTenantUsageRequest{TenantID: f.Tenant})
	expect.Must(t, err == nil, "EraseTenantUsage: %v", err)
	r, err := f.Usage.Refund(theirCtx, meter.RefundRequest{ChargeID: theirs.ChargeID, Amount: keyedPart, RefundID: id}, nil)
	expect.That(t, err == nil && !r.Replayed && r.Amount == keyedPart, "refund under the id of an erased tenant's refund = %+v, %v; want a fresh refund", r, err)
}
