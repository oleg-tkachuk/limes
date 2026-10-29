// Package migrate creates and upgrades the schema pgstore's stores query.
// Only a migration job imports it: it carries the migrations and runs them
// with github.com/jackc/tern, which a binary that only serves never links.
//
// Run migrates a database itself. A consumer with a migration tool of its
// own applies Steps and Grants with that tool instead, from pgstore/migrate/ddl,
// which holds them without tern.
package migrate

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/jackc/pgx/v5"
	tern "github.com/jackc/tern/v2/migrate"

	"github.com/oleg-tkachuk/limes/pgstore/internal/pgschema"
	"github.com/oleg-tkachuk/limes/pgstore/migrate/ddl"

	"github.com/oleg-tkachuk/limes"
)

// migrationFiles are the migrations ddl carries, under migrationsDir.
var migrationFiles = ddl.Files()

// migrationsDir is where migrationFiles keeps the migrations.
const migrationsDir = ddl.MigrationsDir

// The schema and its version table, as pgstore reads them.
const (
	schema           = pgschema.Name
	versionTableName = pgschema.VersionTableName
	versionTable     = pgschema.VersionTable
)

// Options configures Run.
type Options struct {
	// RuntimeRoles are the roles the stores serve as: roles that do not own
	// the tables, so row-level security binds them. Each gets USAGE on the
	// schema, SELECT, INSERT, UPDATE and DELETE on its tables, SELECT alone
	// on the version table (for CheckSchema), EXECUTE on its functions —
	// which no other role has — and the same on every table a later
	// migration creates. Granting is idempotent, so each Run grants again.
	// Empty: Run grants nothing, and the roles are the consumer's to grant
	// (Grants).
	RuntimeRoles []string
}

// Run brings the store's schema up to the one this version of the package
// queries, running each migration not yet applied in order, then grants the
// runtime roles opts names. Run it before the stores serve, as the role that
// owns the schema, and before any migration of the consumer's that refers to
// its tables.
//
// It holds a session advisory lock while it migrates, so replicas starting
// together migrate once. The lock is the one every migration run by
// github.com/jackc/tern takes; it is reentrant within one session, so a
// consumer migrating with tern runs Run on the connection its own run
// holds, never on a second one, which would wait on that run for ever. A
// consumer with another migration tool runs Run before it, or applies Steps
// with that tool instead.
//
// Migration 014, which pgstore v0.31.0 brought, now builds its three indexes
// CONCURRENTLY, outside a transaction, holding no writes, as every later
// migration that indexes an existing table does, so a Migrate at startup
// holds none. Its first form, in v0.31.0 and the releases up to the one that
// changed it, built them with a plain CREATE INDEX, which held writes to
// limes.capabilities and limes.revocations for some seconds on a million
// records; a store that ran that one has the indexes, and tern does not run
// 014 there again.
//
// Migration 015 adds limes.capabilities.revoked,
// the flag ListRecords and CountRecords read: a change of the catalog, which
// rewrites no row and holds the table only for that moment. 016 then marks
// the records revoked before it, taking only their rows, and 017 builds its
// indexes CONCURRENTLY.
//
// Migrate before the pgstore that needs a migration serves: a pgstore reads
// only the schema of its own version (pgstore.CheckSchema says whether it is
// there).
// 020, for one, gives limes.revocation_lineage a column the pgstore of that
// version reads; an older pgstore reads the function by its columns' names
// and goes on working beside it, so migrating first is the safe order of a
// rolling upgrade.
//
// Run needs one connection, not a pool; over a pool:
//
//	err := pool.AcquireFunc(ctx, func(c *pgxpool.Conn) error {
//		return migrate.Run(ctx, c.Conn(), migrate.Options{RuntimeRoles: []string{"app"}})
//	})
func Run(ctx context.Context, conn *pgx.Conn, opts Options) error {
	if conn == nil {
		return fmt.Errorf("%w: migrate: a connection is required", limes.ErrInvalidRequest)
	}
	if err := ddl.CheckRuntimeRoles(opts.RuntimeRoles); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	if err := ownSchema(ctx, conn); err != nil {
		return err
	}
	m, err := loadMigrations(ctx, conn)
	if err != nil {
		return err
	}
	if err := runMigrations(ctx, conn, m); err != nil {
		return err
	}
	return grant(ctx, conn, opts.RuntimeRoles)
}

