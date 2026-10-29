package pgstore

import (
	"context"
	"testing"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/pgstore/migrate"
)

// sqlFlagDrift is every record whose revoked column says other than
// limes.is_revoked, the walk up IsRevoked asks.
const sqlFlagDrift = `
SELECT c.id FROM limes.capabilities c, limes.is_revoked(c.id, 64) r
WHERE c.revoked <> (r.self OR r.in_chain)`

// flagDrift is every record of the store whose revoked column is wrong.
func flagDrift(t *testing.T) []uuid.UUID {
	t.Helper()
	rows, err := owner.Query(t.Context(), sqlFlagDrift)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		t.Fatal(err)
	}
	return ids
}

// flagOf is a record's revoked column.
func flagOf(t *testing.T, id uuid.UUID) bool {
	t.Helper()
	var revoked bool
	if err := owner.QueryRow(t.Context(), `SELECT revoked FROM limes.capabilities WHERE id = $1`, id).Scan(&revoked); err != nil {
		t.Fatal(err)
	}
	return revoked
}

// A record's revoked column is what IsRevoked answers, through every way a
// record is revoked: its own entry, an ancestor's, a cascade, and a record
// inserted under a revoked one.
func TestTheRevokedFlagFollowsEveryRevocation(t *testing.T) {
	f := newTenantFixture(t)
	ctx := WithTenant(t.Context(), f.mine)
	insert := func(parent uuid.UUID) uuid.UUID {
		t.Helper()
		c := f.capability(f.mine)
		c.ParentID = parent
		if err := f.records.Insert(ctx, limes.InsertRequest{Capability: c, IssuedBy: c.Subject}); err != nil {
			t.Fatalf("insert: %v", err)
		}
		return c.ID
	}
	revoke := func(id uuid.UUID, cascade bool) {
		t.Helper()
		if _, err := f.records.Revoke(ctx, limes.RevokeRequest{CapabilityID: id, Note: "flag", Actor: "test", CascadeChildren: cascade}); err != nil {
			t.Fatalf("revoke: %v", err)
		}
	}
	root := insert(uuid.Nil())
	child := insert(root)
	grandchild := insert(child)
	other := insert(uuid.Nil())
	otherChild := insert(other)
	kept := insert(uuid.Nil())

	revoke(child, false)
	revoke(other, true)
	revoke(child, false) // again: a no-op
	late := insert(grandchild)
	for name, want := range map[uuid.UUID]bool{root: false, child: true, grandchild: true, late: true, other: true, otherChild: true, kept: false} {
		if got := flagOf(t, name); got != want {
			t.Errorf("record %s: revoked %v, want %v", name, got, want)
		}
	}
	if drift := flagDrift(t); len(drift) > 0 {
		t.Errorf("records whose flag is not IsRevoked's answer: %v", drift)
	}
}

// markRevokedVersion is the migration that marks the records revoked
// before 015's triggers, and markRevokedName its file.
const (
	markRevokedVersion = 16
	markRevokedName    = "016_mark_revoked.sql"
)

// 016 marks the records revoked before 015's triggers: every record with
// an entry and every record below one, and no other.
func TestMigrationMarksTheRecordsRevokedBefore(t *testing.T) {
	f := newTenantFixture(t)
	ctx := WithTenant(t.Context(), f.mine)
	var ids []uuid.UUID
	parent := uuid.Nil()
	for range 5 {
		c := f.capability(f.mine)
		c.ParentID = parent
		if err := f.records.Insert(ctx, limes.InsertRequest{Capability: c, IssuedBy: c.Subject}); err != nil {
			t.Fatal(err)
		}
		ids, parent = append(ids, c.ID), c.ID
	}
	if _, err := f.records.Revoke(ctx, limes.RevokeRequest{CapabilityID: ids[2], Note: "before", Actor: "test"}); err != nil {
		t.Fatal(err)
	}
	// The records as they were before 015: no record marked.
	if _, err := owner.Exec(t.Context(), `UPDATE limes.capabilities SET revoked = false`); err != nil {
		t.Fatal(err)
	}
	steps, err := migrate.Steps()
	if err != nil {
		t.Fatal(err)
	}
	backfill := steps[markRevokedVersion-1]
	if backfill.Name != markRevokedName {
		t.Fatalf("step %d is %s, want %s", markRevokedVersion, backfill.Name, markRevokedName)
	}
	if _, err := owner.Exec(t.Context(), backfill.Migration); err != nil {
		t.Fatalf("016: %v", err)
	}
	for i, id := range ids {
		if got, want := flagOf(t, id), i >= 2; got != want {
			t.Errorf("record %d of the chain after 016: revoked %v, want %v", i, got, want)
		}
	}
	if drift := flagDrift(t); len(drift) > 0 {
		t.Errorf("records whose flag is not IsRevoked's answer after 016: %v", drift)
	}
}

