package memstore

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes"
)

// With no record store nothing is known to be gone, so PurgeOrphans leaves
// every counter and hold where it is.
func TestPurgeOrphansWithoutRecordsRemovesNothing(t *testing.T) {
	ctx := context.Background()
	u := newTestUsage(nil)
	subject := newSubject(subjectSpec{capID: uuid.New(), tenant: uuid.New()})
	if _, err := u.Charge(ctx, limes.ChargeRequest{Subject: subject, Amount: unit}, nil); err != nil {
		t.Fatal(err)
	}
	r, err := u.Reserve(ctx, limes.ReserveRequest{Subject: subject, Amount: unit})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := u.PurgeOrphans(ctx, limes.PurgeOrphansRequest{}); err != nil || got.Purged != 0 {
		t.Fatalf("PurgeOrphans = %+v, %v; want nothing purged", got, err)
	}
	if got, err := u.GetUsage(ctx, subject.CapabilityID()); err != nil || got.SpentAmount != unit {
		t.Errorf("usage after PurgeOrphans = %+v, %v", got, err)
	}
	if _, err := u.GetReservation(ctx, r.ID); err != nil {
		t.Errorf("hold after PurgeOrphans: %v", err)
	}
}

// A row Delete dropped stays dropped when a descendant's hold, which still
// counted against it, is released.
func TestDeleteIsNotUndoneByADescendantsHold(t *testing.T) {
	ctx := context.Background()
	records := newTestStore()
	tenant, root, child := uuid.New(), uuid.New(), uuid.New()
	for _, c := range []limes.Capability{mkCap(root, uuid.Nil, tenant, "root"), mkCap(child, root, tenant, "child")} {
		if err := records.Insert(ctx, limes.InsertRequest{Capability: c, IssuedBy: limes.Principal{Subject: "op"}}); err != nil {
			t.Fatal(err)
		}
	}
	u := newTestUsage(records)
	r, err := u.Reserve(ctx, limes.ReserveRequest{Subject: newSubject(subjectSpec{capID: child, tenant: tenant}), Amount: unit})
	if err != nil {
		t.Fatal(err)
	}
	if err := u.DeleteUsage(ctx, limes.DeleteUsageRequest{CapabilityID: root}); err != nil {
		t.Fatal(err)
	}
	if err := u.Release(ctx, limes.ReleaseRequest{ReservationID: r.ID}); err != nil {
		t.Fatal(err)
	}
	if got, err := u.GetUsage(ctx, root); !errors.Is(err, limes.ErrUsageNotFound) {
		t.Errorf("the deleted root after its child's hold was released = %+v, %v; want ErrUsageNotFound", got, err)
	}
}
