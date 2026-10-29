package migrate

import (
	"context"
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"uuid"

	"github.com/jackc/pgx/v5"

	"github.com/oleg-tkachuk/limes/pgstore/internal/pgschema"
)

// The steps are the embedded migrations, numbered from one as tern numbers
// them, the last at the version pgstore queries, each outside a transaction
// exactly when its file asks tern for that.
func TestStepsAreTheEmbeddedMigrations(t *testing.T) {
	steps, err := Steps()
	if err != nil {
		t.Fatal(err)
	}
	files, err := fs.Glob(migrationFiles, migrationsDir+"/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != len(files) || len(steps) != pgschema.Version {
		t.Fatalf("%d steps from %d files; pgschema.Version is %d", len(steps), len(files), pgschema.Version)
	}
	for i, s := range steps {
		raw, err := fs.ReadFile(migrationFiles, files[i])
		if err != nil {
			t.Fatal(err)
		}
		if s.Version != i+1 || !strings.HasSuffix(files[i], "/"+s.Name) {
			t.Errorf("step %d is version %d, %s; want version %d, %s", i, s.Version, s.Name, i+1, files[i])
		}
		if want := !strings.Contains(string(raw), disableTx); s.InTransaction != want {
			t.Errorf("%s: InTransaction %v, want %v", s.Name, s.InTransaction, want)
		}
		if strings.TrimSpace(string(raw)) != s.Migration {
			t.Errorf("%s: Migration is not the file as tern reads it", s.Name)
		}
	}
}

// Step.SQL records the step's version after the migration, and runs a
// step in a transaction read committed and scoped to every tenant first.
func TestStepSQL(t *testing.T) {
	in := Step{Version: 7, Migration: "SELECT 1", InTransaction: true}
	out := Step{Version: 8, Migration: "SELECT 2", InTransaction: false}
	for _, tc := range []struct {
		step   Step
		prefix string
		suffix string
	}{
		{in, sqlReadCommitted, "UPDATE " + versionTable + " SET version = 7"},
		{out, "SELECT 2", "UPDATE " + versionTable + " SET version = 8"},
	} {
		sql := tc.step.SQL()
		if !strings.HasPrefix(sql, tc.prefix) || !strings.HasSuffix(sql, tc.suffix) || !strings.Contains(sql, tc.step.Migration) {
			t.Errorf("Step%+v.SQL() = %q, want it to begin %q, hold the migration, and end %q", tc.step, sql, tc.prefix, tc.suffix)
		}
	}
	if !strings.Contains(in.SQL(), "'"+pgschema.SettingAllTenants+"', '"+pgschema.SettingAllTenantsOn+"', true") {
		t.Errorf("a step in a transaction is not scoped to every tenant: %q", in.SQL())
	}
	if strings.Contains(out.SQL(), pgschema.SettingAllTenants) || strings.Contains(out.SQL(), sqlReadCommitted) {
		t.Errorf("a step outside a transaction sets what only a transaction holds: %q", out.SQL())
	}
}

// The databases the equivalence check builds its schemas in.
const (
	byRunDatabase   = "limes_by_run"
	byStepsDatabase = "limes_by_steps"
	halfwayDatabase = "limes_halfway"
)

// halfway is how many steps a consumer's tool applies before Run takes
// over: past the first step run outside a transaction.
const halfway = 15

// applySteps applies steps to conn as a consumer's tool would: each in a
// transaction of its own, or a statement at a time outside one.
func applySteps(t *testing.T, conn *pgx.Conn, steps []Step) {
	t.Helper()
	ctx := t.Context()
	for _, s := range steps {
		if !s.InTransaction {
			for _, stmt := range statements(s.SQL()) {
				if _, err := conn.Exec(ctx, stmt); err != nil {
					t.Fatalf("%s: %v", s.Name, err)
				}
			}
			continue
		}
		if err := pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, s.SQL())
			return err
		}); err != nil {
			t.Fatalf("%s: %v", s.Name, err)
		}
	}
}

