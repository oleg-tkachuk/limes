package pgstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/oleg-tkachuk/limes/pgstore/internal/pgschema"

	"github.com/oleg-tkachuk/limes"
)

// versionTable is the table pgstore/migrate records the schema's version in.
const versionTable = pgschema.VersionTable

// ErrSchemaMismatch is CheckSchema finding a schema at another version than
// the one this version of the package queries: behind it, migrate.Run has not
// run; ahead of it, a later version of the package migrated.
var ErrSchemaMismatch = fmt.Errorf("%w: pgstore: schema version mismatch", limes.ErrInvalidConfig)

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
// version table, which pgstore/migrate grants each runtime role.
func ReadSchemaVersion(ctx context.Context, db DB) (SchemaVersion, error) {
	v := SchemaVersion{Expected: pgschema.Version}
	// tern keeps the version as an int4.
	var applied int32
	err := db.QueryRow(ctx, sqlSchemaVersion).Scan(&applied)
	// With no version table — migrate.Run never ran here — no migration has
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
// migrates; a role that owns nothing serves (migrate.Options.RuntimeRoles).
var ErrRuntimeRole = fmt.Errorf("%w: pgstore: the store is served by a role row-level security does not hold", limes.ErrInvalidConfig)

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
