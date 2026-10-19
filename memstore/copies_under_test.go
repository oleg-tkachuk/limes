package memstore

import (
	"testing"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes"
)

const (
	// heldCopy is the revocation id of the copy a test hold names, and
	// heldUnit the unit it holds in.
	heldCopy = "held"
	heldUnit = "USD"
)

// A new copy is counted under the ancestors it was admitted against, not
// under a lineage read again once admitted: the records lock apart, and an
// ancestor purged meanwhile would be missed.
func TestAPutCopyCountsUnderTheAncestorsAdmitted(t *testing.T) {
	u := newTestUsage(newTestStore())
	root, child := uuid.New(), uuid.New()
	u.mu.Lock()
	u.putCopyLocked(copyKeyOf(child, []byte(heldCopy)), copyCounter{}, []uuid.UUID{root})
	u.mu.Unlock()
	if got := u.copiesUnder[root]; got != 1 {
		t.Errorf("copies under the admitted ancestor, no longer on record = %d, want 1", got)
	}
	if got := u.copiesUnder[child]; got != 1 {
		t.Errorf("copies under the copy's own capability = %d, want 1", got)
	}
}

// Removing a hold whose copy was dropped meanwhile leaves the copy dropped:
// it neither comes back nor counts again under the ancestors.
func TestReleasingAHoldDoesNotBringBackADroppedCopy(t *testing.T) {
	u := newTestUsage(newTestStore())
	root, child := uuid.New(), uuid.New()
	h := hold{
		id: uuid.New(), capID: child, ancestors: []uuid.UUID{root}, tenantID: uuid.New(),
		amount: unit, unit: heldUnit, copies: []limes.CopyCeiling{{RevocationID: []byte(heldCopy)}},
	}
	u.mu.Lock()
	u.applyHoldLocked(h, -1)
	u.mu.Unlock()
	if hasKey(u.copies, copyKeyOf(child, []byte(heldCopy))) {
		t.Error("releasing a hold brought back the copy dropped under it")
	}
	if got := u.copiesUnder[root]; got != 0 {
		t.Errorf("copies under the ancestor after releasing the hold = %d, want 0", got)
	}
}
