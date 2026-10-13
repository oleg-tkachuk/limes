package pgstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/oleg-tkachuk/limes"
)

// Store implements limes.Store and limes.BiscuitRevocationStore: capability
// records, their revocation entries and the revoked Biscuit copies.
type Store struct {
	conn
}

// NewStore returns a store over cfg.DB, whose schema Migrate created.
func NewStore(cfg Config) (*Store, error) {
	c, err := newConn(cfg)
	if err != nil {
		return nil, err
	}
	return &Store{c}, nil
}

// uniqueViolation is the SQLSTATE of a duplicate key.
const uniqueViolation = "23505"

const (
	sqlCapabilityExists = `SELECT EXISTS (SELECT 1 FROM limes.capabilities WHERE id = $1)`

	// The parent is locked for share, so Purge cannot remove it before its
	// child is on record.
	sqlLockParent = `SELECT depth, capability FROM limes.capabilities WHERE id = $1 FOR SHARE`

	sqlInsertCapability = `
INSERT INTO limes.capabilities
    (id, parent_id, tenant_id, principal_type, subject, expires_at, depth, capability, issued_by)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`
)

// Insert records req.Capability and who asked for it. The parent must be on
// record, in the same tenant, and expire no earlier than the child.
func (s *Store) Insert(ctx context.Context, req limes.InsertRequest) error {
	if err := req.Validate(); err != nil {
		return err
	}
	c := req.Capability
	capability, err := encodeCapability(c)
	if err != nil {
		return err
	}
	issuedBy, err := encodePrincipal(req.IssuedBy)
	if err != nil {
		return err
	}
	err = s.inTx(ctx, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, sqlCapabilityExists, c.ID).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return fmt.Errorf("%w: %s", limes.ErrAlreadyExists, c.ID)
		}
		if s.checkTenant != nil {
			if err := s.checkTenant(ctx, tx, c.Subject.TenantID); err != nil {
				return err
			}
		}
		depth := 0
		var parentID *uuid.UUID
		if c.ParentID != uuid.Nil {
			parentDepth, err := checkParent(ctx, tx, c)
			if err != nil {
				return err
			}
			depth = parentDepth + 1
			parentID = &c.ParentID
		}
		_, err := tx.Exec(ctx, sqlInsertCapability,
			c.ID, parentID, c.Subject.TenantID, string(c.Subject.Type), c.Subject.Subject,
			nullTime(c.ExpiresAt), depth, capability, issuedBy)
		return err
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return fmt.Errorf("%w: %s", limes.ErrAlreadyExists, c.ID)
	}
	return err
}

// checkParent holds c's parent on record for the rest of tx and refuses a
// child it cannot keep: under a parent not on record, outliving it, or in
// another tenant. It returns the parent's depth.
func checkParent(ctx context.Context, tx pgx.Tx, c limes.Capability) (int, error) {
	var depth int
	var raw []byte
	err := tx.QueryRow(ctx, sqlLockParent, c.ParentID).Scan(&depth, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("%w: pgstore: parent %s is not on record", limes.ErrInvalidRequest, c.ParentID)
	}
	if err != nil {
		return 0, err
	}
	parent, err := decodeCapability(raw)
	if err != nil {
		return 0, err
	}
	if c.ExpiresAt.After(parent.ExpiresAt) {
		return 0, fmt.Errorf("%w: expires %s, after its parent %s", limes.ErrDelegationTooWide, c.ExpiresAt, parent.ExpiresAt)
	}
	if c.Subject.TenantID != parent.Subject.TenantID {
		return 0, fmt.Errorf("%w: tenant %s, not its parent's %s", limes.ErrDelegationTooWide, c.Subject.TenantID, parent.Subject.TenantID)
	}
	return depth, nil
}

// nullTime is t for a timestamptz column, NULL for the zero time.
func nullTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

const sqlGetRecord = `
SELECT c.capability, c.issued_by, r.revoked_at, r.note, r.actor, r.cascaded
FROM limes.capabilities c
LEFT JOIN limes.revocations r ON r.capability_id = c.id
WHERE c.id = $1`

// GetRecord reads back the capability, who issued it and its own revocation.
func (s *Store) GetRecord(ctx context.Context, id uuid.UUID) (limes.Record, error) {
	var rec limes.Record
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var capability, issuedBy []byte
		var revokedAt *time.Time
		var note, actor *string
		var cascaded *bool
		if err := tx.QueryRow(ctx, sqlGetRecord, id).Scan(&capability, &issuedBy, &revokedAt, &note, &actor, &cascaded); err != nil {
			return err
		}
		c, err := decodeCapability(capability)
		if err != nil {
			return err
		}
		p, err := decodePrincipal(issuedBy)
		if err != nil {
			return err
		}
		rec = limes.Record{Capability: c, IssuedBy: p}
		if revokedAt != nil {
			rec.Revocation = &limes.Revocation{RevokedAt: *revokedAt, Note: *note, Actor: *actor, Cascaded: *cascaded}
		}
		return nil
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return limes.Record{}, limes.ErrNotFound
	}
	return rec, err
}

