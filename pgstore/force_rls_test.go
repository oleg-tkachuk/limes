package pgstore

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// rlsTables are the tables row-level security guards.
var rlsTables = []string{
	"capabilities", "revocations", "biscuit_revocations", "usage", "copy_usage",
	"tenant_budgets", "charges", "reservations", "erased_tenants",
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
	err := pgx.BeginFunc(t.Context(), app, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), sqlSetLocal, SettingAllTenants, settingUnset); err != nil {
			return err
		}
		var self, copyRevoked bool
		var scope string
		if err := tx.QueryRow(t.Context(), sqlCallerScopeAfterChecks, f.parent.ID, []byte("any")).Scan(&self, &copyRevoked, &scope); err != nil {
			return err
		}
		if scope != settingUnset {
			t.Errorf("the caller's scope after the checks = %q, want it unset as before", scope)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// bypassRole is a role row-level security never binds, short of superuser.
const (
	bypassRole     = "limes_bypass"
	bypassPassword = "limes-bypass-test"
)

// CheckSchema refuses a store served by a role the policies do not hold —
// a superuser, one with BYPASSRLS, the schema's owner — and accepts one that
// owns nothing.
func TestCheckSchemaRefusesARoleTheyDoNotHold(t *testing.T) {
	ctx := t.Context()
	role := pgx.Identifier{bypassRole}.Sanitize()
	if _, err := owner.Exec(ctx, "DROP ROLE IF EXISTS "+role); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, "CREATE ROLE "+role+" LOGIN BYPASSRLS PASSWORD '"+bypassPassword+"'"); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, "GRANT USAGE ON SCHEMA "+schema+" TO "+role+"; GRANT SELECT ON "+versionTable+" TO "+role); err != nil {
		t.Fatal(err)
	}
	bypass, err := poolAs(ctx, ownerDSN, bypassRole, bypassPassword)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		bypass.Close()
		_, _ = owner.Exec(context.Background(), "DROP OWNED BY "+role+"; DROP ROLE IF EXISTS "+role)
	})
	for name, tc := range map[string]struct {
		db      DB
		refused bool
	}{
		"a superuser":      {owner, true},
		"BYPASSRLS":        {bypass, true},
		"the owner":        {migrator, true},
		"the runtime role": {app, false},
	} {
		err := CheckSchema(ctx, tc.db)
		if tc.refused != errors.Is(err, ErrRuntimeRole) || !tc.refused && err != nil {
			t.Errorf("CheckSchema as %s: err = %v, want refused: %v", name, err, tc.refused)
		}
	}
}

// Migrate scopes its run to every tenant and hands the session back with the
// scope it had.
func TestMigrateRestoresTheSessionScope(t *testing.T) {
	ctx := t.Context()
	err := migrator.AcquireFunc(ctx, func(c *pgxpool.Conn) error {
		if _, err := c.Exec(ctx, sqlSetSession, SettingAllTenants, SettingAllTenantsRead); err != nil {
			return err
		}
		if err := Migrate(ctx, c.Conn(), MigrateOptions{}); err != nil {
			return err
		}
		var scope string
		if err := c.QueryRow(ctx, sqlSettingNow, SettingAllTenants).Scan(&scope); err != nil {
			return err
		}
		if scope != SettingAllTenantsRead {
			t.Errorf("the session's scope after Migrate = %q, want %q as before", scope, SettingAllTenantsRead)
		}
		_, err := c.Exec(ctx, sqlSetSession, SettingAllTenants, settingUnset)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

// A run whose context ends before Migrate puts the session's scope back
// still puts it back: the connection is the caller's, and a pool would hand
// it on scoped to every tenant.
func TestMigrateRestoresTheScopeWhenTheContextEnds(t *testing.T) {
	err := migrator.AcquireFunc(t.Context(), func(c *pgxpool.Conn) error {
		if _, err := c.Exec(t.Context(), sqlSetSession, SettingAllTenants, SettingAllTenantsRead); err != nil {
			return err
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		cancelled := func(context.Context) error { cancel(); return nil }
		if err := migrateAllTenants(ctx, c.Conn(), cancelled); err != nil {
			return err
		}
		var scope string
		if err := c.QueryRow(t.Context(), sqlSettingNow, SettingAllTenants).Scan(&scope); err != nil {
			return err
		}
		if scope != SettingAllTenantsRead {
			t.Errorf("the session's scope after a run its context ended in = %q, want %q as before", scope, SettingAllTenantsRead)
		}
		_, err := c.Exec(t.Context(), sqlSetSession, SettingAllTenants, settingUnset)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}
