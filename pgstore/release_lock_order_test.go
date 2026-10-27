package pgstore

import (
	"bytes"
	"context"
	"slices"
	"testing"
	"time"
	"uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/meter"
)

// sqlLockCounter locks one capability's counters, as Bump's lock does among
// the chain it counts.
const sqlLockCounter = `SELECT 1 FROM ` + schema + `.usage WHERE capability_id = $1 FOR UPDATE`

// sqlRenumberHold gives a hold another id, as the owner, so the test can
// choose the order ReleaseExpired takes its holds in.
const sqlRenumberHold = `UPDATE ` + schema + `.reservations SET id = $2 WHERE id = $1`

// ReleaseExpired locks the counters of every hold it releases together, in
// the one order every call takes, before it writes any: released hold by
// hold, a hold on the root of a chain whose ids sort middle, root, leaf took
// the root, and the next hold, on the leaf, then waited for the middle,
// which a request counted against the leaf held while waiting for the root.
// Bump takes no tenant aggregate, so nothing ordered it before the release.
func TestReleaseExpiredLocksEveryCounterInOneOrder(t *testing.T) {
	f := newTenantFixture(t)
	mine := WithTenant(context.Background(), f.mine)

	ids := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
	slices.SortFunc(ids, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
	mid, root, leaf := ids[0], ids[1], ids[2]
	expiry := f.clock.Now().Add(rlsTTL)
	for _, c := range []struct{ id, parent uuid.UUID }{{root, uuid.Nil()}, {mid, root}, {leaf, mid}} {
		capability := limes.Capability{
			ID: c.id, ParentID: c.parent, ExpiresAt: expiry,
			Subject: limes.Principal{Type: limes.PrincipalTypeAgent, TenantID: f.mine, Subject: "agent"},
			Caveats: limes.Caveats{Ops: []limes.Op{limes.OpGet}, Requests: limes.RequestCeiling{Unlimited: true}, Budget: limes.BudgetCeiling{Unlimited: true, UnitCode: testUnit}},
		}
		if err := f.records.Insert(mine, limes.InsertRequest{Capability: capability}); err != nil {
			t.Fatal(err)
		}
	}
	// A hold on the root and one on the leaf, the root's first in id order:
	// the order ReleaseExpired took them in.
	var holds [2]uuid.UUID
	for i, id := range []uuid.UUID{root, leaf} {
		r, err := f.usage.Reserve(mine, meter.ReserveRequest{Subject: meterSubject(id, f.mine), Amount: 1, TTL: time.Minute})
		if err != nil {
			t.Fatal(err)
		}
		holds[i] = r.ID
	}
	order := []uuid.UUID{uuid.New(), uuid.New()}
	slices.SortFunc(order, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
	for i, id := range holds {
		if _, err := owner.Exec(context.Background(), sqlRenumberHold, id, order[i]); err != nil {
			t.Fatal(err)
		}
	}
	f.clock.Advance(2 * time.Minute)

	// A request counted against the leaf, holding the middle's counters.
	bump := otherSide(t)
	if _, err := bump.Exec(context.Background(), sqlLockCounter, mid); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := f.usage.ReleaseExpired(WithAllTenants(context.Background()), meter.ReleaseExpiredRequest{})
		done <- err
	}()
	awaitLockWait(t)
	// The request goes on to the root's counters.
	if _, err := bump.Exec(context.Background(), sqlLockCounter, root); err != nil {
		t.Fatalf("the request locking the root's counters: %v", err)
	}
	if err := bump.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Errorf("ReleaseExpired: %v", err)
	}
	for _, id := range []uuid.UUID{root, leaf} {
		u, err := f.usage.GetUsage(mine, id)
		if err != nil {
			t.Fatal(err)
		}
		if u.ReservedAmount != 0 {
			t.Errorf("%s still holds %d", id, u.ReservedAmount)
		}
	}
}
