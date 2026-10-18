package limes

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// RecordCensus counts capability records by what they are now — active,
// expired or revoked — for a platform operator's report: the records of one
// tenant or of every tenant, whole or by tenant and by principal type, with
// how many expire soon. It reads the store a consumer issues into, so a
// report counts through it rather than through a store's tables, whose
// columns are the store's own; a view over those tables could freeze its
// columns, but not take the store's clock or share these rules with every
// store. It is a type of its own, beside Store: a reporter holds it, an
// issuer does not. memstore and pgstore each ship one; storetest checks it
// when its Env sets one.
//
// Each record counts under exactly one disposition, by the store's clock,
// the one expiry and purging are measured by:
//
//   - Revoked: the record, or an ancestor of it still on record, is
//     revoked — what Store.IsRevoked answers, so a child of a revoked parent
//     is revoked here, unlike ListByPrincipalRequest.IncludeRevoked, which
//     reads a record's own entry. Revoked wins over expired.
//   - Expired: not revoked, and its ExpiresAt is not after now. A
//     child expires no later than its parent (Store.Insert), so no ancestor
//     need be read for it.
//   - Active: neither.
//
// A Biscuit copy's revocation is not a record's and counts nowhere.
type RecordCensus interface {
	// CountRecords returns one page of counts, each one group's: the
	// records of req.TenantID, or of every tenant with req.AllTenants,
	// grouped by tenant and by principal type when req asks, in that order,
	// each ascending. A group with no record has no row, so the census of
	// no record is an empty page.
	CountRecords(ctx context.Context, req CountRecordsRequest) (Page[RecordCount], error)
}

// CountRecordsRequest asks for one page of record counts.
type CountRecordsRequest struct {
	// TenantID is the tenant whose records are counted; AllTenants counts
	// every tenant's instead. Exactly one of them is required.
	TenantID   uuid.UUID
	AllTenants bool
	// ExpiringWithin is the window ExpiringSoon counts in: an active record
	// expiring within it of now. Zero counts none; negative is refused.
	ExpiringWithin time.Duration
	// ByTenant and ByPrincipalType group the counts by the record's tenant
	// and by its subject's principal type as well.
	ByTenant, ByPrincipalType bool
	// Limit is the page size; see PageLimit. Negative is refused.
	Limit int
	// Cursor is the one the previous page returned; empty for the first.
	Cursor string
}

// Validate refuses a request that names no tenant and not every tenant, or
// both, a negative window, or a negative page size.
func (r CountRecordsRequest) Validate() error {
	switch {
	case r.TenantID == uuid.Nil && !r.AllTenants:
		return invalidRequest("count records: a tenant id or AllTenants required")
	case r.TenantID != uuid.Nil && r.AllTenants:
		return invalidRequest("count records: tenant id %s beside AllTenants", r.TenantID)
	case r.ExpiringWithin < 0:
		return invalidRequest("count records: ExpiringWithin %s is negative", r.ExpiringWithin)
	case r.Limit < 0:
		return invalidRequest("count records: limit %d is negative", r.Limit)
	}
	return nil
}

// RecordCount is one group's counts. The fields a request did not group by
// are zero.
type RecordCount struct {
	TenantID      uuid.UUID
	PrincipalType PrincipalType
	// Total is every record of the group: Active, Expired and Revoked
	// together, each record in one of them.
	Total, Active, Expired, Revoked int64
	// Delegated counts the group's records with a parent, whatever their
	// disposition.
	Delegated int64
	// ExpiringSoon counts the active records expiring within the
	// request's ExpiringWithin of now: a part of Active.
	ExpiringSoon int64
	_            noCompare
}
