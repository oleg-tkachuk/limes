package meter

import (
	"context"
	"uuid"
)

// TenantUsageEraser is a UsageStore that erases one tenant's usage, for a
// consumer removing the tenant. It is optional; metertest checks a
// UsageStore that implements it.
type TenantUsageEraser interface {
	// EraseTenantUsage removes every counter, hold and charge of the
	// tenant's capabilities and its Biscuit copies, and the tenant's
	// aggregate: the charges ledger is the tenant's billing history, and
	// it does not come back. It changes no other tenant's rows.
	//
	// It also marks the tenant erased, so that a call made after it — a
	// request verified before the erase, a late cost — counts nothing of
	// the tenant again: Bump, Charge, Reserve, Leaser.Lease and
	// SetTenantBudget for it return limes.ErrTenantErased, and so does the
	// limes.Store's Insert. PurgeOrphans forgets the mark once none of the
	// tenant's records is left, and with it any call that could be metered.
	//
	// It returns what it removed (EraseTenantUsageResult).
	EraseTenantUsage(ctx context.Context, req EraseTenantUsageRequest) (EraseTenantUsageResult, error)
}

// EraseTenantUsageRequest names the tenant EraseTenantUsage erases.
type EraseTenantUsageRequest struct {
	TenantID uuid.UUID
}

// Validate refuses a request naming no tenant.
func (r EraseTenantUsageRequest) Validate() error {
	if r.TenantID == uuid.Nil() {
		return refuseRequest("erase tenant usage: tenant id required")
	}
	return nil
}

// EraseTenantUsageResult is what one EraseTenantUsage removed. A store
// erases a tenant in one call today; a later request that bounds an erase
// to a batch reports here whether more is left.
type EraseTenantUsageResult struct {
	// Counters counts the capabilities whose counters it removed, and
	// Copies the Biscuit copies whose own counters it removed.
	Counters int64
	Copies   int64
	// Holds counts the open holds (Reserve) it removed; a lease goes with
	// its capability's counters.
	Holds int64
	// Charges counts the entries of the charges ledger it removed.
	Charges int64
	_       noCompare
}
