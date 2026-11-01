package meter

import (
	"context"
	"time"
	"uuid"

	"github.com/oleg-tkachuk/limes"
)

// DefaultLeaseTTL is how long a lease lasts when the request does not say.
const DefaultLeaseTTL = 5 * time.Minute

// LeaseRequest asks for budget a capability holds of its chain in advance
// (docs/adr/0017-budget-leases.md).
type LeaseRequest struct {
	// Subject is the leaseholder (VerifiedCapability.MeterSubject).
	Subject limes.MeterSubject
	// Amount to lease, in the subject's unit, added to an open lease's.
	// Within 0..MaxNanos; a lease of no amount covers no Charge.
	Amount limes.Nanos
	// Requests to lease, added to an open lease's; a lease of none covers
	// no Bump. Negative is ErrInvalidRequest, and so is a request of no
	// amount and no requests.
	Requests int64
	// TTL is how long the lease lasts unless settled, at most until its
	// capability expires; a lease topped up lasts until the later of its
	// end and this one. 0 = DefaultLeaseTTL; negative is ErrInvalidRequest.
	TTL time.Duration
}

// Validate refuses an amount out of range, negative requests, a lease of
// no amount and no requests, a subject that names no capability, no tenant
// or no unit, or an unknown unit, a Biscuit copy, and a negative TTL. A
// copy's charges and bumps never go under a lease, so a lease of one would
// hold its chain's budget past the copy's own ceilings.
func (r LeaseRequest) Validate() error {
	if err := r.Amount.Validate(); err != nil {
		return err
	}
	if r.Requests < 0 {
		return invalidRequest("lease of %d requests", r.Requests)
	}
	if r.Amount == 0 && r.Requests == 0 {
		return invalidRequest("lease of no amount and no requests")
	}
	if err := r.Subject.ValidateAmounts(); err != nil {
		return err
	}
	if len(r.Subject.Copies()) > 0 {
		return invalidRequest("lease of a Biscuit copy of %s", r.Subject.CapabilityID())
	}
	if r.TTL < 0 {
		return invalidRequest("lease TTL %s is negative", r.TTL)
	}
	return nil
}

// SettleLeaseRequest ends the lease of a capability.
type SettleLeaseRequest struct {
	// Subject is the leaseholder.
	Subject limes.MeterSubject
}

// Validate refuses a subject that names no capability or no tenant.
func (r SettleLeaseRequest) Validate() error {
	if r.Subject.CapabilityID() == uuid.Nil() || r.Subject.TenantID() == uuid.Nil() {
		return invalidRequest("settle lease: a subject with a capability and a tenant required")
	}
	return nil
}

// Lease is the budget a capability holds of its chain: Amount held against
// every ancestor and the tenant, of which Spent is spent.
type Lease struct {
	// ID is this lease's, from its first Lease until it is settled; a
	// lease opened after it has another.
	ID           uuid.UUID
	CapabilityID uuid.UUID
	TenantID     uuid.UUID
	UnitCode     string
	// Amount is what the lease holds, every top-up added; Spent is what
	// its capability and those delegated from it charged under it.
	Amount, Spent limes.Nanos
	// Requests is how many requests the lease holds, every top-up added;
	// RequestsSpent how many its capability and those delegated from it
	// made under it.
	Requests, RequestsSpent int64
	// ExpiresAt is when the lease ends unless settled first; a charge
	// after it goes through the whole chain.
	ExpiresAt time.Time
	_         noCompare
}

// Remaining is what the lease can still be charged.
func (l Lease) Remaining() limes.Nanos {
	return l.Amount - l.Spent
}

// RequestsRemaining is how many requests the lease can still be bumped.
func (l Lease) RequestsRemaining() int64 {
	return l.Requests - l.RequestsSpent
}

// Leaser leases budget and requests to a capability, so that its charges
// and bumps, and those of the capabilities delegated from it, lock its own
// counters and not those of its ancestors or its tenant
// (docs/adr/0017-budget-leases.md). A usage store implements it; memstore's
// and pgstore's do.
//
// A lease holds its amount against every ancestor and the tenant, and its
// requests against every ancestor (ReservedRequests), at once, checking
// every ceiling then, so a charge or a bump under it checks only the
// capabilities below the leaseholder and the lease's balance; what it spent
// reaches the chain when it is settled. Every path
// that does not charge under a lease goes through the whole chain as
// before, which the held amount keeps correct — it counts against every
// ceiling until settled:
//
//   - A Charge with OverrunPolicyReject is charged under the lease of its
//     capability, or of its nearest ancestor that holds one, while the
//     lease is open and unexpired; a charge it cannot cover is refused
//     with ErrLeaseExhausted, and the caller leases more. A lease of no
//     amount covers no Charge.
//   - A Bump is counted under the lease the same way, while the lease
//     holds requests; one it has none left for is refused with
//     ErrLeaseRequestsExhausted. A lease of no requests covers no Bump.
//   - A Charge with OverrunPolicyRecord, a Charge of a Biscuit copy with
//     limits of its own — whose quota counts on every ancestor — a Bump of
//     one, and Reserve, Settle and Release, go through the whole chain.
//   - A capability whose ancestor holds an open lease cannot lease
//     (ErrLeaseNested): a lease is drawn from the whole chain.
//   - A Biscuit copy cannot lease (ErrInvalidRequest): none of its calls
//     go under a lease, which would hold its chain's budget past the
//     copy's own ceilings.
//
// A lease is settled — its spend charged to every ancestor and the tenant,
// its requests counted on every ancestor, the rest released — by SettleLease, by ReleaseExpired once it expired,
// by ReleaseMatching once its capability is revoked, by DeleteUsage and
// PurgeOrphans before its counters go, and by a SetTenantBudget that
// resets the spend, before it does. A Refund of a charge taken under a
// lease returns it to the lease while it is open, and to the chain once
// it is settled.
type Leaser interface {
	// Lease opens a lease of req.Amount and req.Requests on the subject, or
	// adds them to the one open, refusing what a Reserve of that amount, or
	// as many Bumps, would refuse — a ceiling crossed is the ceiling's
	// sentinel — and ErrLeaseNested under
	// an ancestor that holds one. A lease that expired is settled first and
	// a new one opened.
	Lease(ctx context.Context, req LeaseRequest) (Lease, error)
	// GetLease returns the lease of capID still holding budget — open, or
	// expired and not yet settled (ExpiresAt tells which) — or
	// ErrLeaseNotFound.
	GetLease(ctx context.Context, capID uuid.UUID) (Lease, error)
	// SettleLease settles the subject's lease and returns it as it ended,
	// or ErrLeaseNotFound when it holds none.
	SettleLease(ctx context.Context, req SettleLeaseRequest) (Lease, error)
}