// ownSchema creates the store's schema, which the version table lives in,
// so it comes first; and refuses one of its name made earlier by another
// role, which is that role's to change: it could drop or replace the tables
// and policies Migrate puts in it.
func ownSchema(ctx context.Context, conn *pgx.Conn) error {
	if _, err := conn.Exec(ctx, "CREATE SCHEMA IF NOT EXISTS "+pgx.Identifier{schema}.Sanitize()); err != nil {
		return fmt.Errorf("migrate: create schema: %w", err)
	}
	var owned bool
	if err := conn.QueryRow(ctx, sqlSchemaOwned, schema).Scan(&owned); err != nil {
		return fmt.Errorf("migrate: schema owner: %w", err)
	}
	if !owned {
		return fmt.Errorf("%w: %s is owned by another role than the one migrating", ErrSchemaOwner, schema)
	}
	return nil
}

// loadMigrations is a migrator holding the embedded migrations, as tern
// renders them. With no conn it only holds them, as Steps reads them.
func loadMigrations(ctx context.Context, conn *pgx.Conn) (*tern.Migrator, error) {
	m, err := tern.NewMigrator(ctx, conn, versionTable)
	if err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	dir, err := fs.Sub(migrationFiles, migrationsDir)
	if err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	if err := m.LoadMigrations(dir); err != nil {
		return nil, fmt.Errorf("migrate: load: %w", err)
	}
	return m, nil
}

// sqlSetSession sets a setting for the rest of the session, and
// sqlSettingNow reads one, empty when unset.
const (
	sqlSetSession = `SELECT set_config($1, $2, false)`
	sqlSettingNow = `SELECT coalesce(current_setting($1, true), '')`
)

// scopeMigration scopes conn's session to every tenant as a migration
// starts. A variable so that a test can make it fail.
var scopeMigration = func(ctx context.Context, conn *pgx.Conn) error {
	_, err := conn.Exec(ctx, sqlSetSession, pgschema.SettingAllTenants, pgschema.SettingAllTenantsOn)
	return err
}

// sqlReadCommitted makes the transaction a migration runs in read committed,
// whatever the role's, the database's or the session's default: 016 marks a
// level of records a statement at a time and reads the next level's by the
// next statement's snapshot, which under repeatable read would miss a child
// inserted meanwhile. It must be the transaction's first statement.
const sqlReadCommitted = `SET TRANSACTION ISOLATION LEVEL READ COMMITTED`

// readCommitted runs sqlReadCommitted in the transaction tern has just begun
// for a migration; one that runs outside a transaction (CONCURRENTLY) has
// none to set.
func readCommitted(ctx context.Context, conn *pgx.Conn) error {
	if conn.PgConn().TxStatus() != pgTxInBlock {
		return nil
	}
	_, err := conn.Exec(ctx, sqlReadCommitted)
	return err
}

// pgTxInBlock is the transaction status PostgreSQL reports inside a
// transaction block ('T').
const pgTxInBlock = 'T'

// runMigrations runs m's migrations on conn scoped to every tenant, each of
// them, and read committed: tern resets the session's settings after every
// migration, so the scope is set again as each one starts. tern runs a migration whatever its
// OnStart finds, so a scope that cannot be set closes the connection: the
// migration fails before it runs unscoped, its version is not recorded, and
// the run ends with the error.
func runMigrations(ctx context.Context, conn *pgx.Conn, m *tern.Migrator) error {
	var scopeErr error
	m.OnStart = func(int32, string, string, string) {
		if scopeErr != nil {
			return
		}
		if err := readCommitted(ctx, conn); err != nil {
			scopeErr = fmt.Errorf("migrate: run a migration read committed, connection closed: %w", err)
			_ = conn.Close(context.WithoutCancel(ctx))
			return
		}
		if err := scopeMigration(ctx, conn); err != nil {
			scopeErr = fmt.Errorf("migrate: scope a migration to every tenant, connection closed: %w", err)
			_ = conn.Close(context.WithoutCancel(ctx))
		}
	}
	err := migrateAllTenants(ctx, conn, m.Migrate)
	if scopeErr != nil {
		return errors.Join(scopeErr, err)
	}
	return err
}

