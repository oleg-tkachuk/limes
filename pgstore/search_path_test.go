package pgstore

import (
	"context"
	"testing"
	"uuid"

	"github.com/jackc/pgx/v5"
)

// shadowSchema holds look-alikes of the catalog functions and operators the
// policy functions call, put first on the search path.
const shadowSchema = "limes_shadow"

// sqlShadow defines a current_setting that says every tenant is in scope.
const sqlShadow = `
CREATE SCHEMA ` + shadowSchema + `;
CREATE FUNCTION ` + shadowSchema + `.current_setting(text, boolean) RETURNS text
LANGUAGE sql AS $$ SELECT 'on' $$`

// The policy functions resolve the catalog's own functions, operators and
// types whatever the session's search path, so a schema put ahead of
// pg_catalog cannot answer for them and widen a scope.
func TestPolicyFunctionsIgnoreTheSearchPath(t *testing.T) {
	ctx := context.Background()
	conn, err := owner.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	// No scope of the session's own: only the shadow function could then
	// admit a tenant, whatever an earlier user of the connection set.
	if _, err := conn.Exec(ctx, sqlSetSession, SettingAllTenants, settingUnset); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, sqlSetSession, SettingTenant, settingUnset); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, sqlShadow); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = owner.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+shadowSchema+" CASCADE")
	})
	if _, err := conn.Exec(ctx, "SET search_path = "+shadowSchema+", pg_catalog"); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = conn.Exec(context.Background(), "RESET search_path") }()
	for _, fn := range []string{"tenant_visible", "tenant_writable"} {
		var admitted bool
		// NULL, as with no tenant set, admits no row: a policy reads it as false.
		q := "SELECT coalesce(" + pgx.Identifier{schema, fn}.Sanitize() + "($1), false)"
		if err := conn.QueryRow(ctx, q, uuid.New()).Scan(&admitted); err != nil {
			t.Fatal(err)
		}
		if admitted {
			t.Errorf("%s admits a tenant out of scope once %s.current_setting comes first", fn, shadowSchema)
		}
	}
	// The trigger function runs plpgsql, which no planner inlines: its
	// search path is pinned instead.
	var pinned bool
	if err := conn.QueryRow(ctx, `SELECT coalesce(array_to_string(proconfig, ',') LIKE '%search_path=%', false)
FROM pg_proc WHERE oid = 'limes.notify_revocation'::regproc`).Scan(&pinned); err != nil {
		t.Fatal(err)
	}
	if !pinned {
		t.Error("limes.notify_revocation runs with the caller's search path")
	}
}