// A record inserted while its parent is revoked is marked whichever comes
// first: the insert holding its parent's row FOR SHARE, as Insert does,
// makes the revocation wait and then mark it; a revocation holding the
// parent's row makes the insert wait and then inherit it.
func TestTheRevokedFlagSurvivesAnInsertBesideARevoke(t *testing.T) {
	for _, first := range []string{"insert", "revoke"} {
		t.Run(first+" first", func(t *testing.T) {
			f := newTenantFixture(t)
			ctx := t.Context()
			parent := f.capability(f.mine)
			if err := f.records.Insert(WithTenant(ctx, f.mine), limes.InsertRequest{Capability: parent, IssuedBy: parent.Subject}); err != nil {
				t.Fatal(err)
			}
			child := uuid.NewV7()
			inserting, revoking := begin(t, owner), begin(t, owner)
			insertChild := func() error {
				if _, err := inserting.Exec(ctx, sqlLockParent, parent.ID); err != nil {
					return err
				}
				_, err := inserting.Exec(ctx, `
INSERT INTO limes.capabilities (id, parent_id, tenant_id, principal_type, subject, expires_at, depth, capability, issued_by)
VALUES ($1, $2, $3, 'agent', 'agent:child', $4, 1, '{}', '{}')`, child, parent.ID, f.mine, time.Now().Add(time.Hour))
				return err
			}
			revokeParent := func() error {
				_, err := revoking.Exec(ctx, `
INSERT INTO limes.revocations (capability_id, tenant_id, revoked_at, note, actor, cascaded)
VALUES ($1, $2, now(), 'race', 'test', false)`, parent.ID, f.mine)
				return err
			}
			holder, waiter, holds, waits := inserting, revoking, insertChild, revokeParent
			if first == "revoke" {
				holder, waiter, holds, waits = revoking, inserting, revokeParent, insertChild
			}
			if err := holds(); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- waits() }()
			select {
			case err := <-done:
				t.Fatalf("the second went ahead of the first's lock: %v", err)
			case <-time.After(200 * time.Millisecond):
			}
			if err := holder.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if err := waiter.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if !flagOf(t, child) {
				t.Error("the child of a revoked parent is not marked revoked")
			}
			if drift := flagDrift(t); len(drift) > 0 {
				t.Errorf("records whose flag is not IsRevoked's answer: %v", drift)
			}
		})
	}
}

// begin is a transaction on a connection of its own from pool, rolled back
// when the test ends unless committed.
func begin(t *testing.T, pool *pgxpool.Pool) pgx.Tx {
	t.Helper()
	tx, err := pool.BeginTx(t.Context(), txOptions)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.WithoutCancel(t.Context())) })
	return tx
}

// capability is a root capability of tenant's, like the fixture's parent.
func (f tenantFixture) capability(tenant uuid.UUID) limes.Capability {
	c := f.parent
	c.ID, c.ParentID = uuid.NewV7(), uuid.Nil()
	c.Subject.TenantID, c.Subject.Subject = tenant, "agent:"+c.ID.String()
	return c
}
