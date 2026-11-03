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
	ErrCounterOverflow error = &familyError{"limes: counter would pass MaxNanos", nil, CodeCounterOverflow}

	// ErrChargeNotFound — Refund, GetCharge or ChargeByRef named a charge
	// with no ledger row.
	ErrChargeNotFound error = &familyError{"limes: charge not found", limes.ErrNotFound, CodeChargeNotFound}

	// ErrRefundExceedsCharge — Refund would return more than the charge
	// took, counting earlier refunds of the same charge.
	ErrRefundExceedsCharge error = &familyError{"limes: refund exceeds charge", nil, CodeRefundExceedsCharge}

	// ErrReservationNotFound — Settle named a reservation that does not
	// exist, was released, or has expired. One already settled is not an
	// error: Settle returns its charge again (ChargeReceipt.Replayed).
	ErrReservationNotFound error = &familyError{"limes: reservation not found", limes.ErrNotFound, CodeReservationNotFound}

	// ErrCopyLimitExceeded — counting one more Biscuit copy of the
	// capability would pass the copy quota of the capability or of an
	// ancestor (limes.Caveats.Copies), or the store's bound of copies per
	// capability (Meter, "Copies of a Biscuit"). The request is refused and nothing is
	// counted. Matches ErrRequestLimitExceeded: the capability has used what
	// it may.
	ErrCopyLimitExceeded error = &familyError{"limes: copy limit exceeded", limes.ErrRequestLimitExceeded, CodeCopyLimitExceeded}

	// ErrUsageNotFound — no usage row exists for this capability.
	// Get-only; bump/charge always inserts on first hit.
	ErrUsageNotFound error = &familyError{"limes: usage row not found", limes.ErrNotFound, CodeUsageNotFound}

	// ErrTenantBudgetNotFound — Get-only; the tenant has no ceiling set
	// and has never been charged. A tenant charged before any ceiling was
	// set has a row, Unlimited, with ResourceVersion 0.
	ErrTenantBudgetNotFound error = &familyError{"limes: tenant budget row not found", limes.ErrNotFound, CodeTenantBudgetNotFound}

	// ErrTenantBudgetVersionMismatch — SetTenantBudget's OCC guard refused the
	// write: the stored resource_version is not the one the caller read, so
	// someone else changed the cap in between. The caller re-reads and decides,
	// rather than silently overwriting a change it never saw. Matches
	// ErrConflict.
	ErrTenantBudgetVersionMismatch error = &familyError{"limes: tenant budget resource_version mismatch", limes.ErrConflict, CodeTenantBudgetVersionMismatch}
)

// ErrExternalRefConflict — Charge named an ExternalRef its capability already
// charged under, for a different amount, unit or tenant, or under other
// Biscuit copies. One reference names one cost; replaying the first receipt
// would report the second as booked. The error names the reference, never
// the charge it names. Matches ErrConflict: the request is well formed, and
// would have charged had the reference been free.
var ErrExternalRefConflict error = &familyError{"limes: external ref already names a different charge", limes.ErrConflict, CodeExternalRefConflict}

// ErrSettleConflict — Settle named a reservation already settled, for a
// different amount. One reservation names one cost; replaying the first
// receipt would report the second as booked. Matches ErrConflict, as
// ErrExternalRefConflict does.
var ErrSettleConflict error = &familyError{"limes: reservation already settled for a different amount", limes.ErrConflict, CodeSettleConflict}

// ErrLeaseExhausted — a Charge under a lease would take it past what it
// holds. The caller leases more (Leaser.Lease) and charges again. Matches
// ErrBudgetExceeded: the budget the capability may spend now is used.
var ErrLeaseExhausted error = &familyError{"limes: lease exhausted", limes.ErrBudgetExceeded, CodeLeaseExhausted}

// ErrLeaseRequestsExhausted — a Bump under a lease would take it past the
// requests it holds. The caller leases more (Leaser.Lease) and bumps again.
// Matches ErrRequestLimitExceeded: the requests the capability may make now
// are used.
var ErrLeaseRequestsExhausted error = &familyError{"limes: lease requests exhausted", limes.ErrRequestLimitExceeded, CodeLeaseRequestsExhausted}

// ErrLeaseNotFound — GetLease or SettleLease named a capability that holds
// no lease.
var ErrLeaseNotFound error = &familyError{"limes: lease not found", limes.ErrNotFound, CodeLeaseNotFound}

// ErrLeaseNested — Lease named a capability whose ancestor holds an open
// lease: a lease is drawn from the whole chain, and the ancestor's already
// holds part of it. Matches ErrConflict.
var ErrLeaseNested error = &familyError{"limes: an ancestor holds a lease", limes.ErrConflict, CodeLeaseNested}

// ErrFeatureDisabled — a store implements a feature its configuration
// turned off (FeatureGate): Require, As and AsAll refuse it so, apart from
// a feature the store lacks, which is a build to fix rather than a mode to
// run in. Matches limes.ErrInvalidConfig.
var ErrFeatureDisabled error = &familyError{"limes: feature disabled by the store's configuration", limes.ErrInvalidConfig, CodeFeatureDisabled}

// ErrLeasingDisabled — Lease of a store whose operator disabled leasing
// (pgstore's Config.DisableLeases) while an instance writing its database
// runs a release that knows no leases. The caller charges without one.
// Leases already open are still charged under and settled. Matches
// ErrFeatureDisabled, as As refuses the same store at startup, and so
// limes.ErrInvalidConfig.
var ErrLeasingDisabled error = &familyError{"limes: leasing is not enabled", ErrFeatureDisabled, CodeLeasingDisabled}

// familyError is a sentinel with a name of its own that also matches the
// family it belongs to, if any, and that family's own, as limes's sentinels
// do; and that names its code, by which limes answers it (errcode.Coded).
type familyError struct {
	msg    string
	family error
	code   limes.Code
}

func (e *familyError) Error() string { return e.msg }
func (e *familyError) Is(target error) bool {
	return e.family != nil && (target == e.family || errors.Is(e.family, target))
}

// LimesErrorCode is the sentinel's code, which limes answers it by.
func (e *familyError) LimesErrorCode() string { return string(e.code) }

// invalidRequest is a limes.ErrInvalidRequest saying what was wrong.
func invalidRequest(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{limes.ErrInvalidRequest}, args...)...)
}

// noCompare makes a struct that holds it not comparable with ==, as
// limes's structs that may gain a field in 1.x are.
type noCompare [0]func()
