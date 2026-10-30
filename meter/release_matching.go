package meter

import (
	"context"
	"uuid"
)

// ReleaseMatchingRequest names the holds ReleaseMatching releases: those of
// the revoked capabilities of one tenant that match each selector set, by
// the rules limes.RevokeMatchingRequest selects by, and of every capability
// delegated from one (docs/adr/0015-release-matching-holds.md).
type ReleaseMatchingRequest struct {
	// TenantID is the tenant whose holds are released. Required: a
	// revocation across tenants reports the tenants it wrote in
	// (limes.RevokeMatchingResult.Tenants), one call each.
	TenantID uuid.UUID
	// Subject, OnBehalfOf, ClientID, Session and RunID select as on
	// limes.RevokeMatchingRequest; at least one is required.
	Subject, OnBehalfOf, ClientID, Session string
	RunID                                  uuid.UUID
	// Limit bounds the holds one call releases. Zero:
	// DefaultHousekeepingLimit; negative is refused.
	Limit int
}

// Validate refuses a request with no tenant, no selector, or a negative
// limit, as ErrInvalidRequest.
func (r ReleaseMatchingRequest) Validate() error {
	switch {
	case r.TenantID == uuid.Nil():
		return invalidRequest("release matching: no tenant")
	case r.Subject == "" && r.OnBehalfOf == "" && r.ClientID == "" && r.Session == "" && r.RunID == uuid.Nil():
		return invalidRequest("release matching: no selector; name a subject, an on-behalf-of, a client, a session or an agent run")
	}
	return validateHousekeepingLimit("release matching", r.Limit)
}

// ReleaseMatchingResult is what one ReleaseMatching call did.
type ReleaseMatchingResult struct {
	// Released counts the holds the call released.
	Released int64
	// More reports a full batch: more may match, so call again until it is
	// false.
	More bool
	_    noCompare
}

// MatchingReleaser releases at once the holds of capabilities a bulk
// revocation stopped, which would otherwise count against the ceilings
// until they expire (ReleaseExpired). A usage store over the records the
// revocation wrote implements it; memstore's and pgstore's do.
type MatchingReleaser interface {
	// ReleaseMatching releases at most req's Limit (HousekeepingLimit) open
	// holds, in id order, each of a capability that is revoked and that
	// matches req's selectors or was delegated from one that does,
	// returning each hold's amount to its capability, its ancestors and its
	// tenant as Release does, and reports how many and whether the batch
	// was full. A hold of a live capability is never released: its cost may
	// still come. It refuses what ReleaseMatchingRequest.Validate refuses,
	// and a call scoped to another tenant as limes.ErrScopeMismatch.
	ReleaseMatching(ctx context.Context, req ReleaseMatchingRequest) (ReleaseMatchingResult, error)
}
