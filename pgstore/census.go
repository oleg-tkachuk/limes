package pgstore

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/oleg-tkachuk/limes"
)

// A record's revoked column says whether it is revoked, itself or through
// an ancestor still on record, as IsRevoked's walk up answers it: a
// revocation entry marks its record and every record below it, and a record
// inserted under a marked one is marked (migrations/015_revoked_flag.sql).
// The census and ListRecords read it, so neither walks the revocations.

// Census implements limes.RecordCensus, limes.RecordLister and
// limes.RecordListingReader — limes.RecordReporter is the last two —
// over the store's capability records.
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
// A disposition is one of revoked, then expired — no expiry, or not after
// $4 — then active.
const sqlCountRecords = `
WITH counts AS (
    SELECT
        CASE WHEN $2::boolean THEN c.tenant_id ELSE '00000000-0000-0000-0000-000000000000'::uuid END AS tenant_id,
        CASE WHEN $3::boolean THEN c.principal_type ELSE '' END AS principal_type,
        count(*) AS total,
        count(*) FILTER (WHERE NOT c.revoked AND c.expires_at > $4) AS active,
        count(*) FILTER (WHERE NOT c.revoked AND (c.expires_at IS NULL OR c.expires_at <= $4)) AS expired,
        count(*) FILTER (WHERE c.revoked) AS revoked,
        count(*) FILTER (WHERE c.parent_id IS NOT NULL) AS delegated,
        count(*) FILTER (WHERE NOT c.revoked AND c.expires_at > $4 AND c.expires_at <= $5) AS expiring_soon
    FROM ` + schema + `.capabilities c
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

// sqlListRecords is ListRecords' query before its filters: tenant $1's
// records, each with its own revocation entry, read by its key for each
// record of the page (the OFFSET keeps the planner from joining every
// entry instead), and its disposition at $2 by the census's rules: revoked,
// then expired, then active.
// listRecordsSQL adds a condition for each filter the request sets and
// none for the rest, so each shape of request is a statement, and a plan, of
// its own: one that pages by subject reads capabilities_principal, one that
// pages a tenant or a principal type the index of migration 014, and one
// of some dispositions the partial index of 017 for their revoked flag.
const sqlListRecords = sqlRecordListing + `
WHERE c.tenant_id = $1`

// sqlRecordListing is a record listing's columns, from capabilities c and
// its own revocation entry, its disposition at $2, and for a record revoked
// only through an ancestor the entry of the nearest ancestor that has one:
// what ListRecords and GetRecordListing read alike, so both give a record
// one disposition. The walk up runs for those records alone, a parent a
// step by its key, and stops at the first ancestor with an entry, or at
// one no longer on record, as IsRevoked's does.
const sqlRecordListing = `
SELECT c.capability, c.issued_by, v.revoked_at, v.note, v.actor, v.cascaded, CASE
    WHEN c.revoked THEN 'revoked'
    WHEN c.expires_at IS NULL OR c.expires_at <= $2 THEN 'expired'
    ELSE 'active'
