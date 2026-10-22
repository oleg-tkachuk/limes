package pgstore

import (
	"sync"
	"testing"

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
		if !r.Revocation.RevokedAt.Equal(results[0].Revocation.RevokedAt) || r.Revocation.Note != "race" {
			t.Errorf("revoke %d reported %+v; revoke 0 %+v", i, r.Revocation, results[0].Revocation)
		}
	}
	if writers != 1 {
		t.Errorf("%d revokes reported writing the entry, want 1", writers)
	}
}
