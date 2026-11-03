package meter

import "github.com/oleg-tkachuk/limes"

// The codes of the meter's own sentinels (docs/errors.md), which limes
// answers as its own: limes.CodeOf, limes.CategoryOf, limes.ProblemOf and
// limes.Codes know every one of them.
const (
	CodeCounterOverflow             = limes.CodeCounterOverflow
	CodeChargeNotFound              = limes.CodeChargeNotFound
	CodeRefundExceedsCharge         = limes.CodeRefundExceedsCharge
	CodeReservationNotFound         = limes.CodeReservationNotFound
	CodeCopyLimitExceeded           = limes.CodeCopyLimitExceeded
	CodeUsageNotFound               = limes.CodeUsageNotFound
	CodeTenantBudgetNotFound        = limes.CodeTenantBudgetNotFound
	CodeTenantBudgetVersionMismatch = limes.CodeTenantBudgetVersionMismatch
	CodeExternalRefConflict         = limes.CodeExternalRefConflict
	CodeSettleConflict              = limes.CodeSettleConflict
	CodeLeaseExhausted              = limes.CodeLeaseExhausted
	CodeLeaseRequestsExhausted      = limes.CodeLeaseRequestsExhausted
	CodeLeaseNotFound               = limes.CodeLeaseNotFound
	CodeLeaseNested                 = limes.CodeLeaseNested
	CodeLeasingDisabled             = limes.CodeLeasingDisabled
	CodeFeatureDisabled             = limes.CodeFeatureDisabled
)
