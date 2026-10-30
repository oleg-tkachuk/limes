package limes

import (
	"context"

	"uuid"
)

// RevokeMatchingRequest names the capabilities of one tenant, or of every
// tenant, to revoke together (docs/adr/0013-bulk-revocation.md,
// docs/adr/0014-bulk-revocation-across-tenants.md): every one on record that
// matches each selector given — at least one — and has no revocation entry
// of its own yet.
type RevokeMatchingRequest struct {
	// TenantID is the tenant whose capabilities are revoked; AllTenants
	// revokes in every tenant instead. Exactly one of them is required.
	TenantID uuid.UUID
	// AllTenants matches capabilities in every tenant, by ClientID and
	// Session alone: an authorization server's client and an identity
	// provider's session are named by the issuer, which knows no tenant, so
	// one may hold capabilities in many. Subject and OnBehalfOf name a
	// principal only within its tenant, and are refused beside it.
	AllTenants bool
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
	// RunID matches the agent run a capability was issued to
	// (Capability.Subject.Agent.RunID): a run gone wrong, whose capabilities
	// issued apart share no lineage. The consumer mints run ids, so they name
	// a run within its tenant alone, and AllTenants refuses it.
	RunID uuid.UUID
	// Note and Actor are stored for audit, as on RevokeRequest.
	Note, Actor string
	// Limit bounds the capabilities one call revokes. Zero:
	// DefaultHousekeepingLimit; negative is refused.
	Limit int
}

// Validate refuses a request with neither a tenant nor AllTenants, or both,
// no selector, a selector AllTenants does not take, or a negative limit.
func (r RevokeMatchingRequest) Validate() error {
	switch {
	case r.TenantID == uuid.Nil() && !r.AllTenants:
		return invalidRequest("revoke matching: a tenant id or AllTenants required")
	case r.TenantID != uuid.Nil() && r.AllTenants:
		return invalidRequest("revoke matching: tenant id %s beside AllTenants", r.TenantID)
	case r.Subject == "" && r.OnBehalfOf == "" && r.ClientID == "" && r.Session == "" && r.RunID == uuid.Nil():
		return invalidRequest("revoke matching: no selector; name a subject, an on-behalf-of, a client, a session or an agent run")
	case r.AllTenants && (r.Subject != "" || r.OnBehalfOf != "" || r.RunID != uuid.Nil()):
		return invalidRequest("revoke matching: AllTenants matches by client or session alone; a subject or an agent run names one tenant's")
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
	// Tenants are the tenants the call wrote entries in, in ascending
	// order, for an audit per tenant: the request's alone, or none, without
	// AllTenants.
	Tenants []uuid.UUID
	// More reports a full batch: more may match, so call again until it is
	// false. A capability issued meanwhile, by a client still holding a
	// token that can mint one, is caught by the next call.
	More bool
	_    noCompare
}

// BulkRevoker revokes every capability of a tenant that matches a subject,
// an on-behalf-of, a client, a session or an agent run — or of every tenant
// that matches a client or a session — in batches, as Revoke revokes one:
// an entry for each that has none, and every descendant stopped through its
// lineage. A store that records sessions (InsertRequest.Session) implements
// it; memstore and pgstore do.
type BulkRevoker interface {
	// RevokeMatching writes a revocation entry, not cascaded, for at most
	// req.BatchLimit() capabilities of req.TenantID — or, with AllTenants,
	// of every tenant — that match every selector req names and have no
	// entry of their own, in order of tenant and id, and reports how many,
	// in which tenants, and whether more may match. It refuses what
	// RevokeMatchingRequest.Validate refuses, a call scoped to another
	// tenant, and an AllTenants call under a scope that does not admit
	// writing every tenant, as ErrScopeMismatch.
	RevokeMatching(ctx context.Context, req RevokeMatchingRequest) (RevokeMatchingResult, error)
}