// restoreScopeTimeout bounds putting the session's scope back after a run,
// which goes ahead when the caller's context is done: the connection is the
// caller's, and a pool would hand it on still scoped to every tenant.
const restoreScopeTimeout = 5 * time.Second

// migrateAllTenants runs the migrations (run) scoped to every tenant, to read
// and write: row-level security binds the owner (010_force_row_security.sql),
// so a migration that moves data would otherwise see no row and change none
// without a word. The session's own value comes back after, so a consumer's
// migration run on the same connection keeps what it set — even when ctx
// ends during the run. When it cannot come back, the connection is closed
// rather than left scoped to every tenant.
func migrateAllTenants(ctx context.Context, conn *pgx.Conn, run func(context.Context) error) (err error) {
	var before string
	if err := conn.QueryRow(ctx, sqlSettingNow, pgschema.SettingAllTenants).Scan(&before); err != nil {
		return fmt.Errorf("migrate: read the scope: %w", err)
	}
	if _, err := conn.Exec(ctx, sqlSetSession, pgschema.SettingAllTenants, pgschema.SettingAllTenantsOn); err != nil {
		return fmt.Errorf("migrate: scope to every tenant: %w", err)
	}
	defer func() {
		restoreCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), restoreScopeTimeout)
		defer cancel()
		if _, rerr := conn.Exec(restoreCtx, sqlSetSession, pgschema.SettingAllTenants, before); rerr != nil {
			_ = conn.Close(restoreCtx)
			err = errors.Join(err, fmt.Errorf("migrate: restore the scope, connection closed: %w", rerr))
		}
	}()
	if err := run(ctx); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

// ErrSchemaOwner is Run finding the store's schema owned by another role
// than the one it runs as.
var ErrSchemaOwner = fmt.Errorf("%w: migrate: the schema belongs to another role", limes.ErrInvalidConfig)

// sqlSchemaOwned is whether the role migrating owns the schema named $1.
const sqlSchemaOwned = `SELECT pg_get_userbyid(nspowner) = current_user FROM pg_namespace WHERE nspname = $1`

// Grants are the statements that give each runtime role what the stores
// need of the schema (Options.RuntimeRoles), for a consumer that applies
// Steps with a tool of its own: ddl.Grants.
func Grants(roles []string) ([]string, error) {
	stmts, err := ddl.Grants(roles)
	if err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return stmts, nil
}

// grant runs Grants in one transaction, so a role is granted everything or
// nothing.
func grant(ctx context.Context, conn *pgx.Conn, roles []string) error {
	stmts, err := Grants(roles)
	if err != nil || len(stmts) == 0 {
		return err
	}
	err = pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		for _, stmt := range stmts {
			if _, err := tx.Exec(ctx, stmt); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("migrate: grant: %w", err)
	}
	return nil
}

// Step is one migration as a consumer's own migration tool applies it, as
// ddl.Step is, which a program that vendors the SQL imports without tern.
type Step struct {
	// Version is the schema's version once the step has run: tern's
	// numbering, from one without a gap.
	Version int
	// Name is the migration's file name, such as "001_records.sql".
	Name string
	// Migration is the migration's SQL as tern renders and Run runs it.
	// Apply SQL or Statements rather than this.
	Migration string
	// InTransaction says how to run the step: SQL as one transaction of its
	// own; or, when false, Statements a statement at a time outside any
	// transaction, as CREATE INDEX CONCURRENTLY needs.
	InTransaction bool
}

// SQL is ddl.Step.SQL: what a tool applies for a transactional step.
func (s Step) SQL() string { return ddl.Step(s).SQL() }

// Statements is ddl.Step.Statements: the step as a tool sends it.
func (s Step) Statements() []string { return ddl.Step(s).Statements() }

// Setup is the SQL a consumer's tool runs once, before the first Step:
// ddl.Setup.
func Setup() string { return ddl.Setup() }

// Steps are the migrations Run runs, in order, for a consumer's own
// migration tool to apply: ddl.Steps, the same migrations tern loads.
func Steps() ([]Step, error) {
	steps, err := ddl.Steps()
	if err != nil {
		return nil, err
	}
	out := make([]Step, len(steps))
	for i, s := range steps {
		out[i] = Step(s)
	}
	return out, nil
}
