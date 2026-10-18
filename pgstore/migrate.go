package pgstore

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"

	"github.com/jackc/pgx/v5"
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
	// on the version table (for CheckSchema), and the same on every table a
	// later migration creates. Granting is idempotent, so each Migrate
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
	if err := m.Migrate(ctx); err != nil {
		return fmt.Errorf("pgstore: migrate: %w", err)
	}
	return grant(ctx, conn, opts.RuntimeRoles)
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
	Applied int32
	// Expected is the last migration this package carries.
	Expected int32
}

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
	if err := db.QueryRow(ctx, sqlSchemaVersion).Scan(&v.Applied); err != nil {
		return SchemaVersion{}, fmt.Errorf("pgstore: schema version: %w", err)
	}
	return v, nil
}

// CheckSchema returns ErrSchemaMismatch when the schema is not at the version
// this package queries, for a readiness check: a store over it would fail on
// a table or column it expects.
func CheckSchema(ctx context.Context, db DB) error {
	v, err := ReadSchemaVersion(ctx, db)
	if err != nil {
		return err
	}
	if v.Applied != v.Expected {
		return fmt.Errorf("%w: the database is at %d, this package needs %d", ErrSchemaMismatch, v.Applied, v.Expected)
	}
	return nil
}

// expectedVersion is the number of migrations the package carries: tern
// numbers them from one without a gap, so the count is the last one's.
func expectedVersion() (int32, error) {
	files, err := fs.Glob(migrationFiles, path.Join(migrationsDir, "*.sql"))
	if err != nil {
		return 0, err
	}
	return int32(len(files)), nil //nolint:gosec // a handful of embedded files
}
