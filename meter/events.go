package meter

import (
	"context"
	"time"
	"uuid"
)

// StoreOperation names the store call an event is about: the name of its
// method on UsageStore, Leaser or MatchingReleaser. The values are never
// renamed; new ones may be added.
type StoreOperation string

// The store calls that lock a tenant's aggregate, each its method.
const (
	StoreOperationCharge          StoreOperation = "Charge"
	StoreOperationReserve         StoreOperation = "Reserve"
	StoreOperationSettle          StoreOperation = "Settle"
	StoreOperationRelease         StoreOperation = "Release"
	StoreOperationReleaseExpired  StoreOperation = "ReleaseExpired"
	StoreOperationReleaseMatching StoreOperation = "ReleaseMatching"
	StoreOperationRefund          StoreOperation = "Refund"
	StoreOperationLease           StoreOperation = "Lease"
	StoreOperationSettleLease     StoreOperation = "SettleLease"
	StoreOperationSetTenantBudget StoreOperation = "SetTenantBudget"
	StoreOperationDeleteUsage     StoreOperation = "DeleteUsage"
	StoreOperationPurgeOrphans    StoreOperation = "PurgeOrphans"
)

// StoreEvents are the callbacks a usage store makes as it works, for
// metrics beside its log (docs/adr/0022-unlimited-tenant-aggregate.md,
// Decision 6). Each is optional: a nil one drops its events without a word.
// A store that has nothing to report for one never calls it.
//
// They run synchronously, on the caller's goroutine, while the call runs,
// mostly inside its open transaction with its locks held: a callback must
// not block, and must not panic — the store does not recover one. Fields
// may be added to the struct and to its events.
type StoreEvents struct {
	// OnTenantWait is called each time a metered call has locked the row
	// of its tenant's aggregate, or written the tenant's stripe where the
	// store counts a tenant with no ceiling in stripes, however short the
	// wait: a histogram needs the fast samples too.
	OnTenantWait func(context.Context, TenantWaitEvent)
}

// TenantWaitEvent is one wait of a metered call for its tenant's
// aggregate.
type TenantWaitEvent struct {
	// TenantID is the tenant whose aggregate the call locked.
	TenantID uuid.UUID
	// Operation is the store call that locked it.
	Operation StoreOperation
	// Wait is how long locking the row took, or writing the stripe, by the
	// database's clock: the wait for that row alone, not for the tenant's
	// advisory lock nor for the counters of the call's chain. Never
	// negative.
	Wait time.Duration
}
