package pgstore

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes"
)

// cycleListingBound is how long a listing of a record in a parent cycle may
// take: one that walked the cycle would never return.
const cycleListingBound = 5 * time.Second

// sqlMakeCycle points two records at each other as parents and marks them
// revoked with no entry of their own: what only a hand edit of the table
// could write.
const sqlMakeCycle = `
UPDATE limes.capabilities SET parent_id = CASE id WHEN $1 THEN $2::uuid ELSE $1::uuid END, revoked = true
WHERE id IN ($1, $2)`

// The walk up for a record's StoppedBy ends after limes.MaxDelegationDepth
// generations, as IsRevoked's does, so a parent cycle written by hand ends
// a listing rather than holding it for ever.
func TestRecordListingEndsInAParentCycle(t *testing.T) {
	f := newTenantFixture(t)
	ctx := WithTenant(t.Context(), f.mine)
	ids := make([]uuid.UUID, 2)
	for i := range ids {
		c := f.parent
		c.ID, c.Subject.Subject = uuid.New(), "agent:cycle"
		if err := f.records.Insert(ctx, limes.InsertRequest{Capability: c}); err != nil {
			t.Fatal(err)
		}
		ids[i] = c.ID
	}
	if _, err := owner.Exec(t.Context(), sqlMakeCycle, ids[0], ids[1]); err != nil {
		t.Fatal(err)
	}
	census, err := NewCensus(Config{DB: app, Now: f.clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	bounded, cancel := context.WithTimeout(ctx, cycleListingBound)
	defer cancel()
	l, err := census.GetRecordListing(bounded, ids[0])
	if err != nil {
		t.Fatalf("the listing of a record in a parent cycle: %v", err)
	}
	if l.Disposition != limes.RecordDispositionRevoked || l.StoppedBy != nil {
		t.Errorf("listing = %+v; want revoked, stopped by no entry", l)
	}
}
