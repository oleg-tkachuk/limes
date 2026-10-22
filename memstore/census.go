package memstore

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes"
)

// Census implements limes.RecordCensus, limes.RecordLister and
// limes.RecordListingReader — limes.RecordReporter is the last two —
// over a Store's records.
type Census struct {
	records *Store
}

// NewCensus returns the record census of records.
func NewCensus(records *Store) *Census {
	return &Census{records: records}
}

// countKey is one group of CountRecords, which its cursor names: the fields
// it groups by.
type countKey struct {
	Tenant uuid.UUID           `json:"t"`
	Type   limes.PrincipalType `json:"p"`
}

// CountRecords implements limes.RecordCensus.
func (c *Census) CountRecords(_ context.Context, req limes.CountRecordsRequest) (limes.Page[limes.RecordCount], error) {
	if err := req.Validate(); err != nil {
		return limes.Page[limes.RecordCount]{}, err
	}
	var after *countKey
	if req.Cursor != "" {
		k, err := decodeCountCursor(req.Cursor)
		if err != nil {
			return limes.Page[limes.RecordCount]{}, err
		}
		after = &k
	}
	counts, err := c.count(req)
	if err != nil {
		return limes.Page[limes.RecordCount]{}, err
	}
	slices.SortFunc(counts, func(a, b limes.RecordCount) int { return compareCounts(countKeyOf(a), countKeyOf(b)) })
	if after != nil {
		counts = slices.DeleteFunc(counts, func(n limes.RecordCount) bool { return compareCounts(countKeyOf(n), *after) <= 0 })
	}
	limit := limes.PageLimit(req.Limit)
	if len(counts) <= limit {
		return limes.Page[limes.RecordCount]{Items: counts}, nil
	}
	counts = counts[:limit]
	return limes.Page[limes.RecordCount]{Items: counts, Next: countKeyOf(counts[len(counts)-1]).encode()}, nil
}

// count groups the records req names as it asks.
func (c *Census) count(req limes.CountRecordsRequest) ([]limes.RecordCount, error) {
	s := c.records
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.nowFn()
	groups := map[countKey]*limes.RecordCount{}
	for id, rec := range s.caps {
		if !req.AllTenants && rec.Subject.TenantID != req.TenantID {
			continue
		}
		revoked, err := s.revokedLocked(id)
		if err != nil {
			return nil, err
		}
		var k countKey
		if req.ByTenant {
			k.Tenant = rec.Subject.TenantID
		}
		if req.ByPrincipalType {
			k.Type = rec.Subject.Type
		}
		g, ok := groups[k]
		if !ok {
			g = &limes.RecordCount{TenantID: k.Tenant, PrincipalType: k.Type}
			groups[k] = g
		}
		g.Total++
		if rec.ParentID != uuid.Nil {
			g.Delegated++
		}
		switch {
		case revoked:
			g.Revoked++
		case expiredAt(rec.ExpiresAt, now):
			g.Expired++
		default:
			g.Active++
			if !rec.ExpiresAt.After(now.Add(req.ExpiringWithin)) {
				g.ExpiringSoon++
			}
		}
	}
	out := make([]limes.RecordCount, 0, len(groups))
	for _, g := range groups {
		out = append(out, *g)
	}
	return out, nil
}

// expiredAt reports whether a record expiring at expiresAt is expired at
// now: a zero expiry is, as limes reads one.
func expiredAt(expiresAt, now time.Time) bool {
	return expiresAt.IsZero() || !expiresAt.After(now)
}

// revokedLocked reports whether id, or an ancestor of it still on record, is
// revoked, as IsRevoked does; s.mu is held.
func (s *Store) revokedLocked(id uuid.UUID) (bool, error) {
	if _, ok := s.revoked[id]; ok {
		return true, nil
	}
	ancestors, err := s.ancestorsLocked(id)
	if err != nil {
		return false, err
	}
	for _, a := range ancestors {
		if _, ok := s.revoked[a.ID]; ok {
			return true, nil
		}
	}
	return false, nil
}

// countKeyOf is n's place in CountRecords' order.
func countKeyOf(n limes.RecordCount) countKey {
	return countKey{Tenant: n.TenantID, Type: n.PrincipalType}
}

// compareCounts is CountRecords' order: by tenant, then by principal type,
// each ascending bytewise.
func compareCounts(a, b countKey) int {
	return cmp.Or(
		bytes.Compare(a.Tenant[:], b.Tenant[:]),
		cmp.Compare(a.Type, b.Type),
	)
}

