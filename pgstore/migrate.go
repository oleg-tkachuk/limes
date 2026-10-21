package pgstore

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/tern/v2/migrate"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// migrationsDir is where migrationFiles keeps the migrations.
const migrationsDir = "migrations"

// versionTableName is the table that records which migrations have run, and
// versionTable its name in the schema.
const (
	versionTableName = "schema_version"
	versionTable     = schema + "." + versionTableName
)

// MigrateOptions configures Migrate.
type MigrateOptions struct {
	// RuntimeRoles are the roles the stores serve as: roles that do not own
	// the tables, so row-level security binds them. Each gets USAGE on the
	// schema, SELECT, INSERT, UPDATE and DELETE on its tables, SELECT alone
	// on the version table (for CheckSchema), EXECUTE on its functions —
	// which no other role has — and the same on every table a later
	// migration creates. Granting is idempotent, so each Migrate
	// grants again. Empty: Migrate grants nothing, and the roles are the
	// consumer's to grant.
	RuntimeRoles []string
}

// Migrate brings the store's schema up to the one this version of the package
// queries, running each migration not yet applied in order, then grants the
// runtime roles opts names. Run it before the stores serve, as the role that
// owns the schema, and before any migration of the consumer's that refers to
// its tables.
//
// It holds a session advisory lock while it migrates, so replicas starting
// together migrate once. The lock is the one every migration run by
// github.com/jackc/tern takes; it is reentrant within one session, so a
// consumer migrating with tern runs Migrate on the connection its own run
// holds, never on a second one, which would wait on that run for ever. A
// consumer with another migration tool runs Migrate before it.
//
// Migration 014 (pgstore v0.31.0) builds three indexes with a plain CREATE
// INDEX, which holds writes to limes.capabilities and limes.revocations
// until each is built: Insert, Revoke, Purge and the erasers wait, while
// reads, IsRevoked among them, go on. On a million records the builds take
// some seconds; schedule the first Migrate of v0.31.0 or later for a quiet
// moment on a store that large. Every later migration builds an index on an
// existing table CONCURRENTLY, outside a transaction, holding no writes.
//
// Migration 015 adds limes.capabilities.revoked,
// the flag ListRecords and CountRecords read: a change of the catalog, which
// rewrites no row and holds the table only for that moment. 016 then marks
// the records revoked before it, taking only their rows, and 017 builds its
// indexes CONCURRENTLY.
//
// Migrate needs one connection, not a pool; over a pool:
//
//	err := pool.AcquireFunc(ctx, func(c *pgxpool.Conn) error {
//		return pgstore.Migrate(ctx, c.Conn(), pgstore.MigrateOptions{RuntimeRoles: []string{"app"}})
//	})
func Migrate(ctx context.Context, conn *pgx.Conn, opts MigrateOptions) error {
	if conn == nil {
		return errors.New("pgstore: migrate: a connection is required")
	}
	if err := checkRuntimeRoles(opts.RuntimeRoles); err != nil {
		return fmt.Errorf("pgstore: migrate: %w", err)
	}
	// The version table lives in the schema, so the schema comes first.
	if _, err := conn.Exec(ctx, "CREATE SCHEMA IF NOT EXISTS "+pgx.Identifier{schema}.Sanitize()); err != nil {
		return fmt.Errorf("pgstore: migrate: create schema: %w", err)
	}
	// A schema of the store's name made earlier by another role is that
	// role's to change: it could drop or replace the tables and policies
	// Migrate puts in it.
	var owned bool
	if err := conn.QueryRow(ctx, sqlSchemaOwned, schema).Scan(&owned); err != nil {
		return fmt.Errorf("pgstore: migrate: schema owner: %w", err)
	}
	if !owned {
		return fmt.Errorf("%w: %s is owned by another role than the one migrating", ErrSchemaOwner, schema)
	}
	m, err := migrate.NewMigrator(ctx, conn, versionTable)
	if err != nil {
		return fmt.Errorf("pgstore: migrate: %w", err)
	}
	dir, err := fs.Sub(migrationFiles, migrationsDir)
	if err != nil {
		return fmt.Errorf("pgstore: migrate: %w", err)
	}
	if err := m.LoadMigrations(dir); err != nil {
		return fmt.Errorf("pgstore: migrate: load: %w", err)
	}
	if err := runMigrations(ctx, conn, m); err != nil {
		return err
	}
	return grant(ctx, conn, opts.RuntimeRoles)
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
	_, err := conn.Exec(ctx, sqlSetSession, SettingAllTenants, SettingAllTenantsOn)
	return err
}

