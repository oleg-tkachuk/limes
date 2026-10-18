package memstore

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/meter"
)

// A usage store with no records to read lineage from is refused: no
// ancestor's ceiling would bind, and PurgeOrphans could tell nothing gone.
func TestUsageStoreRequiresRecords(t *testing.T) {
	if _, err := NewUsageStore(UsageStoreConfig[struct{}]{}); err == nil {
		t.Error("NewUsageStore accepted no Records")
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
	subject := recordedSubject(ctx, t, u, subjectSpec{capID: child, tenant: tenant})
	r, err := u.Reserve(ctx, meter.ReserveRequest{Subject: subject, Amount: unit})
	if err != nil {
		t.Fatal(err)
	}
	if err := u.DeleteUsage(ctx, meter.DeleteUsageRequest{CapabilityID: root}); err != nil {
		t.Fatal(err)
	}
	if err := u.Release(ctx, meter.ReleaseRequest{Subject: subject, ReservationID: r.ID}); err != nil {
		t.Fatal(err)
	}
	if got, err := u.GetUsage(ctx, root); !errors.Is(err, meter.ErrUsageNotFound) {
		t.Errorf("the deleted root after its child's hold was released = %+v, %v; want ErrUsageNotFound", got, err)
	}
}