// sqlIsRevoked walks the chain from $1 up in one round trip, to one ancestor
// past $2 (limes.MaxDelegationDepth), which is how a chain too deep shows.
const sqlIsRevoked = `
WITH RECURSIVE chain (id, parent_id, depth) AS (
    SELECT id, parent_id, 0 FROM limes.capabilities WHERE id = $1
  UNION ALL
    SELECT c.id, c.parent_id, chain.depth + 1
    FROM limes.capabilities c
    JOIN chain ON c.id = chain.parent_id
    WHERE chain.depth <= $2
)
SELECT
    EXISTS (SELECT 1 FROM limes.revocations WHERE capability_id = $1),
    coalesce(max(depth), 0),
    EXISTS (SELECT 1 FROM chain JOIN limes.revocations r ON r.capability_id = chain.id)
FROM chain`

// IsRevoked answers for the capability and every ancestor still on record,
// across every tenant whatever the call's scope.
func (s *Store) IsRevoked(ctx context.Context, id uuid.UUID) (bool, error) {
	var self, inChain bool
	var deepest int
	err := s.inTxAllTenants(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, sqlIsRevoked, id, limes.MaxDelegationDepth).Scan(&self, &deepest, &inChain)
	})
	switch {
	case err != nil:
		return false, err
	case self:
		return true, nil
	case deepest > limes.MaxDelegationDepth:
		return false, fmt.Errorf("%w: %s has more than %d ancestors", limes.ErrDelegationTooDeep, id, limes.MaxDelegationDepth)
	}
	return inChain, nil
}

const (
	sqlCapabilityTenant = `SELECT tenant_id FROM limes.capabilities WHERE id = $1`

	sqlRevoke = `
INSERT INTO limes.revocations (capability_id, tenant_id, revoked_at, note, actor, cascaded)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (capability_id) DO NOTHING`

	// sqlRevokeDescendants writes an entry for every descendant of $1 to
	// $2 (limes.MaxDelegationDepth) generations below it, as IsRevoked walks.
	sqlRevokeDescendants = `
WITH RECURSIVE tree (id, tenant_id, generation) AS (
    SELECT id, tenant_id, 1 FROM limes.capabilities WHERE parent_id = $1
  UNION ALL
    SELECT c.id, c.tenant_id, tree.generation + 1
    FROM limes.capabilities c
    JOIN tree ON c.parent_id = tree.id
    WHERE tree.generation < $2
)
INSERT INTO limes.revocations (capability_id, tenant_id, revoked_at, note, actor, cascaded)
SELECT id, tenant_id, $3, $4, $5, true FROM tree
ON CONFLICT (capability_id) DO NOTHING`
)

// Revoke writes the capability's revocation entry, and with CascadeChildren
// one for every descendant. The first entry of each stands.
func (s *Store) Revoke(ctx context.Context, req limes.RevokeRequest) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		var tenant uuid.UUID
		err := tx.QueryRow(ctx, sqlCapabilityTenant, req.CapabilityID).Scan(&tenant)
		if errors.Is(err, pgx.ErrNoRows) {
			return limes.ErrNotFound
		}
		if err != nil {
			return err
		}
		now := s.clock()
		if _, err := tx.Exec(ctx, sqlRevoke, req.CapabilityID, tenant, now, req.Note, req.Actor, req.CascadeChildren); err != nil {
			return err
		}
		if !req.CascadeChildren {
			return nil
		}
		_, err = tx.Exec(ctx, sqlRevokeDescendants, req.CapabilityID, limes.MaxDelegationDepth, now, req.Note, req.Actor)
		return err
	})
}

// sqlPurge removes the oldest-expired records past the cutoff, deepest first
// among those expiring together, so a batch cut at its limit takes a child
// before its parent. Their revocation entries go with them (ON DELETE
// CASCADE). The batch is locked in id order, the order the meter holds a
// lineage's records in, so the two never wait on each other in a cycle.
const sqlPurge = `
WITH due AS (
    SELECT id FROM limes.capabilities
    WHERE expires_at < $1
    ORDER BY expires_at, depth DESC, id
    LIMIT $2
), locked AS (
    SELECT id FROM limes.capabilities
    WHERE id IN (SELECT id FROM due)
    ORDER BY id
    FOR UPDATE
)
DELETE FROM limes.capabilities c USING locked WHERE c.id = locked.id`

// Purge removes the capabilities expired for longer than the retention.
func (s *Store) Purge(ctx context.Context, req limes.PurgeRequest) (limes.PurgeResult, error) {
	if err := req.Validate(); err != nil {
		return limes.PurgeResult{}, err
	}
	limit := req.Limit
	if limit == 0 {
		limit = limes.DefaultHousekeepingLimit
	}
	var purged int64
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, sqlPurge, s.clock().Add(-req.Retention()), limit)
		purged = tag.RowsAffected()
		return err
	})
	if err != nil {
		return limes.PurgeResult{}, err
	}
	return limes.PurgeResult{Purged: purged}, nil
}

