// Package migrate creates and upgrades the schema pgstore's stores query.
// Only a migration job imports it: it carries the migrations and runs them
// with github.com/jackc/tern, which a binary that only serves never links.
//
// Run migrates a database itself. A consumer with a migration tool of its
// own applies Steps and Grants with that tool instead.
package migrate

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	tern "github.com/jackc/tern/v2/migrate"

	"github.com/oleg-tkachuk/limes/pgstore/internal/pgschema"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// migrationsDir is where migrationFiles keeps the migrations.
const migrationsDir = "migrations"

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
		return errors.New("migrate: a connection is required")
	}
	if err := checkRuntimeRoles(opts.RuntimeRoles); err != nil {
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
var ErrSchemaOwner = errors.New("migrate: the schema belongs to another role")

// sqlSchemaOwned is whether the role migrating owns the schema named $1.
const sqlSchemaOwned = `SELECT pg_get_userbyid(nspowner) = current_user FROM pg_namespace WHERE nspname = $1`

// publicRole is the name PostgreSQL reads as every role there is, quoted or
// not, in a GRANT.
const publicRole = "public"

// checkRuntimeRoles refuses a runtime role that is not one role: an empty
// name, "public" in any case, which would grant every role, and a name with
// a NUL, which the identifier quoting drops, granting another role than the
// one named.
func checkRuntimeRoles(roles []string) error {
	for _, r := range roles {
		switch {
		case r == "":
			return errors.New("an empty runtime role name")
		case strings.EqualFold(r, publicRole):
			return fmt.Errorf("runtime role %q names every role", r)
		case strings.ContainsRune(r, 0):
			return fmt.Errorf("runtime role %q holds a NUL", r)
		}
	}
	return nil
}

// Grants are the statements that give each runtime role what the stores
// need of the schema (Options.RuntimeRoles), for a consumer that applies
// Steps with a tool of its own: run them in one transaction after the last
// step, and again after each later one, as Run does. It refuses a name that
// is not one role: an empty one, "public", or one holding a NUL.
func Grants(roles []string) ([]string, error) {
	if err := checkRuntimeRoles(roles); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	schemaName := pgx.Identifier{schema}.Sanitize()
	versions := pgx.Identifier{schema, versionTableName}.Sanitize()
	var stmts []string
	for _, r := range roles {
		role := pgx.Identifier{r}.Sanitize()
		stmts = append(stmts,
			"GRANT USAGE ON SCHEMA "+schemaName+" TO "+role,
			"GRANT "+tablePrivileges+" ON ALL TABLES IN SCHEMA "+schemaName+" TO "+role,
			// The version table is the migrator's; a runtime role only
			// reads it.
			"REVOKE "+tablePrivileges+" ON "+versions+" FROM "+role,
			"GRANT SELECT ON "+versions+" TO "+role,
			// The schema's functions: the policies' scope checks and the
			// revocation checks, which no other role may call.
			"GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA "+schemaName+" TO "+role,
			"ALTER DEFAULT PRIVILEGES IN SCHEMA "+schemaName+" GRANT "+tablePrivileges+" ON TABLES TO "+role,
		)
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

// tablePrivileges are what a runtime role may do to a table of the store.
const tablePrivileges = "SELECT, INSERT, UPDATE, DELETE"

// Step is one migration as a consumer's own migration tool applies it.
type Step struct {
	// Version is the schema's version once the step has run: tern's
	// numbering, from one without a gap.
	Version int
	// Name is the migration's file name, such as "001_records.sql".
	Name string
	// Migration is the migration's SQL as tern renders and Run runs it.
	// Apply SQL rather than this.
	Migration string
	// InTransaction says how to run SQL: as one transaction, SQL its first
	// statements; or, when false, a statement at a time outside any
	// transaction, as CREATE INDEX CONCURRENTLY needs.
	InTransaction bool
}

// SQL is what a tool applies for the step: Migration, preceded, in a
// transaction, by what Run runs as each migration starts — read committed,
// scoped to every tenant for that transaction, without which a migration
// that moves rows sees none under the forced row-level security — and
// followed by recording Version in the version table, so pgstore.CheckSchema
// reads it and Run carries on from it.
func (s Step) SQL() string {
	record := fmt.Sprintf("UPDATE %s SET version = %d", versionTable, s.Version)
	if !s.InTransaction {
		return s.Migration + stepSeparator + record
	}
	return stepPrelude + s.Migration + stepSeparator + record
}

// stepSeparator ends a migration's last statement before the one Step.SQL
// adds, whether or not the migration ends it itself.
const stepSeparator = "\n;\n"

// disableTxPattern is the line by which a migration asks tern to run it
// outside a transaction, a statement at a time; tern matches it as a whole
// line.
var disableTxPattern = regexp.MustCompile(`(?m)^---- tern: disable-tx ----$`)

// sqlSetupVersion creates the schema and the version table at version 0,
// as Run's tern does before the first migration.
const sqlSetupVersion = `CREATE SCHEMA IF NOT EXISTS ` + schema + `;
CREATE TABLE IF NOT EXISTS ` + versionTable + `(version int4 NOT NULL PRIMARY KEY);
INSERT INTO ` + versionTable + `(version) SELECT 0 WHERE 0 = (SELECT count(*) FROM ` + versionTable + `)`

// Setup is the SQL a consumer's tool runs once, before the first Step: it
// creates the schema and the version table Run would. Run it as the role
// that owns the schema and applies the steps.
func Setup() string { return sqlSetupVersion }

// stepPrelude begins a step run in a transaction: what Run's OnStart runs
// as each migration starts.
var stepPrelude = sqlReadCommitted + ";\nSELECT set_config('" + pgschema.SettingAllTenants + "', '" + pgschema.SettingAllTenantsOn + "', true);\n"

// Steps are the migrations Run runs, in order, for a consumer's own
// migration tool to apply: Setup first, then the SQL of each step not yet
// applied, then Grants. The tool keeps its own record of what it applied;
// the steps keep pgstore's (Step.SQL).
func Steps() ([]Step, error) {
	m, err := loadMigrations(context.Background(), nil)
	if err != nil {
		return nil, err
	}
	steps := make([]Step, 0, len(m.Migrations))
	for _, mg := range m.Migrations {
		steps = append(steps, Step{
			Version: int(mg.Sequence), Name: mg.Name, Migration: mg.UpSQL,
			InTransaction: !disableTxPattern.MatchString(mg.UpSQL),
		})
	}
	return steps, nil
}
