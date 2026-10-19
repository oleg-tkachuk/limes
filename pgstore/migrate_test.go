package pgstore

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/tern/v2/migrate"
)

// The database and role the migration checks start from nothing in.
const (
	freshDatabase = "limes_fresh"
	runtimeRole   = "limes_runtime"
	// laterTable stands in for a table a later migration creates.
	laterTable = schema + ".later"
)

// freshConn is a connection, as the owner, to a database of its own with no
// schema in it, dropped when the test ends.
func freshConn(t *testing.T) *pgx.Conn {
	t.Helper()
	ctx := t.Context()
	db := pgx.Identifier{freshDatabase}.Sanitize()
	if _, err := owner.Exec(ctx, "DROP DATABASE IF EXISTS "+db); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, "CREATE DATABASE "+db); err != nil {
		t.Fatal(err)
	}
	cfg, err := pgx.ParseConfig(ownerDSN)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Database = freshDatabase
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = conn.Close(context.Background())
		_, _ = owner.Exec(context.Background(), "DROP DATABASE IF EXISTS "+db)
	})
	return conn
}

// The runtime roles get the schema, its tables and the tables of later
// migrations, and only read the version table; a second Migrate grants the
// same again.
func TestMigrateGrantsTheRuntimeRoles(t *testing.T) {
	conn := freshConn(t)
	ctx := t.Context()
	role := pgx.Identifier{runtimeRole}.Sanitize()
	if _, err := conn.Exec(ctx, "DROP ROLE IF EXISTS "+role); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, "CREATE ROLE "+role); err != nil {
		t.Fatal(err)
	}
	// The role's privileges go first, in the database that holds them.
	t.Cleanup(func() {
		_, _ = conn.Exec(context.Background(), "DROP OWNED BY "+role)
		_, _ = owner.Exec(context.Background(), "DROP ROLE IF EXISTS "+role)
	})
	opts := MigrateOptions{RuntimeRoles: []string{runtimeRole}}
	for range 2 {
		if err := Migrate(ctx, conn, opts); err != nil {
			t.Fatalf("Migrate: %v", err)
		}
	}
	if _, err := conn.Exec(ctx, "CREATE TABLE "+laterTable+" (id integer)"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		table, privilege string
		want             bool
	}{
		{schema + ".capabilities", "DELETE", true},
		{schema + ".charges", "INSERT", true},
		{schema + ".usage", "UPDATE", true},
		{laterTable, "DELETE", true},
		{versionTable, "SELECT", true},
		{versionTable, "INSERT", false},
		{versionTable, "UPDATE", false},
	} {
		var got bool
		if err := conn.QueryRow(ctx, `SELECT has_table_privilege($1, $2, $3)`, runtimeRole, tc.table, tc.privilege).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Errorf("%s on %s: %v, want %v", tc.privilege, tc.table, got, tc.want)
		}
	}
	var usage bool
	if err := conn.QueryRow(ctx, `SELECT has_schema_privilege($1, $2, 'USAGE')`, runtimeRole, schema).Scan(&usage); err != nil || !usage {
		t.Errorf("USAGE on the schema = %v, %v; want granted", usage, err)
	}
}

// A role with no name is refused, and nothing of the grant stays.
func TestMigrateRefusesAnEmptyRole(t *testing.T) {
	conn := freshConn(t)
	if err := Migrate(t.Context(), conn, MigrateOptions{RuntimeRoles: []string{""}}); err == nil {
		t.Fatal("Migrate with an empty role name: no error")
	}
	if err := Migrate(t.Context(), nil, MigrateOptions{}); err == nil {
		t.Fatal("Migrate with no connection: no error")
	}
}

