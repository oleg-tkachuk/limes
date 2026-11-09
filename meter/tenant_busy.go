package meter

import (
	"fmt"
	"time"
	"uuid"
)

// TenantBudgetBusyError is ErrTenantBudgetBusy with what the store saw of
// the calls it waited for, so a caller can tell a tenant's ordinary load,
// which a retry outlasts, from a call held open, which no retry outlasts
// until it ends. errors.Is matches ErrTenantBudgetBusy, and limes answers
// it by that code.
type TenantBudgetBusyError struct {
	TenantID uuid.UUID
	// Waited is how long the store waited before it gave up.
	Waited time.Duration
	// Holders is how many transactions held the tenant's calls in flight
	// when it gave up.
	Holders int
	// Oldest is the longest running of the holders the store's database
	// role may inspect; nil when it may inspect none. PostgreSQL shows a
	// session of another role only to one granted pg_read_all_stats, so a
	// holder of another service's role is counted in Holders and seen no
	// further.
	Oldest *TenantBudgetHolder
}

// TenantBudgetHolder is one transaction that held a tenant's calls in
// flight.
type TenantBudgetHolder struct {
	// Running is how long its transaction had run.
	Running time.Duration
	// IdleInTransaction reports that it ran no statement and waited on its
	// client: a caller that holds its transaction open, which ends only when
	// the caller commits, rolls back or is cut off — by the database's
	// idle_in_transaction_session_timeout, for one.
	IdleInTransaction bool
}

// Error says what the store saw, after ErrTenantBudgetBusy's own text.
func (e *TenantBudgetBusyError) Error() string {
	msg := fmt.Sprintf("%v: tenant %s: waited %s for %d calls in flight", ErrTenantBudgetBusy, e.TenantID, e.Waited, e.Holders)
	switch {
	case e.Oldest == nil:
		return msg + ", none of them visible to this role"
	case e.Oldest.IdleInTransaction:
		return fmt.Sprintf("%s, the oldest idle in its transaction for %s", msg, e.Oldest.Running)
	default:
		return fmt.Sprintf("%s, the oldest running for %s", msg, e.Oldest.Running)
	}
}

// Unwrap is ErrTenantBudgetBusy.
func (e *TenantBudgetBusyError) Unwrap() error { return ErrTenantBudgetBusy }
