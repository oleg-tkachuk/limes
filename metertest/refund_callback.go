package metertest

import (
	"context"
	"errors"
	"testing"
	"uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/internal/expect"
	"github.com/oleg-tkachuk/limes/meter"
)

// refundCallback counts the runs of an onRefunded and keeps the receipt it
// was last handed.
type refundCallback struct {
	runs int
	seen meter.RefundReceipt
}

// run is the onRefunded the check passes.
func (c *refundCallback) run(_ context.Context, r meter.Refunded) error {
	c.runs++
	c.seen = r.Receipt
	return nil
}

// checkRefundCallbackRollback holds a store to the atomicity of onRefunded:
// its error is returned as it is, and no counter, refunded amount or
// RefundID survives it — a retry under the same id refunds anew.
func checkRefundCallbackRollback(t *testing.T, f fixture) {
	boom := errors.New("side effect failed")
	failing := func(context.Context, meter.Refunded) error { return boom }
	c := f.keyedCharged(t)
	unchanged := func(how string) {
		t.Helper()
		rec, err := f.Usage.GetCharge(f.Ctx, c.ChargeID)
		expect.Must(t, err == nil, "%v", err)
		expect.That(t, rec.RefundedAmount == 0, "%s: the charge records %v refunded, want nothing", how, rec.RefundedAmount)
		expect.That(t, f.spent(t, f.child) == keyedCharge && f.spent(t, f.root) == keyedCharge && f.tenantSpent(t) == keyedCharge,
			"%s: spent child %v, root %v, tenant %v; want %v each", how, f.spent(t, f.child), f.spent(t, f.root), f.tenantSpent(t), keyedCharge)
	}

	id := uuid.New()
	part := meter.RefundRequest{ChargeID: c.ChargeID, Amount: keyedPart, RefundID: id}
	_, err := f.Usage.Refund(f.Ctx, part, failing)
	expect.Must(t, errors.Is(err, boom), "a keyed refund with a failing callback: err = %v, want the callback's", err)
	unchanged("a keyed refund whose callback failed")
	_, err = f.Usage.Refund(f.Ctx, meter.RefundRequest{ChargeID: c.ChargeID, Remaining: true}, failing)
	expect.Must(t, errors.Is(err, boom), "a refund of the rest with a failing callback: err = %v, want the callback's", err)
	unchanged("a refund of the rest whose callback failed")

	// The id is free again: a retry refunds.
	r, err := f.Usage.Refund(f.Ctx, part, nil)
	expect.That(t, err == nil && !r.Replayed && r.Amount == keyedPart, "the retry = %+v, %v; want %v refunded anew", r, err, keyedPart)
}

// checkRefundCallbackSeesTheReceipt holds a store to handing onRefunded the
// refund it runs inside, once: the receipt the call then returns, with the
// amount actually refunded — a part, or what remains — and the RefundID, so
// a side effect such as an outbox row can name them.
func checkRefundCallbackSeesTheReceipt(t *testing.T, f fixture) {
	c := f.keyedCharged(t)
	for _, tc := range []struct {
		name string
		req  meter.RefundRequest
		want limes.Nanos
	}{
		{name: "an unkeyed part", req: meter.RefundRequest{ChargeID: c.ChargeID, Amount: keyedPart}, want: keyedPart},
		{name: "a keyed part", req: meter.RefundRequest{ChargeID: c.ChargeID, Amount: keyedPart, RefundID: uuid.New()}, want: keyedPart},
		{name: "the rest", req: meter.RefundRequest{ChargeID: c.ChargeID, Remaining: true, RefundID: uuid.New()}, want: keyedCharge - 2*keyedPart},
	} {
		var cb refundCallback
		got, err := f.Usage.Refund(f.Ctx, tc.req, cb.run)
		expect.Must(t, err == nil && got.Amount == tc.want, "%s = %+v, %v; want %v refunded", tc.name, got, err, tc.want)
		expect.Must(t, cb.runs == 1, "%s: onRefunded ran %d times, want once", tc.name, cb.runs)
		s := cb.seen
		expect.That(t, s.ChargeID == c.ChargeID && s.Amount == got.Amount && !s.Replayed && s.TenantID == f.Tenant && s.UnitCode == unit && s.RefundID == tc.req.RefundID && got.RefundID == tc.req.RefundID,
			"%s: onRefunded saw %+v, the call returned %+v", tc.name, s, got)
	}
}

