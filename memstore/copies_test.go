package memstore

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes"
)

// Copy counters: each Biscuit copy that set limits of its own counts apart
// from its siblings, and the capability's counters still take all of them.

// unit is one unit of the capability's currency.
const unit limes.Nanos = limes.NanosPerUnit

func copyCeiling(id string, requests int64, budget limes.Nanos) limes.CopyCeiling {
	return limes.CopyCeiling{RevocationID: []byte(id), Requests: &limes.RequestCeiling{Max: requests}, Budget: &limes.BudgetCeiling{Max: budget}}
}

func newCopyFixture(t *testing.T) (context.Context, *UsageStore[struct{}], uuid.UUID, uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	records := newTestStore()
	capID, tenant := uuid.New(), uuid.New()
	if err := records.Insert(ctx, limes.InsertRequest{Capability: mkCap(capID, uuid.Nil, tenant, "agent"), IssuedBy: limes.Principal{Subject: "op"}}); err != nil {
		t.Fatal(err)
	}
	return ctx, newTestUsage(records), capID, tenant
}

func TestCopyRequestLimits(t *testing.T) {
	ctx, u, capID, tenant := newCopyFixture(t)
	const capLimit = 5
	outer := copyCeiling("outer", 3, 0)
	inner := copyCeiling("inner", 1, 0)
	sibling := copyCeiling("sibling", 3, 0)
	bump := func(copies ...limes.CopyCeiling) error {
		_, err := u.Bump(ctx, limes.BumpRequest{
			Subject: recordedSubject(ctx, t, u, subjectSpec{capID: capID, tenant: tenant, requests: requestsOf(capLimit), copies: copies}),
		})
		return err
	}

	// innermost first, as the verifier hands them over
	if err := bump(inner, outer); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := bump(inner, outer); !errors.Is(err, limes.ErrRequestLimitExceeded) {
		t.Fatalf("inner copy past its limit: %v", err)
	}
	if got := u.copies[copyKeyOf(capID, []byte("outer"))].requests; got != 1 {
		t.Fatalf("a refused request moved the outer copy to %d", got)
	}
	// The outer copy has room for two more, then is spent.
	for range 2 {
		if err := bump(outer); err != nil {
			t.Fatalf("outer: %v", err)
		}
	}
	if err := bump(outer); !errors.Is(err, limes.ErrRequestLimitExceeded) {
		t.Fatalf("outer copy past its limit: %v", err)
	}
	// A sibling counts apart, until the capability's own limit stops it.
	if err := bump(sibling); err != nil {
		t.Fatalf("sibling: %v", err)
	}
	if err := bump(sibling); err != nil {
		t.Fatalf("sibling: %v", err)
	}
	if err := bump(sibling); !errors.Is(err, limes.ErrRequestLimitExceeded) {
		t.Fatalf("the capability's limit did not bound its copies together: %v", err)
	}
	if got := u.copies[copyKeyOf(capID, []byte("sibling"))].requests; got != 2 {
		t.Fatalf("sibling = %d, want 2", got)
	}
}

