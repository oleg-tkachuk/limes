package memstore

import (
	"bytes"
	"context"
	"slices"
	"uuid"

	"github.com/oleg-tkachuk/limes"
)

// RevokeMatching implements limes.BulkRevoker: under the store's lock, an
// entry for each matching capability that has none, in order of tenant and
// id, at most a batch of them.
func (s *Store) RevokeMatching(_ context.Context, req limes.RevokeMatchingRequest) (limes.RevokeMatchingResult, error) {
	if err := req.Validate(); err != nil {
		return limes.RevokeMatchingResult{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var due []uuid.UUID
	for id, c := range s.caps {
		if !s.isRevokedItselfLocked(id) && s.matchesLocked(c, req) {
			due = append(due, id)
		}
	}
	slices.SortFunc(due, func(a, b uuid.UUID) int {
		ta, tb := s.caps[a].Subject.TenantID, s.caps[b].Subject.TenantID
		if c := bytes.Compare(ta[:], tb[:]); c != 0 {
			return c
		}
		return bytes.Compare(a[:], b[:])
	})
	limit := req.BatchLimit()
	more := len(due) >= limit
	if len(due) > limit {
		due = due[:limit]
	}
	now := s.nowFn()
	var tenants []uuid.UUID
	for _, id := range due {
		s.revokeLocked(id, limes.Revocation{RevokedAt: now, Note: req.Note, Actor: req.Actor})
		if tenant := s.caps[id].Subject.TenantID; len(tenants) == 0 || tenants[len(tenants)-1] != tenant {
			tenants = append(tenants, tenant)
		}
	}
	return limes.RevokeMatchingResult{Revoked: int64(len(due)), Tenants: tenants, More: more}, nil
}

// matchesLocked reports whether c, of req's tenant or of any with
// AllTenants, matches every selector req names. s.mu must be held.
func (s *Store) matchesLocked(c limes.Capability, req limes.RevokeMatchingRequest) bool {
	return (req.AllTenants || c.Subject.TenantID == req.TenantID) &&
		s.selectedLocked(c, selectors{subject: req.Subject, onBehalfOf: req.OnBehalfOf, clientID: req.ClientID, session: req.Session})
}

// selectors are what RevokeMatching selects capabilities by, and the census
// and the listing filter by: each set one must match.
type selectors struct {
	subject, onBehalfOf, clientID, session string
}

// selectedLocked reports whether c matches every selector in sel that is
// set. s.mu must be held.
func (s *Store) selectedLocked(c limes.Capability, sel selectors) bool {
	onBehalfOf := ""
	if c.OnBehalfOf != nil {
		onBehalfOf = c.OnBehalfOf.Subject
	}
	return (sel.subject == "" || c.Subject.Subject == sel.subject) &&
		(sel.onBehalfOf == "" || onBehalfOf == sel.onBehalfOf) &&
		(sel.clientID == "" || c.ClientID == sel.clientID) &&
		(sel.session == "" || s.sessions[c.ID] == sel.session)
}

var _ limes.BulkRevoker = (*Store)(nil)
