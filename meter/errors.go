package meter

import (
	"errors"
	"fmt"

	"github.com/oleg-tkachuk/limes"
)

// The meter's own sentinels. The refusals ErrorResponse answers —
// limes.ErrRequestLimitExceeded, limes.ErrBudgetExceeded,
// limes.ErrTenantBudgetExceeded — and limes.ErrInvalidAmount stay in limes,
// beside the rest of its error families.
var (
	// ErrCounterOverflow — an amount, each valid on its own, that would take
	// a counter past MaxNanos. The request is well formed; the counter has
	// no room for it.
	ErrCounterOverflow = errors.New("limes: counter would pass MaxNanos")

	// ErrChargeNotFound — Refund, GetCharge or ChargeByRef named a charge
	// with no ledger row.
	ErrChargeNotFound error = &familyError{"limes: charge not found", limes.ErrNotFound}

	// ErrRefundExceedsCharge — Refund would return more than the charge
	// took, counting earlier refunds of the same charge.
	ErrRefundExceedsCharge = errors.New("limes: refund exceeds charge")

	// ErrReservationNotFound — Settle named a reservation that does not
	// exist, was released, or has expired. One already settled is not an
	// error: Settle returns its charge again (ChargeReceipt.Replayed).
	ErrReservationNotFound error = &familyError{"limes: reservation not found", limes.ErrNotFound}

	// ErrCopyLimitExceeded — counting one more Biscuit copy of the
	// capability would pass the copy quota of the capability or of an
	// ancestor (limes.Caveats.Copies), or the store's bound of copies per
	// capability (Meter, "Copies of a Biscuit"). The request is refused and nothing is
	// counted. Matches ErrRequestLimitExceeded: the capability has used what
	// it may.
	ErrCopyLimitExceeded error = &familyError{"limes: copy limit exceeded", limes.ErrRequestLimitExceeded}

	// ErrUsageNotFound — no usage row exists for this capability.
	// Get-only; bump/charge always inserts on first hit.
	ErrUsageNotFound error = &familyError{"limes: usage row not found", limes.ErrNotFound}

	// ErrTenantBudgetNotFound — Get-only; the tenant has no ceiling set
	// and has never been charged. A tenant charged before any ceiling was
	// set has a row, Unlimited, with ResourceVersion 0.
	ErrTenantBudgetNotFound error = &familyError{"limes: tenant budget row not found", limes.ErrNotFound}

	// ErrTenantBudgetVersionMismatch — SetTenantBudget's OCC guard refused the
	// write: the stored resource_version is not the one the caller read, so
	// someone else changed the cap in between. The caller re-reads and decides,
	// rather than silently overwriting a change it never saw. Matches
	// ErrConflict.
	ErrTenantBudgetVersionMismatch error = &familyError{"limes: tenant budget resource_version mismatch", limes.ErrConflict}
)

// ErrExternalRefConflict — Charge named an ExternalRef its capability already
// charged under, for a different amount, unit or tenant, or under other
// Biscuit copies. One reference names one cost; replaying the first receipt
// would report the second as booked. The error names the reference, never
// the charge it names. Matches ErrConflict: the request is well formed, and
// would have charged had the reference been free.
var ErrExternalRefConflict error = &familyError{"limes: external ref already names a different charge", limes.ErrConflict}

// ErrSettleConflict — Settle named a reservation already settled, for a
// different amount. One reservation names one cost; replaying the first
// receipt would report the second as booked. Matches ErrConflict, as
// ErrExternalRefConflict does.
var ErrSettleConflict error = &familyError{"limes: reservation already settled for a different amount", limes.ErrConflict}

// ErrLeaseExhausted — a Charge under a lease would take it past what it
// holds. The caller leases more (Leaser.Lease) and charges again. Matches
// ErrBudgetExceeded: the budget the capability may spend now is used.
var ErrLeaseExhausted error = &familyError{"limes: lease exhausted", limes.ErrBudgetExceeded}

// ErrLeaseRequestsExhausted — a Bump under a lease would take it past the
// requests it holds. The caller leases more (Leaser.Lease) and bumps again.
// Matches ErrRequestLimitExceeded: the requests the capability may make now
// are used.
var ErrLeaseRequestsExhausted error = &familyError{"limes: lease requests exhausted", limes.ErrRequestLimitExceeded}

// ErrLeaseNotFound — GetLease or SettleLease named a capability that holds
// no lease.
var ErrLeaseNotFound error = &familyError{"limes: lease not found", limes.ErrNotFound}

// ErrLeaseNested — Lease named a capability whose ancestor holds an open
// lease: a lease is drawn from the whole chain, and the ancestor's already
// holds part of it. Matches ErrConflict.
var ErrLeaseNested error = &familyError{"limes: an ancestor holds a lease", limes.ErrConflict}

// ErrLeasingDisabled — Lease of a store whose operator has not enabled
// leasing (pgstore's Config.Leases): every instance writing the store's
// database must run a release that knows leases before any opens. The
// caller charges without one. Leases already open are still charged under
// and settled. Matches limes.ErrInvalidConfig.
var ErrLeasingDisabled error = &familyError{"limes: leasing is not enabled", limes.ErrInvalidConfig}

// familyError is a sentinel with a name of its own that also matches the
// family it belongs to, and that family's own, as limes's sentinels do.
type familyError struct {
	msg    string
	family error
}

func (e *familyError) Error() string { return e.msg }
func (e *familyError) Is(target error) bool {
	return target == e.family || errors.Is(e.family, target)
}

// invalidRequest is a limes.ErrInvalidRequest saying what was wrong.
func invalidRequest(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{limes.ErrInvalidRequest}, args...)...)
}

// noCompare makes a struct that holds it not comparable with ==, as
// limes's structs that may gain a field in 1.x are.
type noCompare [0]func()
