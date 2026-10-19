// Package pgstore is the reference implementation of the limes storage
// contracts over PostgreSQL: limes.Store and limes.BiscuitRevocationStore for
// capability records and revocations, meter.UsageStore and
// meter.CopyUsageReader for the meter, meter.LedgerReader and
// limes.RecordCensus for reports, and
// limes.TenantRecordEraser and meter.TenantUsageEraser for removing a
// tenant. It passes the module's own contract
// suites, storetest and metertest, which is how a consumer knows it keeps
// every rule a store must keep.
//
// Create the schema once, as the role that owns it, granting the role the
// stores serve as; then build both stores over a pool of that role:
//
//	err := ownerPool.AcquireFunc(ctx, func(c *pgxpool.Conn) error {
//		return pgstore.Migrate(ctx, c.Conn(), pgstore.MigrateOptions{RuntimeRoles: []string{"app"}})
//	})
//	records, err := pgstore.NewStore(pgstore.Config{DB: pool})
//	usage, err := pgstore.NewUsageStore(pgstore.Config{DB: pool})
//
// CheckSchema returns ErrSchemaMismatch while the schema is not the version
// this package queries, and ErrRuntimeRole when the pool is a role the
// policies do not hold — a superuser, one with BYPASSRLS, an owner of the
// schema or of anything in it, or a role that can act as one — for a
// readiness check; ReadSchemaVersion reports both versions and
// needs only the SELECT that Migrate grants a runtime role.
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
// binds every role — the tables' owner too — to the tenant a call is scoped
// to, short of a superuser or a role with BYPASSRLS: pass WithTenant(ctx, tenant) to a call for one tenant,
// WithAllTenants(ctx) to housekeeping (Purge, ReleaseExpired, PurgeOrphans),
// and WithAllTenantsReadOnly(ctx) to a read across tenants, such as
// ListTenantBudgets: its policies admit reads only, and a call that writes
// under it is refused with limes.ErrScopeMismatch, as one is under a Scope of
// the consumer's that sets the same read-only setting. A call scoped to none sees
// no row and writes none. IsRevoked and IsBiscuitRevoked see every tenant whatever the scope:
// the verifier asks them before it knows the token's tenant, on every
// request, so each is one statement calling a function of the schema that
// runs as the tables' owner and scopes itself to every tenant, read only,
// for its own query — one round trip, no transaction. Migrate runs as the
// owner, scoped to every tenant for its run; serve as a role that owns
// nothing, which CheckSchema checks, so that no call can turn the policies
// off. Config.CheckTenant lets a consumer that keeps its own
// tenants refuse one it does not hold.
//
// A call whose request names its tenant — Insert, ListByPrincipal, Bump,
// Charge, Reserve, Settle, Release, GetTenantBudget, SetTenantBudget, the erasers and
// Ledger.SumCharges — under a scope
// that does not admit that tenant returns limes.ErrScopeMismatch rather than
// answer as if the tenant's rows were not there (Config.CheckScope). A call
// that names only an id cannot be checked so, and answers a row out of scope
// as not found.
//
// The schema notifies RevocationChannel on every statement that writes a
// revocation; a RevocationWatcher beside each verifier clears its revocation
// caches then, so a revocation reaches every replica at once rather than
// within the caches' TTL.
//
// A Ledger sums a tenant's charges for billing and reports
// (meter.LedgerReader), so a consumer reads the ledger through it rather
// than through the charges table, whose columns are this package's own. A
// Census counts the capability records (limes.RecordCensus) for the same
// reason: of one tenant, scoped to it as every call naming a tenant, or of
// every tenant under WithAllTenantsReadOnly or WithAllTenants — a scope that
// admits fewer is refused with limes.ErrScopeMismatch, not counted as all.
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
	"github.com/oleg-tkachuk/limes/meter"
)

// schema is the PostgreSQL schema every table of the store lives in. The
// migrations and every query spell it so; a mismatch fails each of them.
const schema = "limes"

// timeResolution is the finest a timestamptz column keeps. The store reads
// its clock to it, so a time it writes reads back equal.
const timeResolution = time.Microsecond

