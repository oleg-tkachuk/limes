package pgstore

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/oleg-tkachuk/limes/dpop"

	"github.com/oleg-tkachuk/limes"
)

// ReplayCache is a dpop.ReplayCache over PostgreSQL: one table,
// limes.dpop_seen_proofs, that every replica of a verifier shares, so a
// proof accepted on one is refused on all of them until it expires.
//
// It is for a deployment that runs pgstore already and would rather not run
// Redis (redisreplay) beside it, or keep the ids in one process alone
// (dpop.MemoryReplayCache). The check runs as the runtime role before any
// tenant is known: the table has no row-level security, and the cache sets
// no scope.
//
// Each holder — the capability whose proofs they are — has a share of the
// cache: Seen refuses, as a replay, an id of a holder that already has
// EntriesPerHolder live ids, whatever key signed them. The count and the
// insert happen under a transaction-scoped advisory lock on the holder, so
// concurrent proofs of one holder never take it past its share. Such a
// refusal, unlike a replay, is logged at WARN.
//
// What is live is decided by Now, the verifier's clock, never the
// database's. An expired id counts for nothing, and the same id may take
// its place; PurgeExpired deletes expired ids, which a job should call until
// it returns 0.
type ReplayCache struct {
	db         DB
	share      int
	now        func() time.Time
	log        *slog.Logger
	purgeBatch int
}

var _ dpop.ReplayCache = (*ReplayCache)(nil)

// ReplayConfig configures a ReplayCache.
type ReplayConfig struct {
	// DB is the database, migrated by pgstore/migrate, as the runtime role.
	// Required.
	DB DB
	// EntriesPerHolder bounds the live proof ids of one holder. Zero:
	// dpop.DefaultReplayEntriesPerHolder; negative is refused.
	EntriesPerHolder int
	// Now is the clock. Nil: time.Now.
	Now func() time.Time
	// Logger logs a holder refused for its share. Nil: slog.Default().
	Logger *slog.Logger
}

// DefaultReplayPurgeBatch is how many expired ids one PurgeExpired deletes
// at most: a statement that holds its rows for a bounded time.
const DefaultReplayPurgeBatch = 10_000

// errNoReplayDB is a ReplayConfig with no DB.
var errNoReplayDB = fmt.Errorf("%w: pgstore: ReplayConfig.DB required", limes.ErrInvalidConfig)

// NewReplayCache builds a ReplayCache, refusing a config with no DB or a
// negative share.
func NewReplayCache(cfg ReplayConfig) (*ReplayCache, error) {
	if isNil(cfg.DB) {
		return nil, errNoReplayDB
	}
	if cfg.EntriesPerHolder < 0 {
		return nil, fmt.Errorf("%w: pgstore: replay cache share of one holder %d is negative", limes.ErrInvalidConfig, cfg.EntriesPerHolder)
	}
	if cfg.EntriesPerHolder == 0 {
		cfg.EntriesPerHolder = dpop.DefaultReplayEntriesPerHolder
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &ReplayCache{db: cfg.DB, share: cfg.EntriesPerHolder, now: cfg.Now, log: cfg.Logger, purgeBatch: DefaultReplayPurgeBatch}, nil
}

// replayTable is where the cache keeps proof ids
// (migrate/ddl/migrations/023_dpop_seen_proofs.sql).
const replayTable = schema + ".dpop_seen_proofs"

// replayLockClass is the first key of the advisory lock that makes one
// holder's count and insert one decision.
const replayLockClass int32 = 0x6c696470

const (
	// sqlRecordProof records $1/$2 for holder $3 until $4, unless the id is
	// live at $5 or the holder already has $6 live ids: one row inserted or
	// an expired one taken over is a new id; none, a replay or a refusal.
	sqlRecordProof = `
WITH live AS (
    SELECT count(*) AS n FROM ` + replayTable + ` WHERE holder = $3 AND expires_at > $5
)
INSERT INTO ` + replayTable + ` (jkt, jti, holder, expires_at)
SELECT $1, $2, $3, $4 FROM live WHERE live.n < $6
ON CONFLICT (jkt, jti) DO UPDATE SET holder = EXCLUDED.holder, expires_at = EXCLUDED.expires_at
WHERE ` + replayTable + `.expires_at <= $5`

	// sqlProofLive is whether $1/$2 is live at $3: a replay, rather than a
	// holder past its share.
	sqlProofLive = `SELECT EXISTS (SELECT 1 FROM ` + replayTable + ` WHERE jkt = $1 AND jti = $2 AND expires_at > $3)`

	// sqlPurgeProofs deletes up to $2 ids expired at $1.
	sqlPurgeProofs = `
DELETE FROM ` + replayTable + ` p
USING (SELECT jkt, jti FROM ` + replayTable + ` WHERE expires_at <= $1 ORDER BY expires_at LIMIT $2) d
WHERE p.jkt = d.jkt AND p.jti = d.jti AND p.expires_at <= $1`
)

// The attributes of the cache's log lines.
const (
	logKeyHolder = "holder"
	logKeyShare  = "share"
)

// Seen implements dpop.ReplayCache. A holder past its share is refused as a
// replay rather than accepted unrecorded, and logged at WARN. A database
// that cannot answer returns (true, err): dpop.Verifier.Verify refuses the
// proof with the error, an outage rather than a replay.
func (c *ReplayCache) Seen(ctx context.Context, e dpop.ReplayEntry) (bool, error) {
	now := c.now()
	// Lapsed already: nothing could replay it, so nothing is kept.
	if !e.ExpiresAt.After(now) {
		return false, nil
	}
	var recorded, live bool
	err := pgx.BeginTxFunc(ctx, c.db, txOptions, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, sqlTakeTenantLock, replayLockClass, e.Holder); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, sqlRecordProof, e.JKT, e.JTI, e.Holder, e.ExpiresAt, now, c.share)
		if err != nil {
			return err
		}
		if recorded = tag.RowsAffected() == 1; recorded {
			return nil
		}
		return tx.QueryRow(ctx, sqlProofLive, e.JKT, e.JTI, now).Scan(&live)
	})
	if err != nil {
		return true, fmt.Errorf("pgstore: record proof id: %w", err)
	}
	if !recorded && !live {
		c.log.WarnContext(ctx, "pgstore: replay cache refused a proof: its holder is past its share",
			slog.String(logKeyHolder, e.Holder), slog.Int(logKeyShare, c.share))
	}
	return !recorded, nil
}

// PurgeExpired deletes up to DefaultReplayPurgeBatch ids that have expired
// by Now, and returns how many it deleted. Call it until it returns 0.
func (c *ReplayCache) PurgeExpired(ctx context.Context) (int64, error) {
	var purged int64
	err := pgx.BeginTxFunc(ctx, c.db, txOptions, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, sqlPurgeProofs, c.now(), c.purgeBatch)
		purged = tag.RowsAffected()
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("pgstore: purge expired proof ids: %w", err)
	}
	return purged, nil
}
