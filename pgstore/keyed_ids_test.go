package pgstore

import (
	"errors"
	"testing"
	"uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/meter"
)

// A reservation id or a refund id another tenant holds is hidden from the
// call by row-level security, and still names that tenant's hold or
// refund: the call is ErrReserveConflict or ErrRefundConflict, never a
// constraint violation, and moves nothing.
func TestAnotherTenantsIDConflicts(t *testing.T) {
	f := newTenantFixture(t)
	mine, theirs := WithTenant(t.Context(), f.mine), WithTenant(t.Context(), f.theirs)
	theirCap := f.parent
	theirCap.ID, theirCap.ParentID, theirCap.Subject.TenantID = uuid.New(), uuid.Nil(), f.theirs
	if err := f.records.Insert(theirs, limes.InsertRequest{Capability: theirCap}); err != nil {
		t.Fatal(err)
	}
	mySubject, theirSubject := meterSubject(f.child.ID, f.mine), meterSubject(theirCap.ID, f.theirs)
	const amount = 2
	held := uuid.New()
	if _, err := f.usage.Reserve(theirs, meter.ReserveRequest{Subject: theirSubject, Amount: amount, ReservationID: held}); err != nil {
		t.Fatal(err)
	}
	r, err := f.usage.Reserve(mine, meter.ReserveRequest{Subject: mySubject, Amount: amount, ReservationID: held})
	if !errors.Is(err, meter.ErrReserveConflict) {
		t.Errorf("Reserve under another tenant's reservation id = %+v, %v; want ErrReserveConflict", r, err)
	}
	if u, err := f.usage.GetUsage(mine, f.child.ID); err == nil && u.ReservedAmount != 0 {
		t.Errorf("a conflicting Reserve held %v", u.ReservedAmount)
	}

	theirCharge, err := f.usage.Charge(theirs, meter.ChargeRequest{Subject: theirSubject, Amount: amount}, nil)
	if err != nil {
		t.Fatal(err)
	}
	myCharge, err := f.usage.Charge(mine, meter.ChargeRequest{Subject: mySubject, Amount: amount}, nil)
	if err != nil {
		t.Fatal(err)
	}
	refundID := uuid.New()
	if _, err := f.usage.Refund(theirs, meter.RefundRequest{ChargeID: theirCharge.ChargeID, Remaining: true, RefundID: refundID}, nil); err != nil {
		t.Fatal(err)
	}
	rr, err := f.usage.Refund(mine, meter.RefundRequest{ChargeID: myCharge.ChargeID, Remaining: true, RefundID: refundID}, nil)
	if !errors.Is(err, meter.ErrRefundConflict) {
		t.Errorf("Refund under another tenant's refund id = %+v, %v; want ErrRefundConflict", rr, err)
	}
	if c, err := f.usage.GetCharge(mine, myCharge.ChargeID); err != nil || c.RefundedAmount != 0 {
		t.Errorf("a conflicting Refund refunded %v, %v; want nothing", c.RefundedAmount, err)
	}
}
