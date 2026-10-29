package migrate

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/oleg-tkachuk/limes/pgstore/internal/pgschema"
)

// settingAllTenantsRead is the scope a session holds before a run, which
// the run must hand back: pgstore's read-only scope of every tenant.
const settingAllTenantsRead = "read"

// sessionScope is the session's scope of every tenant.
func sessionScope(t *testing.T, conn *pgx.Conn) string {
	t.Helper()
	var scope string
	if err := conn.QueryRow(context.WithoutCancel(t.Context()), sqlSettingNow, pgschema.SettingAllTenants).Scan(&scope); err != nil {
		t.Fatal(err)
	}
	return scope
}

// Run scopes its run to every tenant and hands the session back with the
// scope it had.
func TestRunRestoresTheSessionScope(t *testing.T) {
	conn := freshConn(t)
	ctx := t.Context()
	if _, err := conn.Exec(ctx, sqlSetSession, pgschema.SettingAllTenants, settingAllTenantsRead); err != nil {
		t.Fatal(err)
	}
	if err := Run(ctx, conn, Options{}); err != nil {
		t.Fatal(err)
	}
	if scope := sessionScope(t, conn); scope != settingAllTenantsRead {
		t.Errorf("the session's scope after Run = %q, want %q as before", scope, settingAllTenantsRead)
	}
}

// A run whose context ends before Run puts the session's scope back still
// puts it back: the connection is the caller's, and a pool would hand it on
// scoped to every tenant.
func TestRunRestoresTheScopeWhenTheContextEnds(t *testing.T) {
	conn := freshConn(t)
	if _, err := conn.Exec(t.Context(), sqlSetSession, pgschema.SettingAllTenants, settingAllTenantsRead); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	cancelled := func(context.Context) error { cancel(); return nil }
	if err := migrateAllTenants(ctx, conn, cancelled); err != nil {
		t.Fatal(err)
	}
	if scope := sessionScope(t, conn); scope != settingAllTenantsRead {
		t.Errorf("the session's scope after a run its context ended in = %q, want %q as before", scope, settingAllTenantsRead)
	}
}