// runMigrations runs m's migrations on conn scoped to every tenant, each of
// them: tern resets the session's settings after every migration, so the
// scope is set again as each one starts. tern runs a migration whatever its
// OnStart finds, so a scope that cannot be set closes the connection: the
// migration fails before it runs unscoped, its version is not recorded, and
// the run ends with the error.
func runMigrations(ctx context.Context, conn *pgx.Conn, m *migrate.Migrator) error {
	var scopeErr error
	m.OnStart = func(int32, string, string, string) {
		if scopeErr != nil {
			return
		}
		if err := scopeMigration(ctx, conn); err != nil {
			scopeErr = fmt.Errorf("pgstore: migrate: scope a migration to every tenant, connection closed: %w", err)
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
	if err := conn.QueryRow(ctx, sqlSettingNow, SettingAllTenants).Scan(&before); err != nil {
		return fmt.Errorf("pgstore: migrate: read the scope: %w", err)
	}
	if _, err := conn.Exec(ctx, sqlSetSession, SettingAllTenants, SettingAllTenantsOn); err != nil {
		return fmt.Errorf("pgstore: migrate: scope to every tenant: %w", err)
	}
	defer func() {
		restoreCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), restoreScopeTimeout)
		defer cancel()
		if _, rerr := conn.Exec(restoreCtx, sqlSetSession, SettingAllTenants, before); rerr != nil {
			_ = conn.Close(restoreCtx)
			err = errors.Join(err, fmt.Errorf("pgstore: migrate: restore the scope, connection closed: %w", rerr))
		}
	}()
	if err := run(ctx); err != nil {
		return fmt.Errorf("pgstore: migrate: %w", err)
	}
	return nil
}

// ErrSchemaOwner is Migrate finding the store's schema owned by another role
// than the one it runs as.
var ErrSchemaOwner = errors.New("pgstore: the schema belongs to another role")

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

