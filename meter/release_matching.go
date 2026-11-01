package meter

import (
	"context"
	"uuid"
)

// ReleaseMatchingRequest names the holds ReleaseMatching releases: those of
// the revoked capabilities of one tenant, or of every tenant, that match
// each selector set, by the rules limes.RevokeMatchingRequest selects by,
// or that CapabilityID names, and of every capability delegated from one
// (docs/adr/0015-release-matching-holds.md).
type ReleaseMatchingRequest struct {
	// TenantID is the tenant whose holds are released; AllTenants releases
	// them in every tenant instead. Exactly one of the two is required.
	TenantID uuid.UUID
	// AllTenants releases the holds in every tenant, by ClientID and Session
	// alone, as limes.RevokeMatchingRequest.AllTenants revokes. It names no
	// tenant, so a call after a revocation across tenants needs no list of
	// the tenants it wrote in: one whose release failed is retried as it
	// was, though the revocation, repeated, revokes nothing.
	AllTenants bool
	// Subject, OnBehalfOf, ClientID, Session and RunID select as on
	// limes.RevokeMatchingRequest; at least one of them or CapabilityID is
	// required.
	Subject, OnBehalfOf, ClientID, Session string
	RunID                                  uuid.UUID
	// CapabilityID selects one capability by its id, as after
	// limes.Revoker.Revoke of that one: its holds and those of the
	// capabilities delegated from it. It names one tenant's.
	CapabilityID uuid.UUID
	// Limit bounds the holds one call releases. Zero:
	// DefaultHousekeepingLimit; negative is refused.
	Limit int
}

// Validate refuses, as ErrInvalidRequest, a request with neither a tenant
// nor AllTenants, or both, no selector, a selector AllTenants does not take,
// or a negative limit.
func (r ReleaseMatchingRequest) Validate() error {
	switch {
	case r.TenantID == uuid.Nil() && !r.AllTenants:
		return invalidRequest("release matching: a tenant id or AllTenants required")
	case r.TenantID != uuid.Nil() && r.AllTenants:
		return invalidRequest("release matching: tenant id %s beside AllTenants", r.TenantID)
	case r.Subject == "" && r.OnBehalfOf == "" && r.ClientID == "" && r.Session == "" && r.RunID == uuid.Nil() && r.CapabilityID == uuid.Nil():
		return invalidRequest("release matching: no selector; name a subject, an on-behalf-of, a client, a session, an agent run or a capability")
	case r.AllTenants && (r.Subject != "" || r.OnBehalfOf != "" || r.RunID != uuid.Nil() || r.CapabilityID != uuid.Nil()):
		return invalidRequest("release matching: AllTenants matches by client or session alone; a subject, an agent run or a capability names one tenant's")
	}
	return validateHousekeepingLimit("release matching", r.Limit)
}

// ReleaseMatchingResult is what one ReleaseMatching call did.
type ReleaseMatchingResult struct {
	// Released counts the holds the call released.
	Released int64
	// SettledLeases counts the leases of matching revoked capabilities the
	// call settled (Leaser).
	SettledLeases int64
	// Tenants are the tenants the call released holds in, ascending: the
	// request's alone, or, with AllTenants, each one a hold was in.
	Tenants []uuid.UUID
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
	// a call scoped to another tenant as limes.ErrScopeMismatch, and an
	// AllTenants call under a scope that does not admit writing every
	// tenant as limes.ErrScopeMismatch too.
	ReleaseMatching(ctx context.Context, req ReleaseMatchingRequest) (ReleaseMatchingResult, error)
}
