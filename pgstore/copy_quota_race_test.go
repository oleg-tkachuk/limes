package pgstore

import (
	"errors"
	"sync"
	"testing"
	"uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/internal/link"
	"github.com/oleg-tkachuk/limes/meter"
)

// raceRounds is how many times two children race for their root's last copy.
const raceRounds = 5

// Two children of a root whose quota leaves room for one copy, each adding
// one at once, count one between them: the root's usage row, which both
// calls lock, serialises them.
func TestTwoChildrenRaceForTheirRootsLastCopy(t *testing.T) {
	f := newTenantFixture(t)
	ctx := WithTenant(t.Context(), f.mine)
	insert := func(parent uuid.UUID, quota limes.CopyQuota) uuid.UUID {
		t.Helper()
		c := f.parent
		c.ID, c.ParentID = uuid.New(), parent
		c.Caveats.Copies = quota
		if err := f.records.Insert(ctx, limes.InsertRequest{Capability: c}); err != nil {
			t.Fatal(err)
		}
		return c.ID
	}
	for round := range raceRounds {
		root := insert(uuid.Nil(), limes.CopyQuota{Max: 1})
		children := []uuid.UUID{insert(root, limes.CopyQuota{Unlimited: true}), insert(root, limes.CopyQuota{Unlimited: true})}
		start := make(chan struct{})
		errs := make([]error, len(children))
		var wg sync.WaitGroup
		for i, child := range children {
			wg.Add(1)
			go func() {
				defer wg.Done()
				subject := link.NewMeterSubject(link.MeterSubjectFields{
					CapabilityID: child, TenantID: f.mine,
					Requests: limes.RequestCeiling{Unlimited: true}, Budget: limes.BudgetCeiling{Unlimited: true, UnitCode: testUnit},
					Copies:    []limes.CopyCeiling{{RevocationID: []byte(child.String()), Requests: &limes.RequestCeiling{Unlimited: true}}},
					CopyQuota: limes.CopyQuota{Unlimited: true},
				}).(limes.MeterSubject)
				<-start
				_, errs[i] = f.usage.Bump(ctx, meter.BumpRequest{Subject: subject})
			}()
		}
		close(start)
		wg.Wait()
		refused := 0
		for _, err := range errs {
			switch {
			case errors.Is(err, meter.ErrCopyLimitExceeded):
				refused++
			case err != nil:
				t.Fatalf("round %d: %v", round, err)
			}
		}
		if refused != 1 {
			t.Errorf("round %d: %d of two children refused under a root with room for one copy, want 1", round, refused)
		}
	}
}
