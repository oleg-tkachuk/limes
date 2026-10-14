package memstore

import (
	"bytes"
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes"
)

// Census implements limes.RecordCensus over a Store's records.
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

var _ limes.RecordCensus = (*Census)(nil)
