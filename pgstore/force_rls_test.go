package pgstore

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// rlsTables are the tables row-level security guards.
var rlsTables = []string{
	"capabilities", "revocations", "biscuit_revocations", "usage", "copy_usage",
	"tenant_budgets", "charges", "reservations", "erased_tenants",
}

// unguardedTables are the tables of the schema row-level security does not
// guard, each for its reason: the version table holds no tenant's data and
// the runtime role only reads it; the DPoP replay table is read and written
// before any tenant is known.
var unguardedTables = []string{"schema_version", "dpop_seen_proofs"}

// sqlSchemaTables lists the tables of schema $1.
const sqlSchemaTables = `SELECT tablename FROM pg_tables WHERE schemaname = $1 ORDER BY tablename`

// Every table of the schema is guarded, or named as unguarded with its
// reason: a table a migration adds without row-level security fails here.
func TestEveryTableIsGuardedOrNamed(t *testing.T) {
	rows, err := owner.Query(t.Context(), sqlSchemaTables, schema)
	if err != nil {
		t.Fatal(err)
	}
	tables, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range tables {
		if !slices.Contains(rlsTables, table) && !slices.Contains(unguardedTables, table) {
			t.Errorf("table %s is neither guarded by row-level security nor named unguarded", table)
		}
	}
	for _, table := range append(slices.Clone(rlsTables), unguardedTables...) {
		if !slices.Contains(tables, table) {
			t.Errorf("table %s is named but the schema has none", table)
		}
	}
}

// sqlForced is whether row-level security binds the owner of table $2 of
// schema $1.
const sqlForced = `
SELECT c.relforcerowsecurity FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relname = $2`

// Every guarded table holds its owner to its policies.
func TestRowSecurityBindsTheOwner(t *testing.T) {
	for _, table := range rlsTables {
		var forced bool
		if err := owner.QueryRow(t.Context(), sqlForced, schema, table).Scan(&forced); err != nil {
			t.Fatalf("%s: %v", table, err)
		}
		if !forced {
			t.Errorf("%s: row-level security does not bind its owner", table)
		}
	}
}

// sqlCountCapabilities counts the capability records a role sees.
const sqlCountCapabilities = `SELECT count(*) FROM ` + schema + `.capabilities`

// The owner, which migrates, sees no tenant's records unless scoped to it:
// a service that connects as the owner isolates tenants all the same.
func TestTheOwnerSeesOnlyItsScope(t *testing.T) {
	f := newTenantFixture(t)
	for name, tc := range map[string]struct {
		ctx  context.Context
		want int
	}{
		"no scope":       {t.Context(), 0},
		"another tenant": {WithTenant(t.Context(), f.theirs), 0},
		"its tenant":     {WithTenant(t.Context(), f.mine), 2},
	} {
		var seen int
		err := pgx.BeginFunc(tc.ctx, migrator, func(tx pgx.Tx) error {
			if err := ScopeFromContext(tc.ctx, tx); err != nil {
				return err
			}
			return tx.QueryRow(tc.ctx, sqlCountCapabilities).Scan(&seen)
		})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if seen != tc.want {
			t.Errorf("%s: the owner sees %d records, want %d", name, seen, tc.want)
		}
	}
}

// sqlCallerScopeAfterChecks runs both revocation checks in the caller's
// transaction and reads its scope after them.
const sqlCallerScopeAfterChecks = `
SELECT (SELECT self FROM ` + schema + `.is_revoked($1, 1)), ` + schema + `.is_biscuit_revoked(ARRAY[$2::bytea]),
       coalesce(current_setting('` + SettingAllTenants + `', true), '')`