// The schema's version reads back as the package expects once migrated, and
// CheckSchema tells a schema behind or ahead of it, readable as the
// runtime role.
func TestCheckSchema(t *testing.T) {
	ctx := t.Context()
	v, err := ReadSchemaVersion(ctx, app)
	if err != nil {
		t.Fatalf("ReadSchemaVersion as the runtime role: %v", err)
	}
	if v.Applied == 0 || v.Applied != v.Expected {
		t.Errorf("ReadSchemaVersion = %+v, want the migrations this package carries applied", v)
	}
	if err := CheckSchema(ctx, app); err != nil {
		t.Errorf("CheckSchema as the runtime role: %v", err)
	}
	conn := freshConn(t)
	// A database Migrate never ran in has no version table, nor even the
	// schema: it is at version 0, which is a mismatch, not an outage.
	if got, err := ReadSchemaVersion(ctx, conn); err != nil || got.Applied != 0 {
		t.Errorf("ReadSchemaVersion before any Migrate = %+v, %v; want version 0", got, err)
	}
	if err := CheckSchema(ctx, conn); !errors.Is(err, ErrSchemaMismatch) {
		t.Errorf("CheckSchema before any Migrate: err = %v, want ErrSchemaMismatch", err)
	}
	if err := Migrate(ctx, conn, MigrateOptions{}); err != nil {
		t.Fatal(err)
	}
	for name, shift := range map[string]int{"behind": -1, "ahead": 1} {
		if _, err := conn.Exec(ctx, `UPDATE `+versionTable+` SET version = $1`, v.Expected+shift); err != nil {
			t.Fatal(err)
		}
		if err := CheckSchema(ctx, conn); !errors.Is(err, ErrSchemaMismatch) {
			t.Errorf("CheckSchema of a schema %s: err = %v, want ErrSchemaMismatch", name, err)
		}
	}
}

// A runtime role is one role: "public" in any case names every role there
// is, and a NUL would be dropped from the name, so Migrate refuses both
// before it changes anything.
func TestMigrateRefusesARuntimeRoleThatIsNotOne(t *testing.T) {
	conn := freshConn(t)
	ctx := t.Context()
	for _, role := range []string{"", "public", "PUBLIC", "Public", "app\x00role"} {
		if err := Migrate(ctx, conn, MigrateOptions{RuntimeRoles: []string{role}}); err == nil {
			t.Errorf("Migrate with runtime role %q: no error", role)
		}
	}
	var created bool
	if err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname = $1)`, schema).Scan(&created); err != nil {
		t.Fatal(err)
	}
	if created {
		t.Error("a refused Migrate created the schema")
	}
}

// intruderRole owns a schema of the store's name made before Migrate ran.
const intruderRole = "limes_intruder"

// A schema of the store's name that another role owns is not the store's:
// that role could drop or replace what Migrate creates in it, policies
// included, so Migrate refuses it.
func TestMigrateRefusesASchemaAnotherRoleOwns(t *testing.T) {
	conn := freshConn(t)
	ctx := t.Context()
	role := pgx.Identifier{intruderRole}.Sanitize()
	if _, err := conn.Exec(ctx, "DROP ROLE IF EXISTS "+role); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, "CREATE ROLE "+role); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = conn.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
		_, _ = owner.Exec(context.Background(), "DROP ROLE IF EXISTS "+role)
	})
	if _, err := conn.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()+" AUTHORIZATION "+role); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, conn, MigrateOptions{}); !errors.Is(err, ErrSchemaOwner) {
		t.Fatalf("Migrate into a schema %s owns: err = %v, want ErrSchemaOwner", intruderRole, err)
	}
}

// The scope a probe migration reads, and the table it writes it to.
const (
	probeVersionTable = "public.scope_probe_version"
	createProbe       = `CREATE TABLE public.scope_probe (scope text)`
	recordProbe       = `INSERT INTO public.scope_probe VALUES (current_setting('limes.all_tenants', true))`
	readProbe         = `SELECT scope FROM public.scope_probe`
)

// Every migration of a run reads and writes every tenant's rows, not only
// the first: tern resets the session's settings after each one.
func TestEveryMigrationRunsScopedToEveryTenant(t *testing.T) {
	conn := freshConn(t)
	ctx := t.Context()
	m, err := migrate.NewMigrator(ctx, conn, probeVersionTable)
	if err != nil {
		t.Fatal(err)
	}
	m.AppendMigration("create the probe", createProbe, "")
	m.AppendMigration("record the scope", recordProbe, "")
	m.AppendMigration("record it again", recordProbe, "")
	if err := runMigrations(ctx, conn, m); err != nil {
		t.Fatal(err)
	}
	rows, err := conn.Query(ctx, readProbe)
	if err != nil {
		t.Fatal(err)
	}
	scopes, err := pgx.CollectRows(rows, pgx.RowTo[*string])
	if err != nil {
		t.Fatal(err)
	}
	for i, scope := range scopes {
		if got := ptrText(scope); got != SettingAllTenantsOn {
			t.Errorf("migration %d ran scoped to %q, want %q", i+2, got, SettingAllTenantsOn)
		}
	}
}

// ptrText is *p, or "<unset>" for nil.
func ptrText(p *string) string {
	if p == nil {
		return "<unset>"
	}
	return *p
}
