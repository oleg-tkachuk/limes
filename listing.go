package limes

import (
	"context"
	"fmt"
	"slices"

	"github.com/google/uuid"
)

// RecordLister lists one tenant's capability records a page at a time, each
// with its disposition, for an operator's view of them: every record of the
// tenant, or those of a principal type, a subject or a subject prefix, and of
// the dispositions it asks for, oldest or newest first. It is a type of its
// own beside Store, as RecordCensus is: a reporter holds it, an issuer does
// not. memstore's and pgstore's Census are each one; storetest checks it when
// its Env sets one.
//
// A record's disposition is the one RecordCensus counts it under, by the
// store's clock: revoked — itself or an ancestor still on record, unlike
// ListByPrincipalRequest.IncludeRevoked — over expired, over active. So, at
// one instant, the records of each disposition a full listing yields number
// what CountRecords counts for the tenant. Each page is read on its own, so
// a walk of many pages is no snapshot: a record can expire, or be revoked,
// between two of them.
type RecordLister interface {
	// ListRecords returns one page of req.TenantID's records, in id order —
	// ascending, or descending with ListOrderDescending: a capability's id
	// is a UUIDv7, so that is newest first — at most Limit of them, and the
	// cursor of the next page, empty on the last. The cursor is the store's
	// own and opaque, and holds the order and the filters it was handed out
	// for: a cursor of the other order, or of another principal type,
	// subject, subject prefix or set of dispositions, is refused.
	ListRecords(ctx context.Context, req ListRecordsRequest) (Page[RecordListing], error)
}

// RecordListingReader reads one record with its disposition, as a listing
// gives it: what Store.GetRecord reads back, and the disposition by the
// rules RecordLister and RecordCensus share, by the store's clock. A
// consumer showing one record asks it rather than working the disposition
// out from IsRevoked and the expiry, which would be a second rule beside
// the store's. A type of its own beside RecordLister, as the lister is
// beside Store: memstore's and pgstore's Census are each one, and storetest
// checks it when its Env.Lister is one.
type RecordListingReader interface {
	// GetRecordListing reads the record of id with its disposition; one
	// not on record, or not visible to the caller, is ErrNotFound, as
	// GetRecord answers it.
	GetRecordListing(ctx context.Context, id uuid.UUID) (RecordListing, error)
}

// RecordReporter is RecordLister and RecordListingReader together: what a
// reporter that lists a tenant's records and opens one of them holds.
// memstore's and pgstore's Census are each one, as NewCensus returns it.
type RecordReporter interface {
	RecordLister
	RecordListingReader
}

// RecordDisposition is what a record is now: RecordLister's and
// RecordCensus's three, under which each record falls exactly once.
type RecordDisposition string

// The dispositions. The zero value, RecordDispositionUnspecified, names
// none.
const (
	RecordDispositionUnspecified RecordDisposition = ""
	RecordDispositionActive      RecordDisposition = "active"
	RecordDispositionExpired     RecordDisposition = "expired"
	RecordDispositionRevoked     RecordDisposition = "revoked"
)

// Validate refuses a disposition other than the three, as ErrInvalidRequest.
func (d RecordDisposition) Validate() error {
	switch d {
	case RecordDispositionActive, RecordDispositionExpired, RecordDispositionRevoked:
		return nil
	case RecordDispositionUnspecified:
	}
	return invalidRequest("disposition %q is not one of %q, %q, %q",
		d, RecordDispositionActive, RecordDispositionExpired, RecordDispositionRevoked)
}

// ListOrder is the id order a list pages in.
type ListOrder uint8

const (
	// ListOrderAscending is ascending id, as ListByPrincipal pages: oldest
	// first. The zero value.
	ListOrderAscending ListOrder = iota
	// ListOrderDescending is descending id: newest first.
	ListOrderDescending
)

// String names the order for logs and errors.
func (o ListOrder) String() string {
	switch o {
	case ListOrderAscending:
		return "ascending"
	case ListOrderDescending:
		return "descending"
	default:
		return fmt.Sprintf("ListOrder(%d)", uint8(o))
	}
}

// Validate refuses an order this package does not define, as
// ErrInvalidRequest.
func (o ListOrder) Validate() error {
	if o > ListOrderDescending {
		return invalidRequest("unknown list order %s", o)
	}
	return nil
}

// ListRecordsRequest asks for one page of a tenant's records. Every filter
// but TenantID is optional, and the ones set must all hold.
type ListRecordsRequest struct {
	// TenantID is the tenant whose records are listed; required.
	TenantID uuid.UUID
	// PrincipalType lists the records issued to principals of that type;
	// unspecified, every type's.
	PrincipalType PrincipalType
	// Subject lists the records issued to that subject exactly, and
	// SubjectPrefix those whose subject starts with it, bytewise; at most
	// one of them. Empty, every subject's.
	Subject, SubjectPrefix string
	// Dispositions lists the records of any of these dispositions; empty,
	// every record.
	Dispositions []RecordDisposition
	// Order is the id order of the pages; the zero value is ascending.
	Order ListOrder
	// Cursor is the one the previous page returned; empty for the first.
	Cursor string
	// Limit is the page size; see PageLimit. Negative is refused.
	Limit int
}

// Validate refuses a request that names no tenant, an unknown principal
// type, both a subject and a subject prefix, an unknown disposition or
// order, or a negative page size. Every error matches ErrInvalidRequest.
func (r ListRecordsRequest) Validate() error {
	switch {
	case r.TenantID == uuid.Nil:
		return invalidRequest("list records: tenant id required")
	case r.PrincipalType != PrincipalTypeUnspecified && r.PrincipalType.Validate() != nil:
		return invalidRequest("list records: principal type %q is not a principal kind", r.PrincipalType)
	case r.Subject != "" && r.SubjectPrefix != "":
		return invalidRequest("list records: a subject %q beside a subject prefix %q", r.Subject, r.SubjectPrefix)
	case r.Order.Validate() != nil:
		return invalidRequest("list records: unknown order %s", r.Order)
	case r.Limit < 0:
		return invalidRequest("list records: limit %d is negative", r.Limit)
	}
	for _, d := range r.Dispositions {
		if d.Validate() != nil {
			return invalidRequest("list records: disposition %q is not one of %q, %q, %q",
				d, RecordDispositionActive, RecordDispositionExpired, RecordDispositionRevoked)
		}
	}
	return nil
}

// Admits reports whether a record of disposition d is one r lists: r names
// no disposition, or names d.
func (r ListRecordsRequest) Admits(d RecordDisposition) bool {
	return len(r.Dispositions) == 0 || slices.Contains(r.Dispositions, d)
}

// RecordListing is one record of a listing: what GetRecord reads back about
// it — its own revocation entry among it, none when it is stopped only
// through an ancestor — and its disposition now.
type RecordListing struct {
	Record      Record
	Disposition RecordDisposition
}