// applyGrants runs Grants for roles in one transaction.
func applyGrants(t *testing.T, conn *pgx.Conn, roles []string) {
	t.Helper()
	grants, err := Grants(roles)
	if err != nil {
		t.Fatal(err)
	}
	if err := pgx.BeginFunc(t.Context(), conn, func(tx pgx.Tx) error {
		for _, g := range grants {
			if _, err := tx.Exec(t.Context(), g); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// withRuntimeRole creates runtimeRole for the test, dropped with what it
// was granted in each of dbs when it ends.
func withRuntimeRole(t *testing.T, dbs ...*pgx.Conn) {
	t.Helper()
	role := pgx.Identifier{runtimeRole}.Sanitize()
	for _, stmt := range []string{"DROP ROLE IF EXISTS " + role, "CREATE ROLE " + role} {
		if _, err := owner.Exec(t.Context(), stmt); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for _, db := range dbs {
			_, _ = db.Exec(context.Background(), "DROP OWNED BY "+role)
		}
		_, _ = owner.Exec(context.Background(), "DROP ROLE IF EXISTS "+role)
	})
}

// A schema a consumer's tool builds from the steps is the schema Run
// builds, version table and grants included; and a tool's schema halfway
// up is one Run carries on from to the same end.
func TestStepsBuildTheSchemaRunBuilds(t *testing.T) {
	ctx := t.Context()
	byRun := pg.FreshConn(t, byRunDatabase)
	bySteps := pg.FreshConn(t, byStepsDatabase)
	halfwayUp := pg.FreshConn(t, halfwayDatabase)
	withRuntimeRole(t, byRun, bySteps, halfwayUp)
	roles := []string{runtimeRole}
	steps, err := Steps()
	if err != nil {
		t.Fatal(err)
	}

	if err := Run(ctx, byRun, Options{RuntimeRoles: roles}); err != nil {
		t.Fatal(err)
	}
	for _, conn := range []*pgx.Conn{bySteps, halfwayUp} {
		if _, err := conn.Exec(ctx, Setup()); err != nil {
			t.Fatal(err)
		}
	}
	applySteps(t, bySteps, steps)
	applyGrants(t, bySteps, roles)
	applySteps(t, halfwayUp, steps[:halfway])
	if err := Run(ctx, halfwayUp, Options{RuntimeRoles: roles}); err != nil {
		t.Fatalf("Run after %d steps: %v", halfway, err)
	}

	want, err := pg.Dump(byRunDatabase)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(want, "CREATE TABLE "+schema+".capabilities") {
		t.Fatalf("the dump of Run's schema holds no records table:\n%s", want)
	}
	for _, db := range []string{byStepsDatabase, halfwayDatabase} {
		got, err := pg.Dump(db)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("the schema in %s differs from Run's:\n%s", db, firstDifference(want, got))
		}
	}
	for _, conn := range []*pgx.Conn{bySteps, halfwayUp} {
		var v int
		if err := conn.QueryRow(ctx, "SELECT version FROM "+versionTable).Scan(&v); err != nil || v != pgschema.Version {
			t.Errorf("version = %d, %v; want %d", v, err, pgschema.Version)
		}
	}
}

// firstDifference is the first line at which two dumps part, with each
// side's.
func firstDifference(want, got string) string {
	w, g := strings.Split(want, "\n"), strings.Split(got, "\n")
	for i := range min(len(w), len(g)) {
		if w[i] != g[i] {
			return fmt.Sprintf("line %d:\n  Run:   %s\n  other: %s", i+1, w[i], g[i])
		}
	}
	return fmt.Sprintf("one dump is longer: %d lines against %d", len(w), len(g))
}

// The role a consumer's tool migrates as in the row-level security check:
// no superuser, so the forced policies bind it as they bind a deployment's
// migration role, and the database it migrates.
const (
	stepsOwnerRole     = "limes_steps_owner"
	stepsOwnerPassword = "limes-steps-owner-test"
	rlsStepsDatabase   = "limes_rls_steps"
	// beforeRevokedFlag is how many steps run before 015 adds the flag
	// that 016 then sets on the records revoked before it.
	beforeRevokedFlag = 14
	// throughMarkRevoked is the last step of the check: 016.
	throughMarkRevoked = 16
)

// SQL that seeds a revoked root and its child, and reads a record's flag.
const (
	sqlSeedRecord     = `INSERT INTO limes.capabilities (id, parent_id, tenant_id, principal_type, subject, depth, capability, issued_by) VALUES ($1, $2, $3, 'agent', 'agent', $4, '{}', '{}')`
	sqlSeedRevocation = `INSERT INTO limes.revocations (capability_id, tenant_id, revoked_at, note, actor, cascaded) VALUES ($1, $2, now(), 'test', 'test', false)`
	sqlReadRevoked    = `SELECT revoked FROM limes.capabilities WHERE id = $1`
)

// A step that moves rows moves them under the forced row-level security
// when a tool applies it as a role the policies bind: Step.SQL scopes it to
// every tenant, as Run does, or 016 would mark no record and say nothing.
func TestAStepMovesRowsUnderRowSecurity(t *testing.T) {
	ctx := t.Context()
	pg.FreshConn(t, rlsStepsDatabase)
	role := pgx.Identifier{stepsOwnerRole}.Sanitize()
	for _, stmt := range []string{
		"DROP ROLE IF EXISTS " + role,
		"CREATE ROLE " + role + " LOGIN PASSWORD '" + stepsOwnerPassword + "'",
		"GRANT CREATE ON DATABASE " + pgx.Identifier{rlsStepsDatabase}.Sanitize() + " TO " + role,
	} {
		if _, err := owner.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := pgx.ParseConfig(ownerDSN)
	if err != nil {
		t.Fatal(err)
	}
	cfg.User, cfg.Password, cfg.Database = stepsOwnerRole, stepsOwnerPassword, rlsStepsDatabase
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = conn.Close(context.Background())
		_, _ = owner.Exec(context.Background(), "DROP DATABASE IF EXISTS "+pgx.Identifier{rlsStepsDatabase}.Sanitize())
		_, _ = owner.Exec(context.Background(), "DROP ROLE IF EXISTS "+role)
	})
	steps, err := Steps()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, Setup()); err != nil {
		t.Fatal(err)
	}
	applySteps(t, conn, steps[:beforeRevokedFlag])

	scope := func(value string) {
		t.Helper()
		if _, err := conn.Exec(ctx, sqlSetSession, pgschema.SettingAllTenants, value); err != nil {
			t.Fatal(err)
		}
	}
	tenant, root, child := uuid.New(), uuid.New(), uuid.New()
	scope(pgschema.SettingAllTenantsOn)
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{sqlSeedRecord, []any{root, nil, tenant, 0}},
		{sqlSeedRecord, []any{child, root, tenant, 1}},
		{sqlSeedRevocation, []any{root, tenant}},
	} {
		if _, err := conn.Exec(ctx, q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	// The tool's session holds no scope of its own: the steps set theirs.
	scope("")
	applySteps(t, conn, steps[beforeRevokedFlag:throughMarkRevoked])

	scope(pgschema.SettingAllTenantsOn)
	for name, id := range map[string]uuid.UUID{"the revoked root": root, "its child": child} {
		var revoked bool
		if err := conn.QueryRow(ctx, sqlReadRevoked, id).Scan(&revoked); err != nil {
			t.Fatal(err)
		}
		if !revoked {
			t.Errorf("%s is not marked revoked after 016 applied as a step", name)
		}
	}
}

// Grants refuses a runtime role that is not one role, as Run does, and
// grants nothing for none.
func TestGrantsRefusesARoleThatIsNotOne(t *testing.T) {
	for _, role := range []string{"", "public", "PUBLIC", "app\x00role"} {
		if stmts, err := Grants([]string{role}); err == nil {
			t.Errorf("Grants(%q) = %d statements, want an error", role, len(stmts))
		}
	}
	if stmts, err := Grants(nil); err != nil || len(stmts) != 0 {
		t.Errorf("Grants(nil) = %v, %v; want nothing", stmts, err)
	}
}
