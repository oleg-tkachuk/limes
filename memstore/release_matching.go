package memstore

import (
	"bytes"
	"context"
	"slices"
	"uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/meter"
)

// ReleaseMatching implements meter.MatchingReleaser: under the store's lock,
// the open holds, in id order, of tenant's revoked capabilities that match
// req's selectors or were delegated from one that does, at most a batch.
func (s *UsageStore) ReleaseMatching(_ context.Context, req meter.ReleaseMatchingRequest) (meter.ReleaseMatchingResult, error) {
	if err := req.Validate(); err != nil {
		return meter.ReleaseMatchingResult{}, err
	}
	sel := selectors{subject: req.Subject, onBehalfOf: req.OnBehalfOf, clientID: req.ClientID, session: req.Session, runID: req.RunID}
	s.mu.Lock()
	defer s.mu.Unlock()
	var due []hold
	for _, h := range s.holds {
		if h.tenantID == req.TenantID && s.records.revokedUnderMatch(h.capID, sel) {
			due = append(due, h)
		}
	}
	slices.SortFunc(due, func(a, b hold) int { return bytes.Compare(a.id[:], b.id[:]) })
	limit := meter.HousekeepingLimit(req.Limit)
	more := len(due) >= limit
	if len(due) > limit {
		due = due[:limit]
	}
	for _, h := range due {
		s.applyHoldLocked(h, -1)
		delete(s.holds, h.id)
	}
	return meter.ReleaseMatchingResult{Released: int64(len(due)), More: more}, nil
}

// revokedUnderMatch reports whether id is on record and revoked, and it or
// an ancestor matches sel. A lineage it cannot walk whole is not released:
// a hold kept is a ceiling erring towards refusing spend.
func (s *Store) revokedUnderMatch(id uuid.UUID, sel selectors) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.caps[id]
	if !ok {
		return false
	}
	if revoked, err := s.revokedLocked(id); err != nil || !revoked {
		return false
	}
	ancestors, err := s.ancestorsLocked(id)
	if err != nil {
		return false
	}
	return slices.ContainsFunc(append([]limes.Capability{c}, ancestors...), func(a limes.Capability) bool {
		return s.selectedLocked(a, sel)
	})
}

var _ meter.MatchingReleaser = (*UsageStore)(nil)
