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
	// Within 1..MaxNanos.
	Amount limes.Nanos
	// TTL is how long the lease lasts unless settled, at most until its
	// capability expires; a lease topped up lasts until the later of its
	// end and this one. 0 = DefaultLeaseTTL; negative is ErrInvalidRequest.
	TTL time.Duration
}

// Validate refuses an amount out of range or of zero, a subject that names
// no capability, no tenant or no unit, or an unknown unit, and a negative
// TTL.
func (r LeaseRequest) Validate() error {
	if err := r.Amount.Validate(); err != nil {
		return err
	}
	if r.Amount == 0 {
		return invalidRequest("lease of no amount")
	}
	if err := r.Subject.ValidateAmounts(); err != nil {
		return err
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
	// ExpiresAt is when the lease ends unless settled first; a charge
	// after it goes through the whole chain.
	ExpiresAt time.Time
	_         noCompare
}

// Remaining is what the lease can still be charged.
func (l Lease) Remaining() limes.Nanos {
	return l.Amount - l.Spent
}

// Leaser leases budget to a capability, so that its charges, and those of
// the capabilities delegated from it, lock its own counters and not those
// of its ancestors or its tenant (docs/adr/0017-budget-leases.md). A usage
// store implements it; memstore's and pgstore's do.
//
// A lease holds its amount against every ancestor and the tenant at once,
// checking every ceiling then, so a charge under it checks only the
// capabilities below the leaseholder, their copies, and the lease's
// balance; what it spent reaches the chain when it is settled. Every path
// that does not charge under a lease goes through the whole chain as
// before, which the held amount keeps correct — it counts against every
// ceiling until settled:
//
//   - A Charge with OverrunPolicyReject is charged under the lease of its
//     capability, or of its nearest ancestor that holds one, while the
//     lease is open and unexpired; a charge it cannot cover is refused
//     with ErrLeaseExhausted, and the caller leases more.
//   - A Charge with OverrunPolicyRecord, and Reserve, Settle and Release,
//     go through the whole chain.
//   - A capability whose ancestor holds an open lease cannot lease
//     (ErrLeaseNested): a lease is drawn from the whole chain.
//
// A lease is settled — its spend charged to every ancestor and the tenant,
// the rest released — by SettleLease, by ReleaseExpired once it expired,
// by ReleaseMatching once its capability is revoked, by DeleteUsage and
// PurgeOrphans before its counters go, and by a SetTenantBudget that
// resets the spend, before it does. A Refund of a charge taken under a
// lease returns it to the lease while it is open, and to the chain once
// it is settled.
type Leaser interface {
	// Lease opens a lease of req.Amount on the subject, or adds it to the
	// one open, refusing what a Reserve of that amount would refuse — a
	// ceiling crossed is the ceiling's sentinel — and ErrLeaseNested under
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
