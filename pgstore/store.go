package pgstore

import (
	"context"
	"errors"
	"fmt"
	"time"
	"uuid"

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
// record, in the same tenant, and expire no earlier than the child, and the
// child may have no more than limes.MaxDelegationDepth ancestors on record.
// A tenant whose usage EraseTenantUsage removed is refused with
// limes.ErrTenantErased.
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
	err = s.inTenantTx(ctx, c.Subject.TenantID, func(tx pgx.Tx) error {
		return s.insert(ctx, tx, c, capability, issuedBy)
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return fmt.Errorf("%w: %s", limes.ErrAlreadyExists, c.ID)
	}
	return err
}

// insert is Insert in tx, c encoded as capability and its issuer as
// issuedBy.
func (s *Store) insert(ctx context.Context, tx pgx.Tx, c limes.Capability, capability, issuedBy []byte) error {
	// Shares the tenant's erase lock: an erase that commits first is
	// seen, one that comes after waits for this insert.
	if err := notErased(ctx, tx, c.Subject.TenantID); err != nil {
		return err
	}
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
	depth, parentID, err := childDepth(ctx, tx, c)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, sqlInsertCapability,
		c.ID, parentID, c.Subject.TenantID, string(c.Subject.Type), c.Subject.Subject,
		nullTime(c.ExpiresAt), depth, capability, issuedBy)
	return err
}

// childDepth is c's depth and its parent's id, nil for a root; a child is
// held to its parent, under the tenant's revoke lock shared.
func childDepth(ctx context.Context, tx pgx.Tx, c limes.Capability) (int, *uuid.UUID, error) {
	if c.ParentID == uuid.Nil() {
		return 0, nil, nil
	}
	if err := shareRevokeLock(ctx, tx, c.Subject.TenantID); err != nil {
		return 0, nil, err
	}
	parentDepth, err := checkParent(ctx, tx, c)
	if err != nil {
		return 0, nil, err
	}
	return parentDepth + 1, &c.ParentID, nil
}

// checkParent holds c's parent on record for the rest of tx and refuses a
// child it cannot keep: under a parent not on record, outliving it (with no
// expiry under a parent that has one included), in
// another tenant, or past limes.MaxDelegationDepth. It returns the parent's
// depth.
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
	if err := checkUnderParent(c, parent); err != nil {
		return 0, err
	}
	// The child's ancestors are its parent and the parent's own, counted as
	// IsRevoked counts them: those still on record. The depth column keeps
	// the depth at insertion, which a purged ancestor does not lower.
	rows, err := tx.Query(ctx, sqlLineage, c.ParentID, limes.MaxDelegationDepth)
	if err != nil {
		return 0, err
	}
	chain, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return 0, err
	}
	if len(chain) > limes.MaxDelegationDepth {
		return 0, fmt.Errorf("%w: %s would have more than %d ancestors", limes.ErrDelegationTooDeep, c.ID, limes.MaxDelegationDepth)
	}
	return depth, nil
}

