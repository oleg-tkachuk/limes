package limes

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// IssuanceStore is what an Issuer needs of storage: to record what it mints,
// and to know whether a parent it delegates from has been revoked.
type IssuanceStore interface {
	// Insert records a capability at issuance. The persisted row holds
	// the full claim set so admin tooling can show a capability without
	// parsing the JWT, and so audit can correlate without
	// keeping every issued token.
	//
	// req.IssuedBy is the principal that ASKED for the capability, which
	// is not its Subject (the principal it authorises) and not its Issuer
	// (the service that minted it). A delegated capability is requested by
	// its parent's holder; a root one by an operator. It is issuance
	// metadata, not a claim, so it travels beside the Capability rather
	// than in it, which would change the token's frozen wire format.
	//
	// An id already on record returns ErrAlreadyExists and changes nothing.
	// It refuses what InsertRequest.Validate refuses, a parent not on
	// record as ErrInvalidRequest, a child expiring after its parent — or
	// with no expiry under a parent that has one — or in another tenant as
	// ErrDelegationTooWide, and a child that would have
	// more than MaxDelegationDepth ancestors still on record — counted as
	// IsRevoked counts them — as ErrDelegationTooDeep: Purge and the
	// IsRevoked walk rely on a lineage that is whole, no deeper than the
	// walk reaches, expires from the leaves up and stays in one tenant, so a
	// store that scopes rows by tenant sees all of it. A refused record is
	// not written. A
	// store that knows tenants returns ErrUnknownTenant or ErrTenantDeleted
	// for a tenant it cannot mint for.
	Insert(ctx context.Context, req InsertRequest) error

	// IsRevoked reports whether the capability, or any ancestor of it
	// that is still on record, is in the revocation list. Answering for
	// the chain is what makes revoking an orchestrator stop the workers it
	// spawned. Verifiers gate every request on it, so implementations
	// should make the walk a single round trip (a recursive query),
	// bounded at MaxDelegationDepth ancestors: a capability with more
	// still on record returns ErrDelegationTooDeep, never false. An id not
	// on record is not revoked.
	IsRevoked(ctx context.Context, id uuid.UUID) (bool, error)
}

// RecordReader reads one capability's record. Store implements it; a caller
// that only reads records — StoredMeterSubject — asks for no more.
type RecordReader interface {
	// GetRecord reads back everything Insert and Revoke wrote about a
	// capability: the capability, who issued it, and its own revocation
	// entry if it has one. ErrNotFound when there is no record. For audit
	// and admin reads, and for a reporter that needs a capability's
	// ceilings; Verifier never calls it.
	GetRecord(ctx context.Context, id uuid.UUID) (Record, error)
}

// MaxDelegationDepth is the most ancestors a capability may have: how far
// IsRevoked walks, and how deep Delegate mints.
const MaxDelegationDepth = 64

// InsertRequest is a capability to record, and who asked for it.
type InsertRequest struct {
	Capability Capability
	IssuedBy   Principal
}

// Validate refuses a capability with no id, as ErrInvalidRequest: nothing
// could read it back, and the zero id is what a root's ParentID holds.
func (r InsertRequest) Validate() error {
	if r.Capability.ID == uuid.Nil {
		return invalidRequest("insert: the capability has no id")
	}
	return nil
}

// Store persists capability records and revocation entries. Consumers supply
// the implementation: memstore ships an in-memory one, and the module
// github.com/oleg-tkachuk/limes/pgstore one over PostgreSQL.
//
// Records are append-mostly: capabilities aren't mutated after issuance
// except via Revoke. Revocations are checked by the verifier on every
// request; put a CachedRevocationChecker in front to keep the per-call
// cost flat.
type Store interface {
	IssuanceStore
	RecordReader

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

	// Purge removes the capabilities expired for longer than the request's
	// retention (PurgeRequest.Retention) — their records, their revocation
	// entries and those of their Biscuit copies — at most req.Limit of
	// them, and reports how many records it removed. A housekeeping worker
	// calls it until a batch comes back short of the limit. The retention
	// is how long an expired capability stays on record for audit, and
	// never less than MinPurgeRetention: a verifier still accepts a token
	// for up to its Leeway past expiry, and a revocation purged before
	// that would let a revoked token through again.
	//
	// Purge never removes a capability while a child of it stays on record:
	// a child expires no later than its parent (Insert refuses one that
	// would not), and among records expiring together Purge takes the
	// deepest first. The IsRevoked walk and the Meter's lineage, which stop
	// at the first ancestor no longer on record, therefore miss no
	// revocation and no ceiling. Once records go, UsageHousekeeping.PurgeOrphans
	// releases what the meter still holds for them.
	Purge(ctx context.Context, req PurgeRequest) (PurgeResult, error)

	// ListByPrincipal lists the capabilities issued to one principal, in
	// ascending id order, a page at a time: at most Limit of them, and the
	// cursor of the next page, empty on the last. The cursor is the store's
	// own and opaque.
	ListByPrincipal(ctx context.Context, req ListByPrincipalRequest) (Page[Capability], error)
}