// checkRefundCallbackSkipsAReplay holds a store to running onRefunded once
// for a keyed refund: its replay moved nothing and runs none, so a side
// effect committed with the first is not written again.
func checkRefundCallbackSkipsAReplay(t *testing.T, f fixture) {
	c := f.keyedCharged(t)
	var cb refundCallback
	req := meter.RefundRequest{ChargeID: c.ChargeID, Amount: keyedPart, RefundID: uuid.New()}
	_, err := f.Usage.Refund(f.Ctx, req, cb.run)
	expect.Must(t, err == nil && cb.runs == 1, "keyed refund: %v, onRefunded ran %d times; want once", err, cb.runs)
	again, err := f.Usage.Refund(f.Ctx, req, cb.run)
	expect.Must(t, err == nil && again.Replayed && again.RefundID == req.RefundID, "keyed refund again = %+v, %v; want it replayed", again, err)
	expect.That(t, cb.runs == 1, "a replayed refund ran onRefunded again (%d runs)", cb.runs)
}

// checkRefundCallbackSkipsNothing holds a store to running no onRefunded for
// a refund of nothing — Remaining once nothing remains — keyed or not. A
// keyed one is still recorded, and its retry replays and runs none either.
func checkRefundCallbackSkipsNothing(t *testing.T, f fixture) {
	c := f.keyedCharged(t)
	_, err := f.Usage.Refund(f.Ctx, meter.RefundRequest{ChargeID: c.ChargeID, Remaining: true}, nil)
	expect.Must(t, err == nil, "refund of the whole charge: %v", err)
	var cb refundCallback
	for _, tc := range []struct {
		name string
		req  meter.RefundRequest
	}{
		{name: "unkeyed", req: meter.RefundRequest{ChargeID: c.ChargeID, Remaining: true}},
		{name: "keyed", req: meter.RefundRequest{ChargeID: c.ChargeID, Remaining: true, RefundID: uuid.New()}},
	} {
		got, err := f.Usage.Refund(f.Ctx, tc.req, cb.run)
		expect.Must(t, err == nil && got.Amount == 0, "%s refund of nothing = %+v, %v; want nothing refunded", tc.name, got, err)
		expect.That(t, cb.runs == 0, "%s refund of nothing ran onRefunded", tc.name)
		if tc.req.RefundID == uuid.Nil() {
			continue
		}
		again, err := f.Usage.Refund(f.Ctx, tc.req, cb.run)
		expect.That(t, err == nil && again.Replayed && again.Amount == 0, "%s refund of nothing again = %+v, %v; want it replayed", tc.name, again, err)
		expect.That(t, cb.runs == 0, "%s refund of nothing replayed ran onRefunded", tc.name)
	}
}

// A refund of a charge under an open lease runs onRefunded as one without
// does: a callback that fails is returned as it is and moves neither the
// counters nor the lease, and one that succeeds sees the refund through.
func checkALeasedRefundRunsOnRefunded(t *testing.T, f fixture) {
	l := f.leaser(t)
	child := f.childSubject()
	f.lease(t, l, child, leased)
	c, err := f.chargeOf(child, leaseCharge)
	expect.Must(t, err == nil, "leased charge: %v", err)
	boom := errors.New("side effect failed")
	_, err = f.Usage.Refund(f.Ctx, meter.RefundRequest{ChargeID: c.ChargeID, Amount: one}, func(context.Context, meter.Refunded) error { return boom })
	expect.Must(t, errors.Is(err, boom), "a leased refund with a failing callback: err = %v, want the callback's", err)
	open, err := l.GetLease(f.Ctx, f.child)
	expect.That(t, err == nil && open.Spent == leaseCharge, "the lease after a failed callback = %+v, %v; want %v spent", open, err, leaseCharge)
	expect.That(t, f.usageOf(t, f.child).SpentAmount == leaseCharge, "the child spent %v after a failed callback, want %v", f.usageOf(t, f.child).SpentAmount, leaseCharge)
	var cb refundCallback
	r, err := f.Usage.Refund(f.Ctx, meter.RefundRequest{ChargeID: c.ChargeID, Amount: one}, cb.run)
	expect.Must(t, err == nil && r.Amount == one && cb.runs == 1 && cb.seen.Amount == one, "a leased refund with a callback = %+v, %v; the callback ran %d times and saw %+v", r, err, cb.runs, cb.seen)
	open, err = l.GetLease(f.Ctx, f.child)
	expect.That(t, err == nil && open.Spent == leaseCharge-one, "the lease after the refund = %+v, %v; want %v spent", open, err, leaseCharge-one)
}