// A check scopes itself to every tenant for its own query only: the caller's
// transaction keeps the scope it had, never every tenant's rows.
func TestRevocationChecksLeaveTheCallersScope(t *testing.T) {
	f := newTenantFixture(t)
	for _, caller := range []string{settingUnset, SettingAllTenantsOn, SettingAllTenantsRead} {
		err := pgx.BeginFunc(t.Context(), app, func(tx pgx.Tx) error {
			if _, err := tx.Exec(t.Context(), sqlSetLocal, SettingAllTenants, caller); err != nil {
				return err
			}
			var self, copyRevoked bool
			var scope string
			if err := tx.QueryRow(t.Context(), sqlCallerScopeAfterChecks, f.parent.ID, []byte("any")).Scan(&self, &copyRevoked, &scope); err != nil {
				return err
			}
			if scope != caller {
				t.Errorf("the caller's scope after the checks = %q, want %q as before", scope, caller)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("a caller scoped %q: %v", caller, err)
		}
	}
}

// The roles TestCheckSchemaRefusesARoleTheyDoNotHold serves as, and the
// password each logs in with.
const (
	bypassRole        = "limes_bypass"
	bypassGroup       = "limes_bypass_group"
	bypassMember      = "limes_bypass_member"
	objectOwnerRole   = "limes_object_owner"
	ownerMemberRole   = "limes_owner_member"
	checkRolePassword = "limes-check-role-test"
	// probeFunction is a function of the schema objectOwnerRole owns.
	probeFunction = schema + ".check_role_probe()"
)

// loginRole creates a role that logs in and can read the schema version,
// runs setup as the superuser, and returns a pool as the role; all of it goes
// when the test ends.
func loginRole(t *testing.T, name, attributes string, setup ...string) *pgxpool.Pool {
	t.Helper()
	ctx := t.Context()
	role := pgx.Identifier{name}.Sanitize()
	stmts := append([]string{
		"DROP ROLE IF EXISTS " + role,
		"CREATE ROLE " + role + " LOGIN " + attributes + " PASSWORD '" + checkRolePassword + "'",
		"GRANT USAGE ON SCHEMA " + schema + " TO " + role,
		"GRANT SELECT ON " + versionTable + " TO " + role,
	}, setup...)
	for _, stmt := range stmts {
		if _, err := owner.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	pool, err := poolAs(ctx, ownerDSN, name, checkRolePassword)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = owner.Exec(context.Background(), "DROP OWNED BY "+role+"; DROP ROLE IF EXISTS "+role)
	})
	return pool
}

// CheckSchema refuses a store served by a role the policies do not hold —
// a superuser, one with BYPASSRLS, an owner of the schema or of anything in
// it, or a role that can act as any of these — and accepts one that owns
// nothing.
func TestCheckSchemaRefusesARoleTheyDoNotHold(t *testing.T) {
	ctx := t.Context()
	group := pgx.Identifier{bypassGroup}.Sanitize()
	if _, err := owner.Exec(ctx, "DROP ROLE IF EXISTS "+group+"; CREATE ROLE "+group+" NOLOGIN BYPASSRLS"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = owner.Exec(context.Background(), "DROP ROLE IF EXISTS "+group) })
	for name, tc := range map[string]struct {
		db      DB
		refused bool
	}{
		"a superuser": {owner, true},
		"BYPASSRLS":   {loginRole(t, bypassRole, "BYPASSRLS"), true},
		"a member of a BYPASSRLS role": {loginRole(t, bypassMember, "",
			"GRANT "+group+" TO "+pgx.Identifier{bypassMember}.Sanitize()), true},
		"the owner": {migrator, true},
		"a member of the owner": {loginRole(t, ownerMemberRole, "",
			"GRANT "+pgx.Identifier{migratorUser}.Sanitize()+" TO "+pgx.Identifier{ownerMemberRole}.Sanitize()), true},
		"an owner of a function in the schema": {loginRole(t, objectOwnerRole, "",
			"CREATE FUNCTION "+probeFunction+" RETURNS integer LANGUAGE sql AS 'SELECT 1'",
			"ALTER FUNCTION "+probeFunction+" OWNER TO "+pgx.Identifier{objectOwnerRole}.Sanitize()), true},
		"the runtime role": {app, false},
	} {
		err := CheckSchema(ctx, tc.db)
		if tc.refused != errors.Is(err, ErrRuntimeRole) || !tc.refused && err != nil {
			t.Errorf("CheckSchema as %s: err = %v, want refused: %v", name, err, tc.refused)
		}
	}
}

// sqlSchemaFunctions lists every function of schema $1 by its signature.
const sqlSchemaFunctions = `SELECT p.oid::regprocedure::text FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace WHERE n.nspname = $1`

// sqlMayCall is whether role $1 may call the schema's function $2.
const sqlMayCall = `SELECT has_function_privilege($1, $2, 'EXECUTE')`

// The schema's functions — every one of them, so that one a later migration
// adds without revoking it is caught — are the runtime roles' to call
// alone: a role that reaches the schema but was not granted by Migrate cannot
// ask whether any tenant's capability is revoked.
func TestOnlyRuntimeRolesCallTheFunctions(t *testing.T) {
	const outsider = "limes_outsider"
	loginRole(t, outsider, "")
	rows, err := owner.Query(t.Context(), sqlSchemaFunctions, schema)
	if err != nil {
		t.Fatal(err)
	}
	functions, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil || len(functions) == 0 {
		t.Fatalf("the schema's functions = %v, %v; want some", functions, err)
	}
	for _, fn := range functions {
		for role, want := range map[string]bool{appUser: true, outsider: false} {
			var may bool
			if err := owner.QueryRow(t.Context(), sqlMayCall, role, fn).Scan(&may); err != nil {
				t.Fatal(err)
			}
			if may != want {
				t.Errorf("%s may call %s: %v, want %v", role, fn, may, want)
			}
		}
	}
}

// A session opened as a role the policies do not hold, that SET ROLE or SET
// SESSION AUTHORIZATION to the runtime role, is refused all the same: RESET
// takes it back.
func TestCheckSchemaRefusesASessionOfAnotherRole(t *testing.T) {
	ctx := t.Context()
	for set, reset := range map[string]string{
		"SET ROLE ":                  "RESET ROLE",
		"SET SESSION AUTHORIZATION ": "RESET SESSION AUTHORIZATION",
	} {
		err := owner.AcquireFunc(ctx, func(c *pgxpool.Conn) error {
			if _, err := c.Exec(ctx, set+pgx.Identifier{appUser}.Sanitize()); err != nil {
				return err
			}
			defer func() { _, _ = c.Exec(context.WithoutCancel(ctx), reset) }()
			if err := CheckSchema(ctx, c.Conn()); !errors.Is(err, ErrRuntimeRole) {
				t.Errorf("CheckSchema after %q the runtime role, in a superuser's session: err = %v, want ErrRuntimeRole", strings.TrimSpace(set), err)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
