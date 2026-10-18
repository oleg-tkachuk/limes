package meter

import (
	"context"

	"github.com/google/uuid"
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
	// the tenant again: Bump, Charge, Reserve and SetTenantBudget for it
	// return ErrTenantErased. PurgeOrphans forgets the mark once none of the
	// tenant's records is left, and with it any call that could be metered.
	EraseTenantUsage(ctx context.Context, req EraseTenantUsageRequest) error
}

// EraseTenantUsageRequest names the tenant EraseTenantUsage erases.
type EraseTenantUsageRequest struct {
	TenantID uuid.UUID
}

// Validate refuses a request naming no tenant.
func (r EraseTenantUsageRequest) Validate() error {
	if r.TenantID == uuid.Nil {
		return invalidRequest("erase tenant usage: tenant id required")
	}
	return nil
}