// encode is the cursor as CountRecords hands it out: opaque, URL-safe.
func (k countKey) encode() string {
	raw, err := json.Marshal(k)
	if err != nil {
		panic(err) // a struct of a uuid and a string always encodes
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// decodeCountCursor reads a cursor encode wrote; anything else is
// ErrInvalidRequest.
func decodeCountCursor(s string) (countKey, error) {
	bad := fmt.Errorf("%w: memstore: record count cursor %q", limes.ErrInvalidRequest, s)
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return countKey{}, bad
	}
	var k countKey
	if err := json.Unmarshal(raw, &k); err != nil {
		return countKey{}, bad
	}
	return k, nil
}

// listCursor is memstore's cursor of ListRecords: the last record's id, and
// the order and the filters it was handed out for (filtersKey).
type listCursor struct {
	After   uuid.UUID       `json:"a"`
	Order   limes.ListOrder `json:"o"`
	Filters string          `json:"f"`
}

// filtersKey names the filters of req: its principal type, subject,
// subject prefix and dispositions, these in any order and repeated or not.
// A cursor holds it, so one handed back with other filters is refused rather
// than paged on from its id under them, which would be neither listing.
func filtersKey(req limes.ListRecordsRequest) string {
	dispositions := slices.Clone(req.Dispositions)
	slices.Sort(dispositions)
	raw, err := json.Marshal([]any{req.PrincipalType, req.Subject, req.SubjectPrefix, slices.Compact(dispositions)})
	if err != nil {
		panic(err) // strings and a list of strings always encode
	}
	sum := sha256.Sum256(raw)
	return base64.RawURLEncoding.EncodeToString(sum[:16])
}

// ListRecords implements limes.RecordLister.
func (c *Census) ListRecords(_ context.Context, req limes.ListRecordsRequest) (limes.Page[limes.RecordListing], error) {
	if err := req.Validate(); err != nil {
		return limes.Page[limes.RecordListing]{}, err
	}
	var after *uuid.UUID
	if req.Cursor != "" {
		k, err := decodeListCursor(req.Cursor, req)
		if err != nil {
			return limes.Page[limes.RecordListing]{}, err
		}
		after = &k.After
	}
	// past reports whether id comes after the cursor's, in the request's
	// order.
	past := func(id uuid.UUID) bool {
		if after == nil {
			return true
		}
		if req.Order == limes.ListOrderDescending {
			return uuidLess(id, *after)
		}
		return uuidLess(*after, id)
	}
	out, err := c.list(req, past)
	if err != nil {
		return limes.Page[limes.RecordListing]{}, err
	}
	slices.SortFunc(out, func(a, b limes.RecordListing) int {
		n := bytes.Compare(a.Record.Capability.ID[:], b.Record.Capability.ID[:])
		if req.Order == limes.ListOrderDescending {
			return -n
		}
		return n
	})
	limit := limes.PageLimit(req.Limit)
	if len(out) <= limit {
		return limes.Page[limes.RecordListing]{Items: out}, nil
	}
	out = out[:limit]
	next := listCursor{After: out[len(out)-1].Record.Capability.ID, Order: req.Order, Filters: filtersKey(req)}
	return limes.Page[limes.RecordListing]{Items: out, Next: encodeCursor(next)}, nil
}

// list is every record req admits past the cursor, unordered, each with its
// disposition by the census's rules.
func (c *Census) list(req limes.ListRecordsRequest, past func(uuid.UUID) bool) ([]limes.RecordListing, error) {
	s := c.records
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.nowFn()
	out := []limes.RecordListing{}
	for id, rec := range s.caps {
		switch {
		case rec.Subject.TenantID != req.TenantID,
			req.PrincipalType != limes.PrincipalTypeUnspecified && rec.Subject.Type != req.PrincipalType,
			req.Subject != "" && rec.Subject.Subject != req.Subject,
			!strings.HasPrefix(rec.Subject.Subject, req.SubjectPrefix),
			!past(id):
			continue
		}
		l, err := s.listingLocked(id, now)
		if err != nil {
			return nil, err
		}
		if req.Admits(l.Disposition) {
			out = append(out, l)
		}
	}
	return out, nil
}

// GetRecordListing implements limes.RecordListingReader.
func (c *Census) GetRecordListing(_ context.Context, id uuid.UUID) (limes.RecordListing, error) {
	s := c.records
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.caps[id]; !ok {
		return limes.RecordListing{}, limes.ErrNotFound
	}
	return s.listingLocked(id, s.nowFn())
}

// listingLocked is the record of id, which is on record, with its
// disposition at now by the census's rules; s.mu is held.
func (s *Store) listingLocked(id uuid.UUID, now time.Time) (limes.RecordListing, error) {
	rec := s.caps[id]
	revoked, err := s.revokedLocked(id)
	if err != nil {
		return limes.RecordListing{}, err
	}
	d := limes.RecordDispositionActive
	switch {
	case revoked:
		d = limes.RecordDispositionRevoked
	case expiredAt(rec.ExpiresAt, now):
		d = limes.RecordDispositionExpired
	}
	r := limes.Record{Capability: rec.Clone(), IssuedBy: s.issuedBy[id].Clone()}
	if entry, ok := s.revoked[id]; ok {
		r.Revocation = &entry
	}
	return limes.RecordListing{Record: r, Disposition: d}, nil
}

// decodeListCursor reads a cursor ListRecords handed out for req's order and
// filters; anything else, a cursor of the other order or of other filters
// among it, is ErrInvalidRequest.
func decodeListCursor(s string, req limes.ListRecordsRequest) (listCursor, error) {
	var k listCursor
	if err := decodeCursor(s, &k); err != nil || k.After == uuid.Nil || k.Order != req.Order || k.Filters != filtersKey(req) {
		return listCursor{}, fmt.Errorf("%w: memstore: record list cursor %q", limes.ErrInvalidRequest, s)
	}
	return k, nil
}

// encodeCursor is a cursor as the census hands it out: opaque, URL-safe.
func encodeCursor(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err) // the cursors are structs of uuids, strings and integers
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// decodeCursor reads a cursor encodeCursor wrote into v.
func decodeCursor(s string, v any) error {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}

var (
	_ limes.RecordCensus        = (*Census)(nil)
	_ limes.RecordLister        = (*Census)(nil)
	_ limes.RecordListingReader = (*Census)(nil)
	_ limes.RecordReporter      = (*Census)(nil)
)
