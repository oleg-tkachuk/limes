package migrate

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	tern "github.com/jackc/tern/v2/migrate"

	"github.com/oleg-tkachuk/limes/pgstore/internal/pgschema"
)

// freshConn is a connection, as the owner, to a database of its own with no
// schema in it, dropped when the test ends.
func freshConn(t *testing.T) *pgx.Conn {
	t.Helper()
	return pg.FreshConn(t, freshDatabase)
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
	opts := Options{RuntimeRoles: []string{runtimeRole}}
	for range 2 {
		if err := Run(ctx, conn, opts); err != nil {
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
	if err := Run(t.Context(), conn, Options{RuntimeRoles: []string{""}}); err == nil {
		t.Fatal("Migrate with an empty role name: no error")
	}
	if err := Run(t.Context(), nil, Options{}); err == nil {
		t.Fatal("Migrate with no connection: no error")
	}
}

// A runtime role is one role: "public" in any case names every role there
// is, and a NUL would be dropped from the name, so Migrate refuses both
// before it changes anything.
func TestMigrateRefusesARuntimeRoleThatIsNotOne(t *testing.T) {
	conn := freshConn(t)
	ctx := t.Context()
	for _, role := range []string{"", "public", "PUBLIC", "Public", "app\x00role"} {
		if err := Run(ctx, conn, Options{RuntimeRoles: []string{role}}); err == nil {
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
	if err := Run(ctx, conn, Options{}); !errors.Is(err, ErrSchemaOwner) {
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
	m, err := tern.NewMigrator(ctx, conn, probeVersionTable)
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
		if got := ptrText(scope); got != pgschema.SettingAllTenantsOn {
			t.Errorf("migration %d ran scoped to %q, want %q", i+2, got, pgschema.SettingAllTenantsOn)
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

// errScopeRefused stands in for a scope the session refuses.
var errScopeRefused = errors.New("scope refused")

// A migration whose scope cannot be set does not run unscoped, even one tern
// runs outside a transaction, and its version is not recorded: the run fails
// there.
func TestAMigrationThatCannotBeScopedDoesNotRun(t *testing.T) {
	conn := freshConn(t)
	ctx := t.Context()
	m, err := tern.NewMigratorEx(ctx, conn, probeVersionTable, &tern.MigratorOptions{DisableTx: true})
	if err != nil {
		t.Fatal(err)
	}
	m.AppendMigration("create the probe", createProbe, "")
	m.AppendMigration("record the scope", recordProbe, "")
	scoped := 0
	original := scopeMigration
	scopeMigration = func(ctx context.Context, conn *pgx.Conn) error {
		if scoped++; scoped > 1 {
			return errScopeRefused
		}
		return original(ctx, conn)
	}
	t.Cleanup(func() { scopeMigration = original })
	if err := runMigrations(ctx, conn, m); !errors.Is(err, errScopeRefused) {
		t.Fatalf("runMigrations with a scope refused: err = %v, want errScopeRefused", err)
	}

	cfg, err := pgx.ParseConfig(ownerDSN)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Database = freshDatabase
	after, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = after.Close(context.WithoutCancel(ctx)) }()
	var recorded int
	if err := after.QueryRow(ctx, `SELECT count(*) FROM public.scope_probe`).Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	if recorded != 0 {
		t.Errorf("the migration that could not be scoped ran: %d rows recorded", recorded)
	}
	var version int32
	if err := after.QueryRow(ctx, `SELECT version FROM `+probeVersionTable).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if want := int32(1); version != want {
		t.Errorf("version after the refused migration = %d, want %d", version, want)
	}
}

// The isolation a probe migration runs at, and the table it writes it to.
const (
	isolationProbeVersionTable = "public.isolation_probe_version"
	recordIsolation            = `CREATE TABLE public.isolation_probe AS SELECT current_setting('transaction_isolation') AS isolation`
	readIsolation              = `SELECT isolation FROM public.isolation_probe`
	sqlSessionRepeatableRead   = `SET default_transaction_isolation = 'repeatable read'`
	readCommittedLevel         = "read committed"
)

// A migration runs read committed even where the session's default is
// repeatable read: 016's walk reads each level of records by a statement's
// own snapshot.
func TestMigrationsRunReadCommitted(t *testing.T) {
	conn := freshConn(t)
	ctx := t.Context()
	if _, err := conn.Exec(ctx, sqlSessionRepeatableRead); err != nil {
		t.Fatal(err)
	}
	m, err := tern.NewMigrator(ctx, conn, isolationProbeVersionTable)
	if err != nil {
		t.Fatal(err)
	}
	m.AppendMigration("record the isolation", recordIsolation, "")
	if err := runMigrations(ctx, conn, m); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := conn.QueryRow(ctx, readIsolation).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != readCommittedLevel {
		t.Errorf("a migration ran at %q, want %q", got, readCommittedLevel)
	}
}
