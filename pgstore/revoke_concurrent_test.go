package pgstore

import (
	"sync"
	"testing"
	"uuid"

	"github.com/oleg-tkachuk/limes"
)

// concurrentRevokeRounds is how many capabilities the concurrent-revoke
// check revokes twice at once: a deadlock between the two is a race, which
// enough rounds reach.
const concurrentRevokeRounds = 30

// Two revokes of one capability at once never deadlock: one writes the
// entry, the other waits for it and reports it, AlreadyRevoked.
func TestConcurrentRevokesOfOneCapability(t *testing.T) {
	f := newTenantFixture(t)
	ctx := WithTenant(t.Context(), f.mine)
	for range concurrentRevokeRounds {
		c := f.parent
		c.ID, c.Subject.Subject = uuid.New(), "agent:twice"
		if err := f.records.Insert(ctx, limes.InsertRequest{Capability: c}); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		start := make(chan struct{})
		results := make([]limes.RevokeResult, 2)
		errs := make([]error, 2)
		for i := range results {
			wg.Go(func() {
				<-start
				results[i], errs[i] = f.records.Revoke(ctx, limes.RevokeRequest{CapabilityID: c.ID})
			})
		}
		close(start)
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Fatalf("revoke %d of two at once: %v", i, err)
			}
		}
		if results[0].AlreadyRevoked == results[1].AlreadyRevoked {
			t.Fatalf("two revokes at once: AlreadyRevoked %v and %v; want exactly one first", results[0].AlreadyRevoked, results[1].AlreadyRevoked)
		}
	}
}
