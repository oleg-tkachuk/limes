package pgstore

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/oleg-tkachuk/limes"
)

// Census implements limes.RecordCensus over the store's capability records.
type Census struct {
	conn
}

// NewCensus returns a record census over cfg.DB, whose schema Migrate
// created.
func NewCensus(cfg Config) (*Census, error) {
	c, err := newConn(cfg)
	if err != nil {
		return nil, err
	}
	return &Census{c}, nil
}

// sqlCountRecords counts the records of tenant $1 — of every tenant the
// scope admits when NULL — grouped by tenant ($2) and principal type ($3),
// at $4 with the window ending at $5, then pages them past the cursor's
// place ($6, a flag, then $7 and $8) in CountRecords' order: tenant, then
// principal type, each ascending bytewise. A group not named is one value —
// the nil uuid or ” — so every column orders and compares.
//
// revoked is every record revoked itself or below a revoked one: the walk
// down from each revocation entry through parent_id, as IsRevoked's walk up
// answers it. A child is in its parent's tenant (Insert), so a census of one
// tenant starts from that tenant's entries alone. A disposition is one of
// revoked, then expired — no expiry, or not after $4 — then active.
const sqlCountRecords = `
WITH RECURSIVE revoked (id) AS (
    SELECT capability_id FROM ` + schema + `.revocations
    WHERE $1::uuid IS NULL OR tenant_id = $1
  UNION
    SELECT c.id FROM ` + schema + `.capabilities c JOIN revoked r ON c.parent_id = r.id
),
counts AS (
    SELECT
        CASE WHEN $2::boolean THEN c.tenant_id ELSE '00000000-0000-0000-0000-000000000000'::uuid END AS tenant_id,
        CASE WHEN $3::boolean THEN c.principal_type ELSE '' END AS principal_type,
        count(*) AS total,
        count(*) FILTER (WHERE r.id IS NULL AND c.expires_at > $4) AS active,
        count(*) FILTER (WHERE r.id IS NULL AND (c.expires_at IS NULL OR c.expires_at <= $4)) AS expired,
        count(*) FILTER (WHERE r.id IS NOT NULL) AS revoked,
        count(*) FILTER (WHERE c.parent_id IS NOT NULL) AS delegated,
        count(*) FILTER (WHERE r.id IS NULL AND c.expires_at > $4 AND c.expires_at <= $5) AS expiring_soon
    FROM ` + schema + `.capabilities c LEFT JOIN revoked r ON r.id = c.id
    WHERE $1::uuid IS NULL OR c.tenant_id = $1
    GROUP BY 1, 2
)
SELECT tenant_id, principal_type, total, active, expired, revoked, delegated, expiring_soon
FROM counts
WHERE NOT $6::boolean
   OR (tenant_id, principal_type COLLATE "C") > ($7::uuid, $8::text COLLATE "C")
ORDER BY counts.tenant_id, counts.principal_type COLLATE "C"
LIMIT $9`

// countCursor is pgstore's cursor of CountRecords: the last row's place, as
// the query orders by it.
type countCursor struct {
	Tenant uuid.UUID `json:"t"`
	Type   string    `json:"p"`
}

// CountRecords implements limes.RecordCensus, in one transaction: scoped to
// the tenant the request names (Config.CheckScope), or to every tenant with
// AllTenants, which a scope admitting fewer is refused for with
// limes.ErrScopeMismatch rather than counted as all.
func (c *Census) CountRecords(ctx context.Context, req limes.CountRecordsRequest) (limes.Page[limes.RecordCount], error) {
	if err := req.Validate(); err != nil {
		return limes.Page[limes.RecordCount]{}, err
	}
	var after countCursor
	if req.Cursor != "" {
		var err error
		if after, err = decodeCountCursor(req.Cursor); err != nil {
			return limes.Page[limes.RecordCount]{}, err
		}
	}
	var tenant *uuid.UUID
	if !req.AllTenants {
		tenant = &req.TenantID
	}
	now := c.clock()
	limit := limes.PageLimit(req.Limit)
	var out []limes.RecordCount
	count := func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, sqlCountRecords,
			tenant, req.ByTenant, req.ByPrincipalType, now, now.Add(req.ExpiringWithin),
			req.Cursor != "", after.Tenant, after.Type, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var n limes.RecordCount
			var kind string
			if err := rows.Scan(&n.TenantID, &kind, &n.Total, &n.Active, &n.Expired, &n.Revoked, &n.Delegated, &n.ExpiringSoon); err != nil {
				return err
			}
			n.PrincipalType = limes.PrincipalType(kind)
			out = append(out, n)
		}
		return rows.Err()
	}
	var err error
	if req.AllTenants {
		err = c.inEveryTenantReadTx(ctx, count)
	} else {
		err = c.inTenantReadTx(ctx, req.TenantID, count)
	}
	if err != nil {
		return limes.Page[limes.RecordCount]{}, err
	}
	if len(out) <= limit {
		return limes.Page[limes.RecordCount]{Items: out}, nil
	}
	out = out[:limit]
	last := out[len(out)-1]
	return limes.Page[limes.RecordCount]{Items: out, Next: countCursor{Tenant: last.TenantID, Type: string(last.PrincipalType)}.encode()}, nil
}

// encode is the cursor as CountRecords hands it out: opaque, URL-safe.
func (c countCursor) encode() string {
	raw, err := json.Marshal(c)
	if err != nil {
		panic(err) // a struct of a uuid and a string always encodes
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// decodeCountCursor reads a cursor encode wrote; anything else is
// ErrInvalidRequest.
func decodeCountCursor(s string) (countCursor, error) {
	bad := fmt.Errorf("%w: pgstore: record count cursor %q", limes.ErrInvalidRequest, s)
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return countCursor{}, bad
	}
	var c countCursor
	if err := json.Unmarshal(raw, &c); err != nil {
		return countCursor{}, bad
	}
	return c, nil
}

var _ limes.RecordCensus = (*Census)(nil)