func TestCopyBudgetChargeAndRefund(t *testing.T) {
	ctx, u, capID, tenant := newCopyFixture(t)
	c := copyCeiling("copy", 0, 2*unit)
	charge := func(amount limes.Nanos) (limes.ChargeReceipt, error) {
		return u.Charge(ctx, limes.ChargeRequest{
			Subject: recordedSubject(ctx, t, u, subjectSpec{capID: capID, tenant: tenant, copies: []limes.CopyCeiling{c}}),
			Amount:  amount,
		}, nil)
	}
	r, err := charge(3 * unit / 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := charge(unit); !errors.Is(err, limes.ErrBudgetExceeded) {
		t.Fatalf("copy past its budget: %v", err)
	}
	if got, _ := u.GetUsage(ctx, capID); got.SpentAmount != 3*unit/2 {
		t.Fatalf("a refused charge moved the capability to %v", got.SpentAmount)
	}
	// Refund returns to the copy without being told about it.
	if _, err := u.Refund(ctx, limes.RefundRequest{ChargeID: r.ChargeID, Remaining: true}); err != nil {
		t.Fatal(err)
	}
	if got := u.copies[copyKeyOf(capID, []byte("copy"))].spent; got != 0 {
		t.Fatalf("copy spend after refund = %v", got)
	}
	if _, err := charge(2 * unit); err != nil {
		t.Fatalf("after refund: %v", err)
	}
}

func TestCopyBudgetReservations(t *testing.T) {
	ctx, u, capID, tenant := newCopyFixture(t)
	c := copyCeiling("copy", 0, 2*unit)
	reserve := func(amount limes.Nanos) (limes.Reservation, error) {
		return u.Reserve(ctx, limes.ReserveRequest{
			Subject: recordedSubject(ctx, t, u, subjectSpec{capID: capID, tenant: tenant, copies: []limes.CopyCeiling{c}}),
			Amount:  amount,
		})
	}
	r, err := reserve(3 * unit / 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reserve(unit); !errors.Is(err, limes.ErrBudgetExceeded) {
		t.Fatalf("a hold past the copy's budget: %v", err)
	}
	// Settling above what the copy has room for is refused, and the hold stays.
	if _, err := u.Settle(ctx, limes.SettleRequest{Subject: recordedSubject(ctx, t, u, subjectSpec{capID: capID, tenant: tenant, copies: []limes.CopyCeiling{c}}), ReservationID: r.ID, Amount: 5 * unit / 2}, nil); !errors.Is(err, limes.ErrBudgetExceeded) {
		t.Fatalf("settle past the copy's budget: %v", err)
	}
	if _, err := u.Settle(ctx, limes.SettleRequest{Subject: recordedSubject(ctx, t, u, subjectSpec{capID: capID, tenant: tenant, copies: []limes.CopyCeiling{c}}), ReservationID: r.ID, Amount: 7 * unit / 4}, nil); err != nil {
		t.Fatalf("settle: %v", err)
	}
	if got := u.copies[copyKeyOf(capID, []byte("copy"))]; got.spent != 7*unit/4 || got.reserved != 0 {
		t.Fatalf("copy after settle = %+v", got)
	}
	// Release returns a hold to the copy.
	r2, err := reserve(unit / 4)
	if err != nil {
		t.Fatal(err)
	}
	if err := u.Release(ctx, limes.ReleaseRequest{Subject: recordedSubject(ctx, t, u, subjectSpec{capID: capID, tenant: tenant, copies: []limes.CopyCeiling{c}}), ReservationID: r2.ID}); err != nil {
		t.Fatal(err)
	}
	if got := u.copies[copyKeyOf(capID, []byte("copy"))].reserved; got != 0 {
		t.Fatalf("copy reserved after release = %v", got)
	}
}

// A copy's counters go with its capability.
func TestCopyCountersPurgedWithTheirCapability(t *testing.T) {
	ctx, u, capID, tenant := newCopyFixture(t)
	if _, err := u.Bump(ctx, limes.BumpRequest{
		Subject: recordedSubject(ctx, t, u, subjectSpec{capID: capID, tenant: tenant, copies: []limes.CopyCeiling{copyCeiling("copy", 1, 0)}}),
	}); err != nil {
		t.Fatal(err)
	}
	if err := u.DeleteUsage(ctx, limes.DeleteUsageRequest{CapabilityID: capID}); err != nil {
		t.Fatal(err)
	}
	if _, ok := u.copies[copyKeyOf(capID, []byte("copy"))]; ok {
		t.Fatal("copy counter outlived its capability's usage")
	}

	gone := uuid.New() // counted while on record, then purged
	if _, err := u.Bump(ctx, limes.BumpRequest{
		Subject: recordedSubject(ctx, t, u, subjectSpec{capID: gone, tenant: tenant, copies: []limes.CopyCeiling{copyCeiling("orphan", 1, 0)}}),
	}); err != nil {
		t.Fatal(err)
	}
	dropRecord(u.records, gone)
	if _, err := u.PurgeOrphans(ctx, limes.PurgeOrphansRequest{}); err != nil {
		t.Fatal(err)
	}
	if _, ok := u.copies[copyKeyOf(gone, []byte("orphan"))]; ok {
		t.Fatal("PurgeOrphans left a copy counter of a capability not on record")
	}
}

// CopyUsage reads back what was counted, and leaves out a copy never used.
func TestCopyUsageReadsTheCounters(t *testing.T) {
	ctx, u, capID, tenant := newCopyFixture(t)
	c := copyCeiling("read", 5, 2*unit)
	if _, err := u.Bump(ctx, limes.BumpRequest{Subject: recordedSubject(ctx, t, u, subjectSpec{capID: capID, tenant: tenant, copies: []limes.CopyCeiling{c}})}); err != nil {
		t.Fatal(err)
	}
	if _, err := u.Charge(ctx, limes.ChargeRequest{Subject: recordedSubject(ctx, t, u, subjectSpec{capID: capID, tenant: tenant, copies: []limes.CopyCeiling{c}}), Amount: unit / 2}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := u.Reserve(ctx, limes.ReserveRequest{Subject: recordedSubject(ctx, t, u, subjectSpec{capID: capID, tenant: tenant, copies: []limes.CopyCeiling{c}}), Amount: unit / 4}); err != nil {
		t.Fatal(err)
	}
	res, err := u.CopyUsage(ctx, limes.CopyUsageRequest{CapabilityID: capID, RevocationIDs: [][]byte{[]byte("read"), []byte("never")}})
	got := res.Copies
	if err != nil {
		t.Fatal(err)
	}
	want := limes.CopyUsage{RevocationID: []byte("read"), CapabilityID: capID, RequestCount: 1, SpentAmount: unit / 2, ReservedAmount: unit / 4}
	if len(got) != 1 || string(got[0].RevocationID) != "read" || got[0].CapabilityID != want.CapabilityID ||
		got[0].RequestCount != want.RequestCount || got[0].SpentAmount != want.SpentAmount || got[0].ReservedAmount != want.ReservedAmount {
		t.Fatalf("CopyUsage = %+v, want [%+v]", got, want)
	}
}
