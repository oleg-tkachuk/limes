package memstore

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/meter"
)

// raceTree inserts root and child (child under root) in one tenant.
func raceTree(t *testing.T) (*Store, uuid.UUID, uuid.UUID, uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	records := newTestStore()
	tenant, root, child := uuid.New(), uuid.New(), uuid.New()
	for _, c := range []limes.Capability{mkCap(root, uuid.Nil, tenant, "root"), mkCap(child, root, tenant, "child")} {
		if err := records.Insert(ctx, limes.InsertRequest{Capability: c, IssuedBy: limes.Principal{Subject: "op"}}); err != nil {
			t.Fatal(err)
		}
	}
	return records, tenant, root, child
}

// dropRecord simulates Store.Purge having removed id.
func dropRecord(s *Store, id uuid.UUID) {
	s.mu.Lock()
	delete(s.caps, id)
	s.mu.Unlock()
}

// TOCTOU: Charge(child) reads the lineage (root on record) and parks on the
// usage lock; root's record is purged and Delete(root) runs; Charge then
// writes root's counters back.
func TestChargeRacingDeleteLeavesTheAncestorDeleted(t *testing.T) {
	resurrected := 0
	const rounds = 200
	for range rounds {
		ctx := context.Background()
		records, tenant, root, child := raceTree(t)
		u := newTestUsage(records)
		other := recordedSubject(ctx, t, u, subjectSpec{capID: uuid.New(), tenant: uuid.New()})

		holding, release := make(chan struct{}), make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { // B: holds the usage lock in onCharged, then barges with Delete
			defer wg.Done()
			_, _ = u.Charge(ctx, meter.ChargeRequest{Subject: other, Amount: unit}, func(context.Context, meter.Charged[struct{}]) error {
				close(holding)
				<-release
				return nil
			})
			_ = u.DeleteUsage(ctx, meter.DeleteUsageRequest{CapabilityID: root})
		}()
		<-holding
		go func() { // R: reads lineage now, parks on the usage lock
			defer wg.Done()
			_, _ = u.Charge(ctx, meter.ChargeRequest{Subject: newSubject(subjectSpec{capID: child, tenant: tenant}), Amount: unit}, nil)
		}()
		runtime.Gosched()
		dropRecord(records, root)
		close(release)
		wg.Wait()
		if _, err := u.GetUsage(ctx, root); !errors.Is(err, meter.ErrUsageNotFound) {
			resurrected++
		}
	}
	t.Logf("root row present after Delete in %d/%d rounds", resurrected, rounds)
	if resurrected > 0 {
		t.Errorf("Delete(root) undone by a Charge that read the lineage before the lock in %d/%d rounds", resurrected, rounds)
	}
}

// Same TOCTOU with Reserve: the new hold names root among its ancestors
// although Delete already stripped root from every hold, so Release later
// writes root's row back too.
func TestReserveRacingDeleteLeavesTheAncestorDeleted(t *testing.T) {
	resurrected := 0
	const rounds = 200
	for range rounds {
		ctx := context.Background()
		records, tenant, root, child := raceTree(t)
		u := newTestUsage(records)
		other := recordedSubject(ctx, t, u, subjectSpec{capID: uuid.New(), tenant: uuid.New()})

		holding, release := make(chan struct{}), make(chan struct{})
		var wg sync.WaitGroup
		var res meter.Reservation
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, _ = u.Charge(ctx, meter.ChargeRequest{Subject: other, Amount: unit}, func(context.Context, meter.Charged[struct{}]) error {
				close(holding)
				<-release
				return nil
			})
			_ = u.DeleteUsage(ctx, meter.DeleteUsageRequest{CapabilityID: root})
		}()
		<-holding
		go func() {
			defer wg.Done()
			res, _ = u.Reserve(ctx, meter.ReserveRequest{Subject: recordedSubject(ctx, t, u, subjectSpec{capID: child, tenant: tenant}), Amount: unit})
		}()
		runtime.Gosched()
		dropRecord(records, root)
		close(release)
		wg.Wait()
		_ = u.Release(ctx, meter.ReleaseRequest{Subject: recordedSubject(ctx, t, u, subjectSpec{capID: child, tenant: tenant}), ReservationID: res.ID})
		if _, err := u.GetUsage(ctx, root); !errors.Is(err, meter.ErrUsageNotFound) {
			resurrected++
		}
	}
	t.Logf("root row present after Delete+Release in %d/%d rounds", resurrected, rounds)
	if resurrected > 0 {
		t.Errorf("Delete(root) undone by a Reserve racing it in %d/%d rounds", resurrected, rounds)
	}
}

// Deterministic: PurgeOrphans drops root's row but, unlike Delete, leaves
// root among the ancestors of a surviving descendant's hold; settling that
// hold writes root's spend back into a row of a capability no longer on
// record.
func TestPurgeOrphansForgetsTheAncestorInHolds(t *testing.T) {
	ctx := context.Background()
	records, tenant, root, child := raceTree(t)
	u := newTestUsage(records)
	subject := newSubject(subjectSpec{capID: child, tenant: tenant})
	r, err := u.Reserve(ctx, meter.ReserveRequest{Subject: subject, Amount: 2 * unit})
	if err != nil {
		t.Fatal(err)
	}
	dropRecord(records, root) // Store.Purge took root, child still on record
	if got, err := u.PurgeOrphans(ctx, meter.PurgeOrphansRequest{}); err != nil || got.Purged != 1 {
		t.Fatalf("PurgeOrphans = %+v, %v", got, err)
	}
	if _, err := u.GetUsage(ctx, root); !errors.Is(err, meter.ErrUsageNotFound) {
		t.Fatalf("root row after PurgeOrphans: %v", err)
	}
	if _, err := u.Settle(ctx, meter.SettleRequest{Subject: subject, ReservationID: r.ID, Amount: unit}, nil); err != nil {
		t.Fatal(err)
	}
	if got, err := u.GetUsage(ctx, root); !errors.Is(err, meter.ErrUsageNotFound) {
		t.Errorf("root (purged, its row purged) after a descendant's Settle = %+v, %v; want ErrUsageNotFound", got, err)
	}
}
