package pgstore

import (
	"context"
	"slices"
	"testing"
	"uuid"

	"github.com/jackc/pgx/v5"

	"github.com/oleg-tkachuk/limes"
)

const (
	// overlappingCalls is how many metered calls hold one lineage at once.
	overlappingCalls = 3

	// keyShareMode is how pg_get_multixact_members names a FOR KEY SHARE
	// lock.
	keyShareMode = "keysh"

	// sqlRecordLockers is the mode of each transaction holding the record
	// $1 at once: the members of the MultiXact in its xmax.
	sqlRecordLockers = `
SELECT m.mode FROM limes.capabilities c, pg_get_multixact_members(c.xmax) m
WHERE c.id = $1`
)

// Metered calls overlapping on one lineage each hold its records for key
// share, and nothing stronger: the record's MultiXact, which overlapping
// holders make, names each of them as a key-share locker. A FOR SHARE
// would make the same MultiXact and also hold back the record's revoke
// (TestRevokeDoesNotWaitForAMeteredLineage); the cost kept is in
// docs/adr/0007-pgstore-components.md.
func TestOverlappingMeteredCallsHoldTheirRootForKeyShare(t *testing.T) {
	f := newTenantFixture(t)
	ctx := WithTenant(t.Context(), f.mine)
	root := f.parent
	root.ID, root.Subject.Subject = uuid.New(), "agent:root"
	leaf := root
	leaf.ID, leaf.ParentID, leaf.Subject.Subject = uuid.New(), root.ID, "agent:leaf"
	for _, c := range []limes.Capability{root, leaf} {
		if err := f.records.Insert(ctx, limes.InsertRequest{Capability: c}); err != nil {
			t.Fatal(err)
		}
	}
	for range overlappingCalls {
		held, err := owner.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = held.Rollback(context.WithoutCancel(t.Context())) })
		if _, err := held.Exec(t.Context(), sqlLineageHeld, leaf.ID, limes.MaxDelegationDepth); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := owner.Query(t.Context(), sqlRecordLockers, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	modes, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	want := slices.Repeat([]string{keyShareMode}, overlappingCalls)
	if !slices.Equal(modes, want) {
		t.Errorf("the root's lockers = %v, want %v", modes, want)
	}
}
