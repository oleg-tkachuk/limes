// Package pgstore is the reference implementation of the limes storage
// contracts over PostgreSQL: limes.Store and limes.BiscuitRevocationStore for
// capability records and revocations, limes.UsageStore[pgx.Tx] and
// limes.CopyUsageReader for the meter. It passes the module's own contract
// suites, storetest and metertest, which is how a consumer knows it keeps
// every rule a store must keep.
//
// Create the schema once, then build both stores over one pool:
//
//	if err := pgstore.Migrate(ctx, pool); err != nil { … }
//	records, err := pgstore.NewStore(pgstore.Config{DB: pool})
//	usage, err := pgstore.NewUsageStore(pgstore.Config{DB: pool})
//
// Every call runs in one transaction on one connection, and every time it
// writes or compares against comes from Config.Now, never from the database's
// own clock: the store's clock is the one limes measures expiry and purging
// by, and a database clock beside it would be a second one.
package pgstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// schema is the PostgreSQL schema every table of the store lives in. The
// migrations and every query spell it so; a mismatch fails each of them.
const schema = "limes"

// timeResolution is the finest a timestamptz column keeps. The store reads
// its clock to it, so a time it writes reads back equal.
const timeResolution = time.Microsecond

// DB is what the store needs of a connection pool: a transaction per call.
// *pgxpool.Pool and *pgx.Conn implement it.
type DB interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Config configures a store.
type Config struct {
	// DB is the database, its schema created by Migrate. Required.
	DB DB
	// Now is the store's clock. Nil: time.Now.
	Now func() time.Time
	// Scope runs first in every transaction the store opens, with the call's
	// context, and sets what row-level security policies read: nil is
	// ScopeFromContext, which serves the schema's own policies from
	// WithTenant and WithAllTenants. A consumer with policies of its own sets
	// its settings here (set_config(…, true), local to the transaction). An
	// error from it ends the call.
	Scope func(ctx context.Context, tx pgx.Tx) error
	// CheckTenant, when set, runs in Insert's transaction before the record
	// is written, for a consumer that keeps its own tenants: it returns
	// limes.ErrUnknownTenant for a tenant it does not hold and
	// limes.ErrTenantDeleted for one in the trash, which Insert returns
	// as they are. Nil: every tenant is one the store can mint for.
	CheckTenant func(ctx context.Context, tx pgx.Tx, tenant uuid.UUID) error
}

// errNoDB is NewStore given a Config without a DB.
var errNoDB = errors.New("pgstore: Config.DB required")

// conn is what every store of the package shares: the database, the clock
// and the scope.
type conn struct {
	db          DB
	now         func() time.Time
	scope       func(ctx context.Context, tx pgx.Tx) error
	checkTenant func(ctx context.Context, tx pgx.Tx, tenant uuid.UUID) error
}

func newConn(cfg Config) (conn, error) {
	if cfg.DB == nil {
		return conn{}, errNoDB
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Scope == nil {
		cfg.Scope = ScopeFromContext
	}
	return conn{db: cfg.DB, now: cfg.Now, scope: cfg.Scope, checkTenant: cfg.CheckTenant}, nil
}

// clock is the store's time now, to the column's resolution and in UTC.
func (c conn) clock() time.Time {
	return c.now().UTC().Truncate(timeResolution)
}

// inTx runs fn in one transaction, scoped first, and commits it when fn
// returns nil.
func (c conn) inTx(ctx context.Context, fn func(pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, c.db, func(tx pgx.Tx) error {
		if err := c.scope(ctx, tx); err != nil {
			return fmt.Errorf("pgstore: scope: %w", err)
		}
		return fn(tx)
	})
}

// inTxAllTenants is inTx scoped to every tenant as well, for a revocation
// check: the verifier asks before it knows the token's tenant, and a check
// that missed an entry because of the caller's scope would let a revoked
// token through.
func (c conn) inTxAllTenants(ctx context.Context, fn func(pgx.Tx) error) error {
	return c.inTx(ctx, func(tx pgx.Tx) error {
		if err := allTenants(ctx, tx); err != nil {
			return err
		}
		return fn(tx)
	})
}
