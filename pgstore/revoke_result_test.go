package pgstore

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/oleg-tkachuk/limes"
)

// concurrentRevokes is how many revokes of one capability race.
const concurrentRevokes = 8

// Revokes of one capability racing each other: one writes the entry, every
// other is told it was revoked already and given that entry — read after it
// waited on the writer, not from a snapshot taken before.
func TestConcurrentRevokesReportOneWriter(t *testing.T) {
	f := newTenantFixture(t)
	ctx := WithTenant(t.Context(), f.mine)
	c := f.capability(f.mine)
	if err := f.records.Insert(ctx, limes.InsertRequest{Capability: c, IssuedBy: c.Subject}); err != nil {
		t.Fatal(err)
	}
	results := make([]limes.RevokeResult, concurrentRevokes)
	errs := make([]error, concurrentRevokes)
	var wg sync.WaitGroup
	for i := range concurrentRevokes {
		wg.Go(func() {
			results[i], errs[i] = f.records.Revoke(ctx, limes.RevokeRequest{CapabilityID: c.ID, Note: "race", Actor: "test"})
		})
	}
	wg.Wait()
	writers := 0
	for i, r := range results {
		if errs[i] != nil {
			t.Fatalf("revoke %d: %v", i, errs[i])
		}
		if !r.AlreadyRevoked {
			writers++
		}
		// The writer found it active; every other one found it revoked,
		// though it may have read the flag before the writer committed.
		if want := map[bool]limes.RecordDisposition{false: limes.RecordDispositionActive, true: limes.RecordDispositionRevoked}[r.AlreadyRevoked]; r.Before != want {
			t.Errorf("revoke %d: AlreadyRevoked %v, before %s; want %s", i, r.AlreadyRevoked, r.Before, want)
		}
		if !r.Revocation.RevokedAt.Equal(results[0].Revocation.RevokedAt) || r.Revocation.Note != "race" {
			t.Errorf("revoke %d reported %+v; revoke 0 %+v", i, r.Revocation, results[0].Revocation)
		}
	}
	if writers != 1 {
		t.Errorf("%d revokes reported writing the entry, want 1", writers)
	}
}

// sqlRevokeParentOpen writes a parent's revocation entry, whose trigger
// marks its child revoked, in a transaction left open.
const sqlRevokeParentOpen = `
INSERT INTO limes.revocations (capability_id, tenant_id, revoked_at, note, actor, cascaded)
VALUES ($1, $2, now(), 'ancestor', 'test', false)`

// A revoke of a child whose parent another transaction is revoking waits
// for that transaction, and reports the child revoked before it, as it was
// when its own entry was written: not active, from a read made before.
func TestRevokeWaitsForAnAncestorsRevoke(t *testing.T) {
	f := newTenantFixture(t)
	ctx := WithTenant(t.Context(), f.mine)
	ids := f.tree(t, root, 0)
	conn, err := owner.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	tx, err := conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(t.Context(), sqlSetSession, SettingAllTenants, SettingAllTenantsOn); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), sqlRevokeParentOpen, ids[0], f.mine); err != nil {
		t.Fatal(err)
	}
	done := make(chan limes.RevokeResult, 1)
	go func() {
		r, err := f.records.Revoke(ctx, limes.RevokeRequest{CapabilityID: ids[1], Note: "child", Actor: "test"})
		if err != nil {
			t.Error(err)
		}
		done <- r
	}()
	select {
	case r := <-done:
		t.Fatalf("the child's revoke returned %+v while its parent's was uncommitted", r)
	case <-time.After(watchQuiet):
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if r := <-done; r.AlreadyRevoked || r.Before != limes.RecordDispositionRevoked {
		t.Errorf("the child's revoke = %+v; want its own entry written, revoked before through its parent", r)
	}
}
