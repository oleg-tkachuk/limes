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
// Every call runs in one transaction on one connection — but for the two
// revocation checks, each a single statement (see Tenants) — and every time it
// writes or compares against comes from Config.Now, never from the database's
// own clock: the store's clock is the one limes measures expiry and purging
// by, and a database clock beside it would be a second one.
//
// # Tenants
//
// Every table keeps its rows' tenant, and the schema's row-level security
// binds a role that does not own the tables to the tenant a call is scoped
// to: pass WithTenant(ctx, tenant) to a call for one tenant,
// WithAllTenants(ctx) to housekeeping (Purge, ReleaseExpired, PurgeOrphans),
// and WithAllTenantsReadOnly(ctx) to a read across tenants, such as
// ListTenantBudgets: its policies admit reads only, and a call that writes
// under it is refused with limes.ErrScopeMismatch. A call scoped to none sees
// no row and writes none. IsRevoked and IsBiscuitRevoked see every tenant whatever the scope:
// the verifier asks them before it knows the token's tenant, on every
// request, so each is one statement calling a function of the schema that
// runs as the tables' owner — one round trip, no transaction. The owner of
// the tables, which runs Migrate, is not bound; connect as another role for
// the policies to hold. Config.CheckTenant lets a consumer that keeps its own
// tenants refuse one it does not hold.
//
// A call whose request names its tenant — Insert, ListByPrincipal, Bump,
// Charge, Reserve, Settle, GetTenantBudget, SetTenantBudget — under a scope
// that does not admit that tenant returns limes.ErrScopeMismatch rather than
// answer as if the tenant's rows were not there (Config.CheckScope). A call
// that names only an id cannot be checked so, and answers a row out of scope
// as not found.
//
// To remove a tenant, Store.EraseTenant revokes its capabilities, removes
// the records Purge would and every meter row of the tenant — the charges
// ledger included — in one transaction, and runs the consumer's own removal
// in it. The records it keeps stay revoked until Purge takes them: an id not
// on record is not revoked, so a token whose record went early would verify
// again.
package pgstore

import (
	"context"
	"errors"
	"fmt"
	"reflect"
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

// DB is what the store needs of a connection pool: a transaction per call,
// and a single statement for a revocation check. *pgxpool.Pool and *pgx.Conn
// implement it.
type DB interface {
	Begin(ctx context.Context) (pgx.Tx, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Config configures a store.
type Config struct {
	// DB is the database, its schema created by Migrate. Required: nil, or a
	// nil pool or connection, is refused at construction.
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
	// CheckScope runs after Scope in the transaction of every call whose
	// request names its tenant — Insert, ListByPrincipal, Bump, Charge,
	// Reserve, Settle, GetTenantBudget, SetTenantBudget — and refuses one
	// whose scope does not admit that tenant with limes.ErrScopeMismatch,
	// which the call returns as it is. Nil, under the default Scope: the
	// call's WithTenant or WithAllTenants decides, and with neither
	// CheckSchemaScope does. Nil beside a Scope of the consumer's: no check,
	// since the store cannot tell what its policies admit; a Scope that sets
	// the schema's own settings passes CheckSchemaScope here.
	CheckScope func(ctx context.Context, tx pgx.Tx, tenant uuid.UUID) error
}

// errNoDB is NewStore given a Config without a DB: none at all, or a nil
// pool or connection, which is a DB that fails on its first call.
var errNoDB = errors.New("pgstore: Config.DB required")

// isNil reports whether db is nil or holds a nil pointer, such as a
// *pgxpool.Pool left unset: a non-nil DB that cannot serve a call.
func isNil(db DB) bool {
	if db == nil {
		return true
	}
	v := reflect.ValueOf(db)
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan:
		return v.IsNil()
	default:
		return false
	}
}

// conn is what every store of the package shares: the database, the clock
// and the scope.
type conn struct {
	db          DB
	now         func() time.Time
	scope       func(ctx context.Context, tx pgx.Tx) error
	checkTenant func(ctx context.Context, tx pgx.Tx, tenant uuid.UUID) error
	checkScope  func(ctx context.Context, tx pgx.Tx, tenant uuid.UUID) error
}

func newConn(cfg Config) (conn, error) {
	if isNil(cfg.DB) {
		return conn{}, errNoDB
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.CheckScope == nil && cfg.Scope == nil {
		cfg.CheckScope = checkContextScope
	}
	if cfg.Scope == nil {
		cfg.Scope = ScopeFromContext
	}
	return conn{db: cfg.DB, now: cfg.Now, scope: cfg.Scope, checkTenant: cfg.CheckTenant, checkScope: cfg.CheckScope}, nil
}

// clock is the store's time now, to the column's resolution and in UTC.
func (c conn) clock() time.Time {
	return c.now().UTC().Truncate(timeResolution)
}

// inTx runs fn, a call that writes, in one transaction, scoped first, and
// commits it when fn returns nil. A read-only scope is refused before the
// transaction begins: its policies would let the writes through as no rows.
func (c conn) inTx(ctx context.Context, fn func(pgx.Tx) error) error {
	if readOnly(ctx) {
		return errReadOnlyScope
	}
	return c.inReadTx(ctx, fn)
}

// inReadTx runs fn, a call that only reads, in one transaction, scoped
// first.
func (c conn) inReadTx(ctx context.Context, fn func(pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, c.db, func(tx pgx.Tx) error {
		if err := c.scope(ctx, tx); err != nil {
			return fmt.Errorf("pgstore: scope: %w", err)
		}
		return fn(tx)
	})
}

// inTenantTx is inTx for a call whose request names tenant: it refuses a
// scope that does not admit tenant before fn reads a row, so a call under
// the wrong scope fails as one rather than as rows that are not there.
func (c conn) inTenantTx(ctx context.Context, tenant uuid.UUID, fn func(pgx.Tx) error) error {
	return c.inTx(ctx, c.admitting(ctx, tenant, fn))
}

// inTenantReadTx is inReadTx for a read whose request names tenant, checked
// as inTenantTx checks.
func (c conn) inTenantReadTx(ctx context.Context, tenant uuid.UUID, fn func(pgx.Tx) error) error {
	return c.inReadTx(ctx, c.admitting(ctx, tenant, fn))
}

// admitting is fn, run once the call's scope admits tenant.
func (c conn) admitting(ctx context.Context, tenant uuid.UUID, fn func(pgx.Tx) error) func(pgx.Tx) error {
	return func(tx pgx.Tx) error {
		if c.checkScope != nil {
			if err := c.checkScope(ctx, tx, tenant); err != nil {
				return err
			}
		}
		return fn(tx)
	}
}