// sqlListByPrincipal pages one principal's capabilities in ascending id,
// which PostgreSQL compares bytewise, as limes orders ids. A NULL expiry is
// expired, as a zero ExpiresAt is.
const sqlListByPrincipal = `
SELECT c.capability FROM limes.capabilities c
WHERE c.tenant_id = $1 AND c.principal_type = $2 AND c.subject = $3
  AND ($4 OR c.expires_at > $5)
  AND ($6 OR NOT EXISTS (SELECT 1 FROM limes.revocations r WHERE r.capability_id = c.id))
  AND ($7::uuid IS NULL OR c.id > $7)
ORDER BY c.id
LIMIT $8`

// ListByPrincipal lists one principal's capabilities a page at a time; the
// cursor is the last id of the page.
func (s *Store) ListByPrincipal(ctx context.Context, req limes.ListByPrincipalRequest) (limes.Page[limes.Capability], error) {
	if err := req.Validate(); err != nil {
		return limes.Page[limes.Capability]{}, err
	}
	var after *uuid.UUID
	if req.Cursor != "" {
		id, err := uuid.Parse(req.Cursor)
		if err != nil {
			return limes.Page[limes.Capability]{}, fmt.Errorf("%w: pgstore: cursor %q", limes.ErrInvalidRequest, req.Cursor)
		}
		after = &id
	}
	limit := limes.PageLimit(req.Limit)
	out := []limes.Capability{}
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, sqlListByPrincipal,
			req.TenantID, string(req.PrincipalType), req.Subject,
			req.IncludeExpired, s.clock(), req.IncludeRevoked, after, limit+1)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (limes.Capability, error) {
			var raw []byte
			if err := row.Scan(&raw); err != nil {
				return limes.Capability{}, err
			}
			return decodeCapability(raw)
		})
		return err
	})
	if err != nil {
		return limes.Page[limes.Capability]{}, err
	}
	if len(out) <= limit {
		return limes.Page[limes.Capability]{Items: out}, nil
	}
	out = out[:limit]
	return limes.Page[limes.Capability]{Items: out, Next: out[len(out)-1].ID.String()}, nil
}

const (
	sqlIsBiscuitRevoked = `SELECT EXISTS (SELECT 1 FROM limes.biscuit_revocations WHERE revocation_id = ANY ($1))`

	sqlRevokeBiscuit = `
INSERT INTO limes.biscuit_revocations (revocation_id, capability_id, tenant_id, revoked_at, note, actor)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (revocation_id) DO NOTHING`

	sqlGetBiscuitRevocation = `
SELECT capability_id, revoked_at, note, actor FROM limes.biscuit_revocations WHERE revocation_id = $1`
)

// IsBiscuitRevoked reports whether any of revocationIDs is revoked, across
// every tenant whatever the call's scope.
func (s *Store) IsBiscuitRevoked(ctx context.Context, revocationIDs [][]byte) (bool, error) {
	if len(revocationIDs) == 0 {
		return false, nil
	}
	var revoked bool
	err := s.inTxAllTenants(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, sqlIsBiscuitRevoked, revocationIDs).Scan(&revoked)
	})
	return revoked, err
}

// RevokeBiscuit lists one copy's revocation id under its capability; the
// first entry stands.
func (s *Store) RevokeBiscuit(ctx context.Context, req limes.RevokeBiscuitRequest) error {
	if err := req.Validate(); err != nil {
		return err
	}
	capID, revocationID := req.Copy.CapabilityID(), req.Copy.RevocationID()
	return s.inTx(ctx, func(tx pgx.Tx) error {
		var tenant uuid.UUID
		err := tx.QueryRow(ctx, sqlCapabilityTenant, capID).Scan(&tenant)
		if errors.Is(err, pgx.ErrNoRows) {
			return limes.ErrNotFound
		}
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, sqlRevokeBiscuit, revocationID, capID, tenant, s.clock(), req.Note, req.Actor)
		return err
	})
}

// GetBiscuitRevocation reads back what RevokeBiscuit wrote for revocationID.
func (s *Store) GetBiscuitRevocation(ctx context.Context, revocationID []byte) (limes.BiscuitRevocation, error) {
	r := limes.BiscuitRevocation{RevocationID: revocationID}
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, sqlGetBiscuitRevocation, revocationID).Scan(&r.CapabilityID, &r.RevokedAt, &r.Note, &r.Actor)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return limes.BiscuitRevocation{}, limes.ErrNotFound
	}
	if err != nil {
		return limes.BiscuitRevocation{}, err
	}
	r.RevocationID = append([]byte(nil), revocationID...)
	return r, nil
}

var (
	_ limes.Store                  = (*Store)(nil)
	_ limes.BiscuitRevocationStore = (*Store)(nil)
)