// checkUnderParent refuses a child its parent does not allow: a later
// expiry, or none under one that expires — Purge could not drop the parent
// while the child, which it never purges, still refers to it — another
// tenant, or another budget unit: metering a child spends from every
// ancestor, so the lineage counts in one unit.
func checkUnderParent(c, parent limes.Capability) error {
	switch {
	case c.ExpiresAt.After(parent.ExpiresAt):
		return fmt.Errorf("%w: expires %s, after its parent %s", limes.ErrDelegationTooWide, c.ExpiresAt, parent.ExpiresAt)
	case c.ExpiresAt.IsZero() && !parent.ExpiresAt.IsZero():
		return fmt.Errorf("%w: never expires, under a parent expiring %s", limes.ErrDelegationTooWide, parent.ExpiresAt)
	case c.Subject.TenantID != parent.Subject.TenantID:
		return fmt.Errorf("%w: tenant %s, not its parent's %s", limes.ErrDelegationTooWide, c.Subject.TenantID, parent.Subject.TenantID)
	case c.Caveats.Budget.UnitCode != parent.Caveats.Budget.UnitCode:
		return fmt.Errorf("%w: budget in %q, under a parent in %q", limes.ErrUnitCodeMismatch, c.Caveats.Budget.UnitCode, parent.Caveats.Budget.UnitCode)
	}
	return nil
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
	err := s.inReadTx(ctx, func(tx pgx.Tx) error {
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

// sqlIsRevoked asks limes.is_revoked (migrations/004_revocation_checks.sql)
// in one statement, outside any transaction: one round trip on the path
// every request takes.
const sqlIsRevoked = `SELECT self, deepest, in_chain FROM limes.is_revoked($1, $2)`

// sqlRevocationLineage asks limes.revocation_lineage
// (migrations/018_revocation_lineage.sql), as sqlIsRevoked asks is_revoked.
const sqlRevocationLineage = `SELECT self, deepest, in_chain, ancestors, known FROM limes.revocation_lineage($1, $2)`

// IsRevoked answers for the capability and every ancestor still on record,
// across every tenant whatever the call's scope, in one round trip: the
// verifier asks it before it knows the token's tenant, on every request.
// Ask the primary, as RevocationLineage: a standby behind a revocation
// answers "not revoked", and a RevocationCache, cleared by the revocation's
// notification an instant before, keeps that answer for its SyncedTTL.
func (s *Store) IsRevoked(ctx context.Context, id uuid.UUID) (bool, error) {
	var self, inChain bool
	var deepest int
	err := s.db.QueryRow(ctx, sqlIsRevoked, id, limes.MaxDelegationDepth).Scan(&self, &deepest, &inChain)
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

// RevocationLineage implements limes.RevocationLineageLookup: IsRevoked's
// answer, in one statement as IsRevoked's, and the ancestors on record it
// read, nearest first; an id not on record is ErrNotFound. Like IsRevoked it
// reads every tenant whatever the call's scope, so it names the ancestors'
// ids, and whether an id is on record at all, to the role that calls it:
// expose it to no one who may not learn another tenant's ids. Migration 020
// dropped and recreated limes.revocation_lineage, and Migrate grants it only
// to MigrateOptions.RuntimeRoles: an EXECUTE granted by hand to another role
// before then is gone and must be granted again.
func (s *Store) RevocationLineage(ctx context.Context, id uuid.UUID) (limes.RevocationAnswer, error) {
	var self, inChain, known bool
	var deepest int
	var ancestors []uuid.UUID
	err := s.db.QueryRow(ctx, sqlRevocationLineage, id, limes.MaxDelegationDepth).Scan(&self, &deepest, &inChain, &ancestors, &known)
	switch {
	case err != nil:
		return limes.RevocationAnswer{}, err
	case !known:
		return limes.RevocationAnswer{}, limes.ErrNotFound
	case self:
		return limes.RevocationAnswer{Revoked: true, Ancestors: ancestors}, nil
	case deepest > limes.MaxDelegationDepth:
		return limes.RevocationAnswer{}, fmt.Errorf("%w: %s has more than %d ancestors", limes.ErrDelegationTooDeep, id, limes.MaxDelegationDepth)
	}
	return limes.RevocationAnswer{Revoked: inChain, Ancestors: ancestors}, nil
}

const (
	sqlCapabilityTenant = `SELECT tenant_id FROM limes.capabilities WHERE id = $1`

	// sqlRevokeTarget is the tenant of the capability Revoke names, and what
	// its disposition is read from: the revoked flag and the expiry. The row
	// is held for no key update until the revoke commits: an ancestor's
	// revoke marking it waits, or is waited for and read, so the disposition
	// is the one the entry is written against; Purge cannot drop it between
	// this read and the entry's insert; and a second revoke of it waits
	// here for the first to commit, then reads its entry. Held for share,
	// two revokes at once each held the row the other's mark had to update,
	// and one ended in a deadlock. Key share, a metered lineage's lock, does
	// not conflict with it.
	sqlRevokeTarget = `SELECT tenant_id, revoked, expires_at FROM limes.capabilities WHERE id = $1 FOR NO KEY UPDATE`

	sqlRevoke = `
INSERT INTO limes.revocations (capability_id, tenant_id, revoked_at, note, actor, cascaded)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (capability_id) DO NOTHING
RETURNING revoked_at, note, actor, cascaded`

	// sqlRevocation reads the entry that kept sqlRevoke from writing. A
	// statement of its own: under READ COMMITTED it sees an entry another
	// transaction committed while sqlRevoke waited on it, which a snapshot
	// taken before that wait would not.
	sqlRevocation = `
SELECT revoked_at, note, actor, cascaded FROM limes.revocations WHERE capability_id = $1`

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
// one for every descendant. Each entry also marks its record, and every
// record below it not marked already, revoked — the flag the census and the
// listing read (migrations/015_revoked_flag.sql) — one UPDATE a generation,
// by key and by parent, inside this transaction. It first takes the
// tenant's revoke lock alone, which every insert of a child shares
// (lockForRevoke): the revoke waits for the inserts and revokes of the
// tenant already running, and those that come after wait for it, so no
// stream of delegations under the capability holds it back. Its row locks
// on the subtree then last until it commits. A metered call's lineage lock
// (FOR KEY SHARE) does not conflict with the mark, so charges neither wait
// for a revoke nor hold one back. A cascading revoke marks the subtree once, with its root's
// entry; a capability under one revoked before costs nothing past its own
// row. The first entry of each stands, and the result carries the
// capability's.
func (s *Store) Revoke(ctx context.Context, req limes.RevokeRequest) (limes.RevokeResult, error) {
	if err := req.Validate(); err != nil {
		return limes.RevokeResult{}, err
	}
	var result limes.RevokeResult
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var err error
		result, err = s.revoke(ctx, tx, req)
		return err
	})
	if err != nil {
		return limes.RevokeResult{}, err
	}
	return result, nil
}

// revoke is Revoke in tx: the target's tenant is read to take its revoke
// lock, then the target itself, held.
func (s *Store) revoke(ctx context.Context, tx pgx.Tx, req limes.RevokeRequest) (limes.RevokeResult, error) {
	var tenant uuid.UUID
	if err := scanFound(tx.QueryRow(ctx, sqlCapabilityTenant, req.CapabilityID), &tenant); err != nil {
		return limes.RevokeResult{}, err
	}
	if err := lockForRevoke(ctx, tx, tenant); err != nil {
		return limes.RevokeResult{}, err
	}
	var revoked bool
	var expiresAt *time.Time
	if err := scanFound(tx.QueryRow(ctx, sqlRevokeTarget, req.CapabilityID), &tenant, &revoked, &expiresAt); err != nil {
		return limes.RevokeResult{}, err
	}
	now := s.clock()
	result := limes.RevokeResult{PriorDisposition: disposition(revoked, expiresAt, now)}
	if err := writeRevocation(ctx, tx, req, tenant, now, &result); err != nil {
		return limes.RevokeResult{}, err
	}
	if !req.CascadeChildren {
		return result, nil
	}
	_, err := tx.Exec(ctx, sqlRevokeDescendants, req.CapabilityID, limes.MaxDelegationDepth, now, req.Note, req.Actor)
	return result, err
}

// scanFound scans row into dest, a missing row as limes.ErrNotFound.
func scanFound(row pgx.Row, dest ...any) error {
	err := row.Scan(dest...)
	if errors.Is(err, pgx.ErrNoRows) {
		return limes.ErrNotFound
	}
	return err
}

// writeRevocation writes req's entry into result, or, when the target's own
// entry was there, or another revoke wrote it while this one waited, reads
// that one: revoked before this call either way, whatever the flag read
// first said.
func writeRevocation(ctx context.Context, tx pgx.Tx, req limes.RevokeRequest, tenant uuid.UUID, now time.Time, result *limes.RevokeResult) error {
	r := &result.Revocation
	err := tx.QueryRow(ctx, sqlRevoke, req.CapabilityID, tenant, now, req.Note, req.Actor, req.CascadeChildren).Scan(&r.RevokedAt, &r.Note, &r.Actor, &r.Cascaded)
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	result.AlreadyRevoked, result.PriorDisposition = true, limes.RecordDispositionRevoked
	return tx.QueryRow(ctx, sqlRevocation, req.CapabilityID).Scan(&r.RevokedAt, &r.Note, &r.Actor, &r.Cascaded)
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
	err := s.inTenantReadTx(ctx, req.TenantID, func(tx pgx.Tx) error {
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
	// sqlIsBiscuitRevoked asks limes.is_biscuit_revoked in one statement,
	// as sqlIsRevoked does.
	sqlIsBiscuitRevoked = `SELECT limes.is_biscuit_revoked($1)`

	sqlRevokeBiscuit = `
INSERT INTO limes.biscuit_revocations (revocation_id, capability_id, tenant_id, revoked_at, note, actor)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (revocation_id, capability_id) DO NOTHING
RETURNING revoked_at, note, actor`

	sqlGetBiscuitRevocation = `
SELECT revoked_at, note, actor FROM limes.biscuit_revocations WHERE revocation_id = $1 AND capability_id = $2`
)

// IsBiscuitRevoked reports whether any of revocationIDs is revoked, across
// every tenant whatever the call's scope, in one round trip.
func (s *Store) IsBiscuitRevoked(ctx context.Context, revocationIDs [][]byte) (bool, error) {
	if len(revocationIDs) == 0 {
		return false, nil
	}
	var revoked bool
	err := s.db.QueryRow(ctx, sqlIsBiscuitRevoked, revocationIDs).Scan(&revoked)
	return revoked, err
}

// RevokeBiscuit lists one copy's revocation id under its capability; the
// first entry stands, and the result carries it. The entry another revoke
// wrote is read in a statement of its own, as Revoke reads one: under READ
// COMMITTED it sees what committed while the insert waited.
func (s *Store) RevokeBiscuit(ctx context.Context, req limes.RevokeBiscuitRequest) (limes.RevokeBiscuitResult, error) {
	if err := req.Validate(); err != nil {
		return limes.RevokeBiscuitResult{}, err
	}
	capID, revocationID := req.Copy.CapabilityID(), req.Copy.RevocationID()
	var result limes.RevokeBiscuitResult
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var tenant uuid.UUID
		err := tx.QueryRow(ctx, sqlCapabilityTenant, capID).Scan(&tenant)
		if errors.Is(err, pgx.ErrNoRows) {
			return limes.ErrNotFound
		}
		if err != nil {
			return err
		}
		result = limes.RevokeBiscuitResult{Revocation: limes.BiscuitRevocation{CapabilityID: capID, RevocationID: revocationID}}
		r := &result.Revocation
		err = tx.QueryRow(ctx, sqlRevokeBiscuit, revocationID, capID, tenant, s.clock(), req.Note, req.Actor).Scan(&r.RevokedAt, &r.Note, &r.Actor)
		if errors.Is(err, pgx.ErrNoRows) {
			result.AlreadyRevoked = true
			err = tx.QueryRow(ctx, sqlGetBiscuitRevocation, revocationID, capID).Scan(&r.RevokedAt, &r.Note, &r.Actor)
		}
		return err
	})
	if err != nil {
		return limes.RevokeBiscuitResult{}, err
	}
	return result, nil
}

// GetBiscuitRevocation reads back what RevokeBiscuit wrote for the copy.
func (s *Store) GetBiscuitRevocation(ctx context.Context, bc limes.BiscuitCopy) (limes.BiscuitRevocation, error) {
	if err := bc.Validate(); err != nil {
		return limes.BiscuitRevocation{}, err
	}
	r := limes.BiscuitRevocation{CapabilityID: bc.CapabilityID(), RevocationID: bc.RevocationID()}
	err := s.inReadTx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, sqlGetBiscuitRevocation, r.RevocationID, r.CapabilityID).Scan(&r.RevokedAt, &r.Note, &r.Actor)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return limes.BiscuitRevocation{}, limes.ErrNotFound
	}
	if err != nil {
		return limes.BiscuitRevocation{}, err
	}
	return r, nil
}

var (
	_ limes.RevocationLineageLookup = (*Store)(nil)
	_ limes.Store                   = (*Store)(nil)
	_ limes.BiscuitRevocationStore  = (*Store)(nil)
)

// disposition is a record's disposition at now from its revoked flag and
// its expiry, by the census's rules (census.go's sqlRecordListing): revoked,
// then expired — a record with no expiry among them — then active.
func disposition(revoked bool, expiresAt *time.Time, now time.Time) limes.RecordDisposition {
	switch {
	case revoked:
		return limes.RecordDispositionRevoked
	case expiresAt == nil || !expiresAt.After(now):
		return limes.RecordDispositionExpired
	}
	return limes.RecordDispositionActive
}
