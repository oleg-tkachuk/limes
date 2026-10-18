package limes

import (
	"context"

	"github.com/google/uuid"
)

// TenantRecordEraser is a Store that erases one tenant's capability records,
// for a consumer removing the tenant. It is optional; storetest checks a
// Store that implements it.
type TenantRecordEraser interface {
	// EraseTenantRecords revokes every capability of the tenant not revoked
	// yet, so no token of the tenant verifies again, and removes those of
	// its records Purge would remove at MinPurgeRetention — each with its
	// revocation entry and its Biscuit copies' — and no other: an id not on
	// record is not revoked, so a record removed while its token may still
	// verify would let that token through again. Purge takes the rest as
	// it expires. It
	// changes no other tenant's records, and a second call revokes and
	// removes nothing the first left.
	EraseTenantRecords(ctx context.Context, req EraseTenantRequest) (EraseTenantResult, error)
}

// EraseTenantRequest names the tenant EraseTenantRecords erases and who
// asked, recorded on each revocation it writes.
type EraseTenantRequest struct {
	TenantID uuid.UUID
	// Note and Actor are written on every revocation entry, as
	// RevokeRequest's are.
	Note  string
	Actor string
}

// Validate refuses a request naming no tenant.
func (r EraseTenantRequest) Validate() error {
	if r.TenantID == uuid.Nil {
		return invalidRequest("erase tenant records: tenant id required")
	}
	return nil
}

// EraseTenantResult is what EraseTenantRecords did.
type EraseTenantResult struct {
	// Revoked counts the revocation entries it wrote: one for each of the
	// tenant's capabilities that had none of its own — one revoked only
	// through an ancestor included — whether or not its record was then
	// removed.
	Revoked int64
	// Removed counts the records it removed; Kept, those it left on record
	// for Purge.
	Removed int64
	Kept    int64
	_       noCompare
}
