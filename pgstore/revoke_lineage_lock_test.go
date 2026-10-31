package pgstore

import (
	"context"
	"testing"
	"time"
	"uuid"

	"github.com/oleg-tkachuk/limes"
)

// revokeBehindLineageBound is how long a revoke may take while a metered
// call holds the capability's lineage: a revoke that waited for it would
// wait until the test ends.
const revokeBehindLineageBound = 2 * time.Second

// A metered call holds its lineage's records (sqlLineageHeld) until it
// commits. A revoke of one of them does not wait for it: were it to, calls
// overlapping one another would hold the record without a gap, and its
// bearer could keep the revoke from ever being written.
func TestRevokeDoesNotWaitForAMeteredLineage(t *testing.T) {
	f := newTenantFixture(t)
	ctx := WithTenant(t.Context(), f.mine)
	c := f.parent
	c.ID, c.Subject.Subject = uuid.New(), "agent:live"
	if err := f.records.Insert(ctx, limes.InsertRequest{Capability: c}); err != nil {
		t.Fatal(err)
	}
	held, err := owner.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Rollback(context.WithoutCancel(t.Context())) }()
	if _, err := held.Exec(t.Context(), sqlLineageHeld, c.ID, limes.MaxDelegationDepth); err != nil {
		t.Fatal(err)
	}
	revokeCtx, cancel := context.WithTimeout(ctx, revokeBehindLineageBound)
	defer cancel()
	r, err := f.records.Revoke(revokeCtx, limes.RevokeRequest{CapabilityID: c.ID})
	if err != nil {
		t.Fatalf("revoke while a metered call holds the lineage: %v", err)
	}
	if r.AlreadyRevoked || r.PriorDisposition != limes.RecordDispositionActive {
		t.Errorf("revoke = %+v; want a first revocation of an active capability", r)
	}
}
