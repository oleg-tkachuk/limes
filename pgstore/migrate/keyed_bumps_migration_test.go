package migrate

import (
	"context"
	"io/fs"
	"testing"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5"
	tern "github.com/jackc/tern/v2/migrate"

	"github.com/oleg-tkachuk/limes/pgstore/internal/pgschema"
)

// lastBeforeKeyedBumps is the schema version just before migration 044,
// which records the bumps counted under an ExternalRef.
const lastBeforeKeyedBumps int32 = 43

const (
	sqlSeedBump = `
INSERT INTO limes.bumps (capability_id, external_ref, tenant_id, copies, request_count, bumped_at)
VALUES ($1, $2, $3, '{}', $4, $5)`
	sqlCountBumps = `SELECT count(*) FROM limes.bumps WHERE capability_id = $1`
	// sqlBumpsForceRLS is whether row-level security binds limes.bumps, its
	// owner too, as it binds limes.usage.
	sqlBumpsForceRLS = `
SELECT c.relrowsecurity AND c.relforcerowsecurity FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = 'limes' AND c.relname = 'bumps'`
)

// bumpRef is the reference the migration's bumps are counted under.
const bumpRef = "request-1"

// A store at version 43 migrates past 044: a keyed bump is recorded under
// row-level security its owner is bound by too, once per capability and
// reference, and never under no reference or a count of none.
func TestMigrationToKeyedBumps(t *testing.T) {
	conn := freshConn(t)
	ctx := t.Context()
	if _, err := conn.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	m, err := tern.NewMigrator(ctx, conn, versionTable)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := fs.Sub(migrationFiles, migrationsDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.LoadMigrations(dir); err != nil {
		t.Fatal(err)
	}
	if err := migrateAllTenants(ctx, conn, func(ctx context.Context) error { return m.MigrateTo(ctx, lastBeforeKeyedBumps) }); err != nil {
		t.Fatal(err)
	}
	if err := runMigrations(ctx, conn, m); err != nil {
		t.Fatalf("migrate past 044: %v", err)
	}
	if _, err := conn.Exec(ctx, sqlSetSession, pgschema.SettingAllTenants, pgschema.SettingAllTenantsOn); err != nil {
		t.Fatal(err)
	}
	var forced bool
	if err := conn.QueryRow(ctx, sqlBumpsForceRLS).Scan(&forced); err != nil || !forced {
		t.Errorf("row-level security forced on limes.bumps = %v, %v; want forced", forced, err)
	}
	capID, tenant, now := uuid.New(), uuid.New(), time.Now()
	if _, err := conn.Exec(ctx, sqlSeedBump, capID, bumpRef, tenant, 1, now); err != nil {
		t.Fatalf("a keyed bump: %v", err)
	}
	for name, args := range map[string][]any{
		"the same reference again": {capID, bumpRef, tenant, 1, now},
		"no reference":             {capID, "", tenant, 1, now},
		"a count of none":          {capID, "request-2", tenant, 0, now},
	} {
		if _, err := conn.Exec(ctx, sqlSeedBump, args...); err == nil {
			t.Errorf("a bump under %s was recorded", name)
		}
	}
	var n int
	if err := conn.QueryRow(ctx, sqlCountBumps, capID).Scan(&n); err != nil || n != 1 {
		t.Errorf("bumps of the capability = %d, %v; want 1", n, err)
	}
}
