package pgstore

import (
	"errors"
	"slices"
	"testing"
	"uuid"

	"github.com/oleg-tkachuk/limes"
)

// heldIDs is the ids of ancestors, in order.
func heldIDs(as []ancestor) []uuid.UUID {
	out := make([]uuid.UUID, len(as))
	for i, a := range as {
		out[i] = a.id
	}
	return out
}

// A lineage's rows come in id order: its ancestors are read back nearest
// first, and cut where a record was deleted between the walk and its lock,
// as one gone from the chain stops the ceilings it would have passed on.
func TestHeldLineage(t *testing.T) {
	self, parent, grand, great := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	row := func(id uuid.UUID, depth int) heldAncestor {
		return heldAncestor{ancestor: ancestor{id: id}, depth: depth}
	}
	got, err := heldLineage(self, []heldAncestor{row(grand, 2), row(self, 0), row(great, 3), row(parent, 1)})
	if err != nil || !slices.Equal(heldIDs(got), []uuid.UUID{parent, grand, great}) {
		t.Errorf("whole chain = %v, %v; want parent, grand, great", heldIDs(got), err)
	}
	got, err = heldLineage(self, []heldAncestor{row(self, 0), row(parent, 1), row(great, 3)})
	if err != nil || !slices.Equal(heldIDs(got), []uuid.UUID{parent}) {
		t.Errorf("chain with grand gone = %v, %v; want parent alone", heldIDs(got), err)
	}
	got, err = heldLineage(self, []heldAncestor{row(self, 0)})
	if err != nil || len(got) != 0 {
		t.Errorf("a root = %v, %v; want no ancestor", heldIDs(got), err)
	}
	if _, err := heldLineage(self, []heldAncestor{row(parent, 1)}); !errors.Is(err, limes.ErrNotFound) {
		t.Errorf("self not held: err = %v, want ErrNotFound", err)
	}
	if _, err := heldLineage(self, nil); !errors.Is(err, limes.ErrNotFound) {
		t.Errorf("nothing held: err = %v, want ErrNotFound", err)
	}
	deep := []heldAncestor{row(self, 0)}
	for depth := 1; depth <= limes.MaxDelegationDepth+1; depth++ {
		deep = append(deep, row(uuid.New(), depth))
	}
	if _, err := heldLineage(self, deep); !errors.Is(err, limes.ErrDelegationTooDeep) {
		t.Errorf("past the depth: err = %v, want ErrDelegationTooDeep", err)
	}
}
