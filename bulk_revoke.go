package limes

import (
	"context"

	"uuid"
)

// RevokeMatchingRequest names the capabilities of one tenant to revoke
// together (docs/adr/0013-bulk-revocation.md): every one on record that
// matches each selector given — at least one — and has no revocation entry
// of its own yet.
type RevokeMatchingRequest struct {
	// TenantID is the tenant whose capabilities are revoked. Required.
	TenantID uuid.UUID
	// Subject matches a capability's Subject.Subject, the principal it
	// authorises: a user who leaves, an agent retired.
	Subject string
	// OnBehalfOf matches the principal a capability acts for
	// (Capability.OnBehalfOf.Subject): every agent acting for a user.
	OnBehalfOf string
	// ClientID matches a capability's ClientID: a client compromised.
	ClientID string
	// Session matches the identity provider's session a capability was
	// issued under (InsertRequest.Session): one the provider ended, by OIDC
	// back-channel logout say.
	Session string
	// Note and Actor are stored for audit, as on RevokeRequest.
	Note, Actor string
	// Limit bounds the capabilities one call revokes. Zero:
	// DefaultHousekeepingLimit; negative is refused.
	Limit int
}

// Validate refuses a request with no tenant, no selector, or a negative
// limit.
func (r RevokeMatchingRequest) Validate() error {
	switch {
	case r.TenantID == uuid.Nil():
		return invalidRequest("revoke matching: no tenant")
	case r.Subject == "" && r.OnBehalfOf == "" && r.ClientID == "" && r.Session == "":
		return invalidRequest("revoke matching: no selector; name a subject, an on-behalf-of, a client or a session")
	case r.Limit < 0:
		return invalidRequest("revoke matching: limit %d is negative", r.Limit)
	}
	return nil
}

// BatchLimit is Limit, or DefaultHousekeepingLimit when it is zero.
func (r RevokeMatchingRequest) BatchLimit() int {
	if r.Limit == 0 {
		return DefaultHousekeepingLimit
	}
	return r.Limit
}

// RevokeMatchingResult is what one RevokeMatching call did.
type RevokeMatchingResult struct {
	// Revoked counts the revocation entries the call wrote.
	Revoked int64
	// More reports a full batch: more may match, so call again until it is
	// false. A capability issued meanwhile, by a client still holding a
	// token that can mint one, is caught by the next call.
	More bool
	_    noCompare
}

// BulkRevoker revokes every capability of a tenant that matches a subject,
// an on-behalf-of, a client or a session, in batches, as Revoke revokes one:
// an entry for each that has none, and every descendant stopped through its
// lineage. A store that records sessions (InsertRequest.Session) implements
// it; memstore and pgstore do.
type BulkRevoker interface {
	// RevokeMatching writes a revocation entry, not cascaded, for at most
	// req.BatchLimit() capabilities of req.TenantID that match every
	// selector req names and have no entry of their own, in id order, and
	// reports how many and whether the batch was full. It refuses what
	// RevokeMatchingRequest.Validate refuses, and a call scoped to another
	// tenant as ErrScopeMismatch.
	RevokeMatching(ctx context.Context, req RevokeMatchingRequest) (RevokeMatchingResult, error)
}
