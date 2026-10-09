package limes

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// Store persists capability records and revocation entries. Consumers supply
// the implementation; memstore ships an in-memory one and the reference
// deployment supplies a relational one.
//
// Records are append-mostly: capabilities aren't mutated after issuance
// except via Revoke. Revocations are checked by the verifier on every
// request; put a CachedRevocationChecker in front to keep the per-call
// cost flat.
type Store interface {
	// Insert records a capability at issuance. The persisted row holds
	// the full claim set so admin tooling can show a capability without
	// parsing the JWT, and so audit can correlate without
	// keeping every issued token.
	//
	// issuedBy is the principal that ASKED for the capability, which is not
	// c.Subject (the principal it authorises) and not c.Issuer (the service
	// that minted it). A delegated capability is requested by its parent's
	// holder; a root one by an operator. Passed as an argument rather than
	// carried on Capability because it is issuance metadata, not a claim —
	// putting it in the struct would change the token's frozen wire format.
	//
	// An id already on record returns ErrAlreadyExists and changes nothing.
	// A store that knows tenants returns ErrUnknownTenant or
	// ErrTenantDeleted for a tenant it cannot mint for.
	Insert(ctx context.Context, c Capability, issuedBy Principal) error

	// Get fetches a capability record by ID. Returns ErrNotFound when
	// no row exists; callers normally treat that as ErrInvalidSignature
	// (token forgery) rather than a missing entity.
	//
	// ErrNotFound is defined in THIS package (below), not in any store
	// implementation — a third party writing its own Store must have a
	// sentinel to return without importing someone else's persistence.
	Get(ctx context.Context, id uuid.UUID) (Capability, error)

	// GetRecord reads back everything Insert and Revoke wrote about a
	// capability: the capability, who issued it, and its own revocation
	// entry if it has one. ErrNotFound as Get. Get stays the read on the
	// verification path; this one is for audit and admin tooling.
	GetRecord(ctx context.Context, id uuid.UUID) (Record, error)

	// IsRevoked reports whether the capability, or any ancestor of it
	// that is still on record, is in the revocation list. Answering for
	// the chain is what makes revoking an orchestrator stop the workers it
	// spawned. Verifiers gate every request on it, so implementations
	// should make the walk a single round trip (a recursive query) and
	// bound its depth. An id not on record is not revoked.
	IsRevoked(ctx context.Context, id uuid.UUID) (bool, error)

	// Revoke adds an entry to the revocation list. Idempotent: a
	// duplicate revoke is a no-op. An id not on record — or not visible
	// to the caller — returns ErrNotFound and writes nothing, so a caller
	// is never told "revoked" about a capability nothing stopped.
	//
	// Every descendant stops verifying either way, because IsRevoked
	// walks the chain. CascadeChildren=true additionally writes a
	// revocation entry for each descendant, so the audit trail names
	// every capability that was stopped, not only the one an operator
	// picked.
	Revoke(ctx context.Context, req RevokeRequest) error

	// PurgeExpired drops the revocation entries — of capabilities and of
	// Biscuit copies — of every capability expired for at least
	// expiredFor, and returns how many it dropped. The capability records
	// stay: an expired token fails verification on its expiry alone, so
	// its revocation entry no longer stops anything. Run by the
	// housekeeping worker so the revocation lists stay bounded.
	PurgeExpired(ctx context.Context, expiredFor time.Duration) (int64, error)

	// ListByPrincipal lists the capabilities issued to one principal, in
	// ascending id order, a page at a time: it returns at most Limit of
	// them and a cursor that is empty on the last page and otherwise
	// fetches the next one. The cursor is opaque.
	ListByPrincipal(ctx context.Context, req ListByPrincipalRequest) ([]Capability, string, error)
}

// ErrNotFound is the typed not-found return from Store.Get. It lives in the
// core package rather than in a store implementation so that any consumer —
// in-memory, relational, or otherwise — can satisfy the Store contract using
// only what this package publishes.
//
// Verifiers map it to an invalid-token response: a syntactically valid token
// whose record is absent was forged or purged, which is an authentication
// failure, not a missing entity.
var ErrNotFound = errors.New("limes: not found")

// Record is a capability as the Store holds it.
type Record struct {
	Capability Capability
	// IssuedBy is the principal Insert was given.
	IssuedBy Principal
	// Revocation is the capability's own revocation entry; nil when it has
	// none. A capability stopped only through an ancestor has none — ask
	// IsRevoked whether it verifies.
	Revocation *Revocation
}

// Revocation is one entry of the revocation list, as Revoke wrote it.
type Revocation struct {
	RevokedAt time.Time
	Reason    string
	Actor     string
	// Cascade reports that the entry was written by a cascading revoke —
	// of this capability or of an ancestor.
	Cascade bool
}

// ErrAlreadyExists is Store.Insert given an id already on record.
var ErrAlreadyExists = errors.New("limes: already exists")

// Page sizes of ListByPrincipal: Limit 0 or below means DefaultListLimit,
// and anything above MaxListLimit is clamped to it.
const (
	DefaultListLimit = 50
	MaxListLimit     = 500
)

// PageLimit is the page size a store serves for a requested limit.
func PageLimit(requested int32) int32 {
	switch {
	case requested <= 0:
		return DefaultListLimit
	case requested > MaxListLimit:
		return MaxListLimit
	default:
		return requested
	}
}

// RevokeRequest is the input shape for Store.Revoke.
type RevokeRequest struct {
	ID uuid.UUID
	// Reason is an operator-supplied label ("compromise", "rotation",
	// "policy-change"). Stored on the revocation row for audit.
	Reason string
	// Actor identifies who triggered the revocation (a user UUID or
	// an automation principal). Stored on the row.
	Actor string
	// CascadeChildren records a revocation entry for every descendant as
	// well. Descendants stop verifying regardless; see Store.Revoke.
	CascadeChildren bool
}

// ListByPrincipalRequest is the input shape for Store.ListByPrincipal.
type ListByPrincipalRequest struct {
	// TenantID, PrincipalType and Subject name the principal; all three are
	// required (Validate).
	TenantID      uuid.UUID
	PrincipalType PrincipalType
	Subject       string
	// IncludeExpired and IncludeRevoked also list capabilities that have
	// expired, or that are revoked themselves (not through an ancestor).
	IncludeExpired bool
	IncludeRevoked bool
	// Cursor is the one the previous page returned; empty for the first.
	Cursor string
	// Limit is the page size; see PageLimit.
	Limit int32
}

// Validate reports whether the request names a principal. Every error
// matches ErrInvalidRequest.
func (r ListByPrincipalRequest) Validate() error {
	switch {
	case r.TenantID == uuid.Nil:
		return invalidRequest("list: tenant id required")
	case r.PrincipalType == "":
		return invalidRequest("list: principal type required")
	case r.Subject == "":
		return invalidRequest("list: subject required")
	}
	return nil
}