END,
a.capability_id, a.revoked_at, a.note, a.actor, a.cascaded
FROM ` + schema + `.capabilities c
LEFT JOIN LATERAL (
    SELECT revoked_at, note, actor, cascaded FROM ` + schema + `.revocations WHERE capability_id = c.id OFFSET 0
) v ON true
LEFT JOIN LATERAL (
    WITH RECURSIVE up (id, generation) AS (
        SELECT c.parent_id, 1 WHERE c.revoked AND v.revoked_at IS NULL AND c.parent_id IS NOT NULL
      UNION ALL
        SELECT p.parent_id, up.generation + 1
        FROM ` + schema + `.capabilities p JOIN up ON p.id = up.id
        WHERE p.parent_id IS NOT NULL
          AND NOT EXISTS (SELECT 1 FROM ` + schema + `.revocations e WHERE e.capability_id = up.id)
    )
    SELECT r.capability_id, r.revoked_at, r.note, r.actor, r.cascaded
    FROM up JOIN ` + schema + `.revocations r ON r.capability_id = up.id
    ORDER BY up.generation
    LIMIT 1
) a ON true`

// sqlGetRecordListing is the listing of the record of id $1 at $2.
const sqlGetRecordListing = sqlRecordListing + `
WHERE c.id = $1`

// GetRecordListing implements limes.RecordListingReader: the record of id
// with its disposition by the store's clock, as ListRecords gives it, in a
// transaction of the call's scope, as GetRecord reads it; a record out of
// that scope is not found.
func (c *Census) GetRecordListing(ctx context.Context, id uuid.UUID) (limes.RecordListing, error) {
	var l limes.RecordListing
	err := c.inReadTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, sqlGetRecordListing, id, c.clock())
		if err != nil {
			return err
		}
		l, err = pgx.CollectExactlyOneRow(rows, scanListing)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return limes.RecordListing{}, limes.ErrNotFound
	}
	return l, err
}

// dispositionWhere is each disposition as a condition on a record's own
// columns at $2, so that a listing of some dispositions reads the index of
// the revoked flag they have (migrations/017_revoked_indexes.sql).
var dispositionWhere = map[limes.RecordDisposition]string{
	limes.RecordDispositionActive:  "(NOT c.revoked AND c.expires_at > $2)",
	limes.RecordDispositionExpired: "(NOT c.revoked AND (c.expires_at IS NULL OR c.expires_at <= $2))",
	limes.RecordDispositionRevoked: "c.revoked",
}

// listRecordsSQL is sqlListRecords with req's filters, past the cursor's id
// after when it is not nil, ordered and limited, and its arguments: the
// tenant, now, then each filter's in turn.
func listRecordsSQL(req limes.ListRecordsRequest, now time.Time, after *uuid.UUID, limit int) (string, []any) {
	var q strings.Builder
	q.WriteString(sqlListRecords)
	args := []any{req.TenantID, now}
	and := func(cond string, arg any) {
		args = append(args, arg)
		fmt.Fprintf(&q, "\n  AND "+cond, len(args))
	}
	if req.PrincipalType != limes.PrincipalTypeUnspecified {
		and("c.principal_type = $%d", string(req.PrincipalType))
	}
	if req.Subject != "" {
		and("c.subject = $%d", req.Subject)
	}
	if req.SubjectPrefix != "" {
		and("starts_with(c.subject, $%d)", req.SubjectPrefix)
	}
	if len(req.Dispositions) > 0 {
		var conds []string
		for _, d := range []limes.RecordDisposition{limes.RecordDispositionActive, limes.RecordDispositionExpired, limes.RecordDispositionRevoked} {
			if req.Admits(d) {
				conds = append(conds, dispositionWhere[d])
			}
		}
		// Not revoked, as both of its own dispositions are: the planner
		// proves the partial index from that conjunct, not from the
		// disjunction.
		if !req.Admits(limes.RecordDispositionRevoked) {
			conds = append([]string{"NOT c.revoked AND"}, "("+strings.Join(conds, " OR ")+")")
			fmt.Fprintf(&q, "\n  AND %s", strings.Join(conds, " "))
		} else {
			fmt.Fprintf(&q, "\n  AND (%s)", strings.Join(conds, " OR "))
		}
	}
	order := "ASC"
	if req.Order == limes.ListOrderDescending {
		order = "DESC"
		if after != nil {
			and("c.id < $%d", *after)
		}
	} else if after != nil {
		and("c.id > $%d", *after)
	}
	args = append(args, limit)
	fmt.Fprintf(&q, "\nORDER BY c.id %s\nLIMIT $%d", order, len(args))
	return q.String(), args
}

// listCursor is pgstore's cursor of ListRecords: the last record's id, and
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

// ListRecords implements limes.RecordLister in one transaction, scoped to
// the tenant the request names (Config.CheckScope), as ListByPrincipal is.
func (c *Census) ListRecords(ctx context.Context, req limes.ListRecordsRequest) (limes.Page[limes.RecordListing], error) {
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
	limit := limes.PageLimit(req.Limit)
	query, args := listRecordsSQL(req, c.clock(), after, limit+1)
	out := []limes.RecordListing{}
	err := c.inTenantReadTx(ctx, req.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, query, args...)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, scanListing)
		return err
	})
	if err != nil {
		return limes.Page[limes.RecordListing]{}, err
	}
	if len(out) <= limit {
		return limes.Page[limes.RecordListing]{Items: out}, nil
	}
	out = out[:limit]
	next := listCursor{After: out[len(out)-1].Record.Capability.ID, Order: req.Order, Filters: filtersKey(req)}
	return limes.Page[limes.RecordListing]{Items: out, Next: encodeCursor(next)}, nil
}

// scanListing reads one row of sqlListRecords.
func scanListing(row pgx.CollectableRow) (limes.RecordListing, error) {
	var capability, issuedBy []byte
	var revokedAt *time.Time
	var note, actor *string
	var cascaded *bool
	var disposition string
	var ancestor *uuid.UUID
	var ancestorAt *time.Time
	var ancestorNote, ancestorActor *string
	var ancestorCascaded *bool
	if err := row.Scan(&capability, &issuedBy, &revokedAt, &note, &actor, &cascaded, &disposition,
		&ancestor, &ancestorAt, &ancestorNote, &ancestorActor, &ancestorCascaded); err != nil {
		return limes.RecordListing{}, err
	}
	c, err := decodeCapability(capability)
	if err != nil {
		return limes.RecordListing{}, err
	}
	p, err := decodePrincipal(issuedBy)
	if err != nil {
		return limes.RecordListing{}, err
	}
	l := limes.RecordListing{Record: limes.Record{Capability: c, IssuedBy: p}, Disposition: limes.RecordDisposition(disposition)}
	if revokedAt != nil {
		l.Record.Revocation = &limes.Revocation{RevokedAt: *revokedAt, Note: *note, Actor: *actor, Cascaded: *cascaded}
	}
	switch {
	case l.Disposition != limes.RecordDispositionRevoked:
	case l.Record.Revocation != nil:
		l.StoppedBy = &limes.CapabilityRevocation{CapabilityID: c.ID, Revocation: *l.Record.Revocation}
	case ancestor != nil:
		l.StoppedBy = &limes.CapabilityRevocation{CapabilityID: *ancestor, Revocation: limes.Revocation{
			RevokedAt: *ancestorAt, Note: *ancestorNote, Actor: *ancestorActor, Cascaded: *ancestorCascaded,
		}}
	}
	return l, nil
}

// decodeListCursor reads a cursor ListRecords handed out for req's order and
// filters. One of the other order or of other filters is ErrCursorMismatch,
// anything that is no cursor ErrInvalidRequest.
func decodeListCursor(s string, req limes.ListRecordsRequest) (listCursor, error) {
	var k listCursor
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err == nil {
		err = json.Unmarshal(raw, &k)
	}
	if err != nil || k.After == uuid.Nil {
		return listCursor{}, fmt.Errorf("%w: pgstore: record list cursor %q", limes.ErrInvalidRequest, s)
	}
	if k.Order != req.Order || k.Filters != filtersKey(req) {
		return listCursor{}, fmt.Errorf("%w: pgstore: record list cursor %q", limes.ErrCursorMismatch, s)
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

var (
	_ limes.RecordCensus        = (*Census)(nil)
	_ limes.RecordLister        = (*Census)(nil)
	_ limes.RecordListingReader = (*Census)(nil)
	_ limes.RecordReporter      = (*Census)(nil)
)