// DB is what the store needs of a connection pool: a transaction per call,
// and a single statement for a revocation check. *pgxpool.Pool and *pgx.Conn
// implement it. A DB of your own opens each transaction with the options it
// is given: one that drops them leaves the store at the database's default
// isolation, which txOptions exists to override.
type DB interface {
	BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// txOptions are those of every transaction the store opens. READ COMMITTED
// whatever the database's default: a call that waits on a lock — a tenant's
// erase lock, a counter row — reads what committed while it waited, such as
// the erase it waited for. Under REPEATABLE READ its snapshot would predate
// the wait.
var txOptions = pgx.TxOptions{IsoLevel: pgx.ReadCommitted}

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
	// WithTenant, WithAllTenants and WithAllTenantsReadOnly. A consumer with policies of its own sets
	// its settings here (set_config(…, true), local to the transaction). An
	// error from it ends the call.
	//
	// A Scope of the consumer's that scopes a call to reading every tenant
	// sets limes.all_tenants to 'read', as WithAllTenantsReadOnly does: a
	// call that writes then reads the setting back once Scope has run, and
	// refuses with limes.ErrScopeMismatch before it writes anything —
	// whatever limes.tenant_id says beside it, and one round trip more per
	// call that writes. A Scope that never sets limes.all_tenants to 'read'
	// gets no such check; its policies alone decide what a write reaches.
	Scope func(ctx context.Context, tx pgx.Tx) error
	// CheckTenant, when set, runs in Insert's transaction before the record
	// is written, for a consumer that keeps its own tenants: it returns
	// limes.ErrUnknownTenant for a tenant it does not hold and
	// limes.ErrTenantDeleted for one in the trash, which Insert returns
	// as they are. Nil: every tenant is one the store can mint for.
	CheckTenant func(ctx context.Context, tx pgx.Tx, tenant uuid.UUID) error
	// CheckScope runs after Scope in the transaction of every call whose
	// request names its tenant — Insert, ListByPrincipal, Bump, Charge,
	// Reserve, Settle, Release, GetTenantBudget, SetTenantBudget, EraseTenant,
	// EraseTenantRecords, EraseTenantUsage, Ledger.SumCharges and
	// Census.CountRecords for one tenant — and
	// refuses one
	// whose scope does not admit that tenant with limes.ErrScopeMismatch,
	// which the call returns as it is. Nil, under the default Scope: the
	// call's WithTenant or WithAllTenants decides, and with neither
	// CheckSchemaScope does. Nil beside a Scope of the consumer's: no check,
	// since the store cannot tell what its policies admit; a Scope that sets
	// the schema's own settings passes CheckSchemaScope here.
	CheckScope func(ctx context.Context, tx pgx.Tx, tenant uuid.UUID) error
	// MaxCopiesPerCapability is how many Biscuit copies the usage store
	// counts for one capability (meter.Meter, "Copies of a Biscuit"). Zero:
	// meter.DefaultMaxCopiesPerCapability; negative is refused. The record
	// store ignores it.
	MaxCopiesPerCapability int
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
	// checkReadOnly refuses, in a transaction that writes, a scope that
	// admits reads only: nil under the default Scope, whose context
	// decides before the transaction begins.
	checkReadOnly func(ctx context.Context, tx pgx.Tx) error
	// checkEvery refuses, in a call across every tenant, a scope that
	// admits fewer.
	checkEvery func(ctx context.Context, tx pgx.Tx) error
	// maxCopies is Config.MaxCopiesPerCapability, its default applied.
	maxCopies int64
}

func newConn(cfg Config) (conn, error) {
	if isNil(cfg.DB) {
		return conn{}, errNoDB
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	switch {
	case cfg.MaxCopiesPerCapability < 0:
		return conn{}, fmt.Errorf("pgstore: Config.MaxCopiesPerCapability %d is negative", cfg.MaxCopiesPerCapability)
	case cfg.MaxCopiesPerCapability == 0:
		cfg.MaxCopiesPerCapability = meter.DefaultMaxCopiesPerCapability
	}
	checkReadOnly, checkEvery := checkSchemaReadOnly, checkSchemaAdmitsEvery
	if cfg.CheckScope == nil && cfg.Scope == nil {
		cfg.CheckScope = checkContextScope
	}
	if cfg.Scope == nil {
		cfg.Scope = ScopeFromContext
		checkReadOnly, checkEvery = nil, checkContextAdmitsEvery
	}
	return conn{
		db: cfg.DB, now: cfg.Now, scope: cfg.Scope,
		checkTenant: cfg.CheckTenant, checkScope: cfg.CheckScope,
		checkReadOnly: checkReadOnly, checkEvery: checkEvery,
		maxCopies: int64(cfg.MaxCopiesPerCapability),
	}, nil
}

// clock is the store's time now, to the column's resolution and in UTC.
func (c conn) clock() time.Time {
	return c.now().UTC().Truncate(timeResolution)
}

// inTx runs fn, a call that writes, in one transaction, scoped first, and
// commits it when fn returns nil. A read-only scope is refused before fn
// writes — WithAllTenantsReadOnly before the transaction begins, the
// read-only setting of a Scope of the consumer's once it has run — since its
// policies would let the writes through as no rows.
func (c conn) inTx(ctx context.Context, fn func(pgx.Tx) error) error {
	if readOnly(ctx) {
		return errReadOnlyScope
	}
	if c.checkReadOnly == nil {
		return c.inReadTx(ctx, fn)
	}
	return c.inReadTx(ctx, func(tx pgx.Tx) error {
		if err := c.checkReadOnly(ctx, tx); err != nil {
			return err
		}
		return fn(tx)
	})
}

// inReadTx runs fn, a call that only reads, in one transaction, scoped
// first.
func (c conn) inReadTx(ctx context.Context, fn func(pgx.Tx) error) error {
	return pgx.BeginTxFunc(ctx, c.db, txOptions, func(tx pgx.Tx) error {
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

// inEveryTenantReadTx is inReadTx for a read across every tenant: it
// refuses a scope that admits fewer before fn reads a row, so one tenant's
// rows are never read as every tenant's.
func (c conn) inEveryTenantReadTx(ctx context.Context, fn func(pgx.Tx) error) error {
	return c.inReadTx(ctx, func(tx pgx.Tx) error {
		if err := c.checkEvery(ctx, tx); err != nil {
			return err
		}
		return fn(tx)
	})
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
