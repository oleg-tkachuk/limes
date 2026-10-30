package migrate

import (
	"context"
	"io/fs"
	"testing"
	"uuid"

	"github.com/jackc/pgx/v5"
	tern "github.com/jackc/tern/v2/migrate"

	"github.com/oleg-tkachuk/limes/pgstore/internal/pgschema"
)

// lastBeforeEveryTenantIndexes is the schema version just before migration
// 027, which indexes a bulk revocation across tenants.
const lastBeforeEveryTenantIndexes int32 = 26

// sqlEveryTenantIndexesValid counts migration 027's indexes that are built
// and valid: a CONCURRENTLY build that failed leaves one invalid.
const sqlEveryTenantIndexesValid = `
SELECT count(*) FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
WHERE c.relname IN ('capabilities_client_tenant', 'capabilities_session_tenant') AND i.indisvalid`

// everyTenantIndexes is how many indexes migration 027 builds.
const everyTenantIndexes = 2

// A store at version 26, holding records, migrates past 027 — which runs
// outside a transaction, a statement at a time — with each of its indexes
// built and valid.
func TestMigrationToTheEveryTenantIndexes(t *testing.T) {
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
	if err := migrateAllTenants(ctx, conn, func(ctx context.Context) error { return m.MigrateTo(ctx, lastBeforeEveryTenantIndexes) }); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, sqlSetSession, pgschema.SettingAllTenants, pgschema.SettingAllTenantsOn); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, sqlSeedCapability, uuid.New(), nil, uuid.New(), 0); err != nil {
		t.Fatal(err)
	}
	if err := runMigrations(ctx, conn, m); err != nil {
		t.Fatalf("migrate past 027: %v", err)
	}
	var valid int
	if err := conn.QueryRow(ctx, sqlEveryTenantIndexesValid).Scan(&valid); err != nil {
		t.Fatal(err)
	}
	if valid != everyTenantIndexes {
		t.Errorf("%d of 027's indexes valid, want %d", valid, everyTenantIndexes)
	}
}