// ErrNotFound is the typed not-found return from Store.GetRecord. It lives in the
// core package rather than in a store implementation so that any consumer —
// in-memory, relational, or otherwise — can satisfy the Store contract using
// only what this package publishes.
//
// The verifier never returns it: an id not on record is simply not revoked.
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
	Note      string
	Actor     string
	// Cascaded reports that the entry was written by a cascading revoke —
	// of this capability or of an ancestor.
	Cascaded bool
	_        noCompare
}

// ErrAlreadyExists is Store.Insert given an id already on record.
var ErrAlreadyExists = errors.New("limes: already exists")

// Page sizes of a list: Limit 0 means DefaultListLimit, anything above
// MaxListLimit is clamped to it, and a negative Limit is the request's
// error (Validate).
const (
	DefaultListLimit = 50
	MaxListLimit     = 500
)

// PageLimit is the page size a store serves for a requested limit.
func PageLimit(requested int) int {
	switch {
	case requested <= 0: // a negative limit was refused by Validate
		return DefaultListLimit
	case requested > MaxListLimit:
		return MaxListLimit
	default:
		return requested
	}
}

// RevokeRequest is the input shape for Store.Revoke.
type RevokeRequest struct {
	CapabilityID uuid.UUID
	// Note is an operator-supplied label ("compromise", "rotation",
	// "policy-change"). Stored on the revocation row for audit.
	Note string
	// Actor identifies who triggered the revocation (a user UUID or
	// an automation principal). Stored on the row.
	Actor string
	// CascadeChildren records a revocation entry for every descendant as
	// well. Descendants stop verifying regardless; see Store.Revoke.
	CascadeChildren bool
}

// DefaultHousekeepingLimit is how many rows one housekeeping batch —
// Store.Purge, UsageHousekeeping.ReleaseExpired or PurgeOrphans — handles
// when the request names no limit.
const DefaultHousekeepingLimit = 1_000

// PurgeClockSkew is how far the clock of the store that purges may run ahead
// of a verifier's: the store measures expiry by its own clock, the verifier
// by its own, and a revocation must outlive the token on both.
const PurgeClockSkew = time.Minute

// MinPurgeRetention is the shortest retention Purge takes: no verifier
// accepts a token longer than MaxLeeway past its expiry, by a clock no more
// than PurgeClockSkew behind the store's, so a capability expired for longer
// can lose its revocation entry unseen.
const MinPurgeRetention = MaxLeeway + PurgeClockSkew

// PurgeRequest is one batch of Store.Purge.
type PurgeRequest struct {
	// ExpiredFor is how long a capability must have been expired to go.
	// Zero: MinPurgeRetention; anything shorter, negative included, is
	// refused.
	ExpiredFor time.Duration
	// Limit bounds the records removed. Zero: DefaultHousekeepingLimit; negative
	// is refused.
	Limit int
}

// Validate refuses a retention shorter than MinPurgeRetention, other than
// zero, and a negative limit.
func (r PurgeRequest) Validate() error {
	switch {
	case r.ExpiredFor != 0 && r.ExpiredFor < MinPurgeRetention:
		return invalidRequest("purge: retention %s is shorter than %s", r.ExpiredFor, MinPurgeRetention)
	case r.Limit < 0:
		return invalidRequest("purge: limit %d is negative", r.Limit)
	}
	return nil
}

// Retention is how long a capability must have been expired to go: ExpiredFor,
// or MinPurgeRetention when it is zero.
func (r PurgeRequest) Retention() time.Duration {
	if r.ExpiredFor == 0 {
		return MinPurgeRetention
	}
	return r.ExpiredFor
}

// PurgeResult is what one Purge removed.
type PurgeResult struct {
	// Purged is the number of capability records removed.
	Purged int64
}

// ListByPrincipalRequest is the input shape for Store.ListByPrincipal.
type ListByPrincipalRequest struct {
	// TenantID, PrincipalType and Subject name the principal; all three are
	// required (Validate).
	TenantID      uuid.UUID
	PrincipalType PrincipalType
	Subject       string
	// IncludeExpired and IncludeRevoked also list capabilities that have
	// expired — at their ExpiresAt, by the store's clock — or that are
	// revoked themselves (not through an ancestor).
	IncludeExpired bool
	IncludeRevoked bool
	// Cursor is the one the previous page returned; empty for the first.
	Cursor string
	// Limit is the page size; see PageLimit.
	Limit int
}

// Validate reports whether the request names a principal. Every error
// matches ErrInvalidRequest.
func (r ListByPrincipalRequest) Validate() error {
	switch {
	case r.TenantID == uuid.Nil:
		return invalidRequest("list: tenant id required")
	case r.PrincipalType.Validate() != nil:
		return invalidRequest("list: principal type %q is not a principal kind", r.PrincipalType)
	case r.Subject == "":
		return invalidRequest("list: subject required")
	case r.Limit < 0:
		return invalidRequest("list: limit %d is negative", r.Limit)
	}
	return nil
}
