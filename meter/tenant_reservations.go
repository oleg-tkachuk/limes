package meter

import (
	"context"
	"uuid"

	"github.com/oleg-tkachuk/limes"
)

// ListTenantReservationsRequest asks for one page of a tenant's
// reservations.
type ListTenantReservationsRequest struct {
	TenantID uuid.UUID
	// Limit is the page size; see PageLimit. Negative is refused.
	Limit int
	// Cursor is the one the previous page returned; empty for the first.
	Cursor string
}

// Validate refuses a request with no tenant or a negative page size.
func (r ListTenantReservationsRequest) Validate() error {
	switch {
	case r.TenantID == uuid.Nil():
		return invalidRequest("list tenant reservations: tenant id required")
	case r.Limit < 0:
		return invalidRequest("list tenant reservations: limit %d is negative", r.Limit)
	}
	return nil
}

// TenantReservationLister lists the holds that count against a tenant's
// budget, for an operator who sees TenantBudget.ReservedAmount and asks
// which holds make it up. It is separate from MeterReader so that a usage
// store of the consumer's own implements nothing more; memstore's and
// pgstore's do, and metertest checks it when the store under test is one.
type TenantReservationLister interface {
	// ListTenantReservations returns, a page at a time, every reservation
	// of req.TenantID's capabilities that GetReservation would return —
	// open, or expired and not yet released — soonest to expire first, and
	// holds expiring at the same instant in ascending ID, compared
	// bytewise: ListReservations' order. Their amounts sum to the tenant's
	// ReservedAmount at one instant; each page is read on its own, so a
	// walk of many is no snapshot. It refuses what
	// ListTenantReservationsRequest.Validate refuses, and a call scoped to
	// another tenant as limes.ErrScopeMismatch.
	ListTenantReservations(ctx context.Context, req ListTenantReservationsRequest) (limes.Page[Reservation], error)
}
