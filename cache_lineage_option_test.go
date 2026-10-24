package limes_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/memstore"
)

// embeddingStore decorates a store by embedding limes.Store, which has no
// RevocationLineage to promote.
type embeddingStore struct{ limes.Store }

// forwardingStore decorates a store and forwards RevocationLineage.
type forwardingStore struct {
	limes.Store
	lineage limes.RevocationLineageLookup
}

func (f forwardingStore) RevocationLineage(ctx context.Context, id uuid.UUID) (limes.RevocationAnswer, error) {
	return f.lineage.RevocationLineage(ctx, id)
}

// Each upstream under RequireLineage and without it: one that names
// lineage is accepted either way and clears narrowly, one that does not is
// refused when lineage is required and clears whole otherwise, and Stats
// says which.
func TestRevocationCacheLineageOfEachUpstream(t *testing.T) {
	ctx := context.Background()
	for name, tc := range map[string]struct {
		upstream func(*memstore.Store) limes.RevocationLookup
		lineage  bool
	}{
		"the store":                      {func(s *memstore.Store) limes.RevocationLookup { return s }, true},
		"a decorator embedding Store":    {func(s *memstore.Store) limes.RevocationLookup { return embeddingStore{s} }, false},
		"a decorator forwarding lineage": {func(s *memstore.Store) limes.RevocationLookup { return forwardingStore{s, s} }, true},
	} {
		for _, require := range []bool{false, true} {
			s := newExampleStack()
			c, _ := s.issue()
			cache, err := limes.NewRevocationCache(limes.RevocationCacheConfig{
				Capabilities: tc.upstream(s.records), RequireLineage: require, TTL: cacheLongerThanTheTest,
			})
			if require && !tc.lineage {
				if !errors.Is(err, limes.ErrNoRevocationLineage) {
					t.Errorf("%s, lineage required: err = %v, want ErrNoRevocationLineage", name, err)
				}
				continue
			}
			if err != nil {
				t.Fatalf("%s, lineage required %v: %v", name, require, err)
			}
			if _, err := cache.IsRevoked(ctx, c.ID); err != nil {
				t.Fatal(err)
			}
			cache.ClearRevoked(uuid.New())
			st := cache.Stats()
			if st.Lineage != tc.lineage {
				t.Errorf("%s, lineage required %v: Stats.Lineage = %v, want %v", name, require, st.Lineage, tc.lineage)
			}
			if whole := st.Clears == 1; whole == tc.lineage {
				t.Errorf("%s, lineage required %v: ClearRevoked of an unrelated id cleared whole %v", name, require, whole)
			}
			if fallback := st.ClearsWithoutLineage == 1; fallback == tc.lineage {
				t.Errorf("%s, lineage required %v: %d ClearRevoked counted as clearing whole for want of lineage", name, require, st.ClearsWithoutLineage)
			}
			// A Clear asked for is no fallback.
			cache.Clear()
			if after := cache.Stats(); after.ClearsWithoutLineage != st.ClearsWithoutLineage || after.Clears != st.Clears+1 {
				t.Errorf("%s: a Clear moved ClearsWithoutLineage from %d to %d, Clears from %d to %d", name, st.ClearsWithoutLineage, after.ClearsWithoutLineage, st.Clears, after.Clears)
			}
		}
	}
}
