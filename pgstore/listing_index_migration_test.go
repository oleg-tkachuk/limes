package pgstore

import (
	"context"
	"io/fs"
	"testing"
	"uuid"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/tern/v2/migrate"
)

// lastBeforeListingIndexes is the schema version just before migration 014,
// which indexes the records a listing pages.
const lastBeforeListingIndexes int32 = 13

// sqlListingIndexesValid counts migration 014's indexes that are built and
// valid: a CONCURRENTLY build that failed leaves one invalid.
const sqlListingIndexesValid = `
SELECT count(*) FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
WHERE c.relname IN ('capabilities_tenant_id', 'capabilities_tenant_type_id', 'revocations_tenant') AND i.indisvalid`

// listingIndexes is how many indexes migration 014 builds.
const listingIndexes = 3

// A store at version 13, holding records, migrates on past 014 — which runs
// outside a transaction, a statement at a time — to the latest version,
// with each of 014's indexes built and valid.
func TestMigrationFromBeforeTheListingIndexes(t *testing.T) {
	conn := freshConn(t)
	ctx := t.Context()
	if _, err := conn.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	m, err := migrate.NewMigrator(ctx, conn, versionTable)
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
	if err := migrateAllTenants(ctx, conn, func(ctx context.Context) error { return m.MigrateTo(ctx, lastBeforeListingIndexes) }); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, sqlSetSession, SettingAllTenants, SettingAllTenantsOn); err != nil {
		t.Fatal(err)
	}
	tenant, root := uuid.New(), uuid.New()
	if _, err := conn.Exec(ctx, sqlSeedCapability, root, nil, tenant, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, sqlSeedCapability, uuid.New(), root, tenant, 1); err != nil {
		t.Fatal(err)
	}
	if err := runMigrations(ctx, conn, m); err != nil {
		t.Fatalf("migrate past 014: %v", err)
	}
	var valid int
	if err := conn.QueryRow(ctx, sqlListingIndexesValid).Scan(&valid); err != nil {
		t.Fatal(err)
	}
	if valid != listingIndexes {
		t.Errorf("%d of 014's indexes valid, want %d", valid, listingIndexes)
	}
}