// grant gives each runtime role what the stores need of the schema, in one
// transaction, so a role is granted everything or nothing.
func grant(ctx context.Context, conn *pgx.Conn, roles []string) error {
	if len(roles) == 0 {
		return nil
	}
	schemaName := pgx.Identifier{schema}.Sanitize()
	versions := pgx.Identifier{schema, versionTableName}.Sanitize()
	err := pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		for _, r := range roles {
			role := pgx.Identifier{r}.Sanitize()
			for _, stmt := range []string{
				"GRANT USAGE ON SCHEMA " + schemaName + " TO " + role,
				"GRANT " + tablePrivileges + " ON ALL TABLES IN SCHEMA " + schemaName + " TO " + role,
				// The version table is the migrator's; a runtime role
				// only reads it.
				"REVOKE " + tablePrivileges + " ON " + versions + " FROM " + role,
				"GRANT SELECT ON " + versions + " TO " + role,
				// The schema's functions: the policies' scope checks and
				// the revocation checks, which no other role may call.
				"GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA " + schemaName + " TO " + role,
				"ALTER DEFAULT PRIVILEGES IN SCHEMA " + schemaName + " GRANT " + tablePrivileges + " ON TABLES TO " + role,
			} {
				if _, err := tx.Exec(ctx, stmt); err != nil {
					return fmt.Errorf("role %s: %w", r, err)
				}
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("pgstore: migrate: grant: %w", err)
	}
	return nil
}

// tablePrivileges are what a runtime role may do to a table of the store.
const tablePrivileges = "SELECT, INSERT, UPDATE, DELETE"

// ErrSchemaMismatch is CheckSchema finding a schema at another version than
// the one this version of the package queries: behind it, Migrate has not
// run; ahead of it, a later version of the package migrated.
var ErrSchemaMismatch = errors.New("pgstore: schema version mismatch")

// SchemaVersion is the schema's version as the database has it and as this
// version of the package needs it.
type SchemaVersion struct {
	// Applied is the last migration run; zero when none has.
	Applied int
	// Expected is the last migration this package carries.
	Expected int
}

// The SQLSTATE codes of a version table that is not there: the table, or
// the whole schema.
const (
	undefinedTable    = "42P01"
	invalidSchemaName = "3F000"
)

// sqlSchemaVersion reads the version tern records.
const sqlSchemaVersion = `SELECT version FROM ` + versionTable

// ReadSchemaVersion reads the schema's version. It needs only SELECT on the
// version table, which Migrate grants each runtime role.
func ReadSchemaVersion(ctx context.Context, db DB) (SchemaVersion, error) {
	expected, err := expectedVersion()
	if err != nil {
		return SchemaVersion{}, err
	}
	v := SchemaVersion{Expected: expected}
	// tern keeps the version as an int4.
	var applied int32
	err = db.QueryRow(ctx, sqlSchemaVersion).Scan(&applied)
	// With no version table — Migrate never ran here — no migration has
	// been applied: version 0, not an outage.
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && (pgErr.Code == undefinedTable || pgErr.Code == invalidSchemaName) {
		return v, nil
	}
	if err != nil {
		return SchemaVersion{}, fmt.Errorf("pgstore: schema version: %w", err)
	}
	v.Applied = int(applied)
	return v, nil
}

// ErrRuntimeRole is CheckSchema finding the store served by a role
// row-level security does not hold to its tenant: a superuser or a role with
// BYPASSRLS, which it never binds, or an owner of the schema or of anything
// in it, which may turn it off or replace what enforces it — that role
// itself, or one it can act as (SET ROLE) or inherits from. The owner
// migrates; a role that owns nothing serves (Migrate's RuntimeRoles).
var ErrRuntimeRole = errors.New("pgstore: the store is served by a role row-level security does not hold")

// sqlRuntimeRole reads what CheckSchema refuses of the role the store is
// served by — each through membership, which takes in the role itself and
// every role it can act as or inherits from: a superuser; a role with
// BYPASSRLS; an owner of the schema $1, or of a table, sequence, view or
// function in it, which may disable its row-level security, drop a policy or
// replace the functions the policies and revocation checks call. It asks of
// the session's role and of the role the connection logged in as as well as
// the current one: a session opened as one of these that SET ROLE, or SET
// SESSION AUTHORIZATION, to another can RESET either back. The login role is
// the backend's in pg_stat_activity, which SET SESSION AUTHORIZATION leaves
// alone.
const sqlRuntimeRole = `
WITH serving (role) AS (
    VALUES (current_user), (session_user)
  UNION
    SELECT usename FROM pg_stat_activity WHERE pid = pg_backend_pid() AND usename IS NOT NULL
)
SELECT
    EXISTS (SELECT 1 FROM serving s, pg_roles r WHERE r.rolsuper AND pg_has_role(s.role, r.oid, 'MEMBER')),
    EXISTS (SELECT 1 FROM serving s, pg_roles r WHERE r.rolbypassrls AND pg_has_role(s.role, r.oid, 'MEMBER')),
    EXISTS (SELECT 1 FROM serving s, pg_namespace n WHERE n.nspname = $1 AND pg_has_role(s.role, n.nspowner, 'MEMBER'))
    OR EXISTS (SELECT 1 FROM serving s, pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
               WHERE n.nspname = $1 AND pg_has_role(s.role, c.relowner, 'MEMBER'))
    OR EXISTS (SELECT 1 FROM serving s, pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
               WHERE n.nspname = $1 AND pg_has_role(s.role, p.proowner, 'MEMBER'))`

// CheckSchema is a readiness check for the stores over db. It returns
// ErrSchemaMismatch when the schema is not at the version this package
// queries — a store over it would fail on a table or column it expects —
// and ErrRuntimeRole when db is served by a role row-level security does not
// hold to its tenant, so a store over it would isolate no tenant.
func CheckSchema(ctx context.Context, db DB) error {
	v, err := ReadSchemaVersion(ctx, db)
	if err != nil {
		return err
	}
	if v.Applied != v.Expected {
		return fmt.Errorf("%w: the database is at %d, this package needs %d", ErrSchemaMismatch, v.Applied, v.Expected)
	}
	var super, bypass, owns bool
	if err := db.QueryRow(ctx, sqlRuntimeRole, schema).Scan(&super, &bypass, &owns); err != nil {
		return fmt.Errorf("pgstore: runtime role: %w", err)
	}
	switch {
	case super:
		return fmt.Errorf("%w: it is, or can act as, a superuser", ErrRuntimeRole)
	case bypass:
		return fmt.Errorf("%w: it has, or can act as a role with, BYPASSRLS", ErrRuntimeRole)
	case owns:
		return fmt.Errorf("%w: it owns, or can act as a role that owns, %s or something in it", ErrRuntimeRole, schema)
	}
	return nil
}

// expectedVersion is the number of migrations the package carries: tern
// numbers them from one without a gap, so the count is the last one's.
func expectedVersion() (int, error) {
	files, err := fs.Glob(migrationFiles, path.Join(migrationsDir, "*.sql"))
	if err != nil {
		return 0, err
	}
	return len(files), nil
}
