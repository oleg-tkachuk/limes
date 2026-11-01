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

// lastBeforeLeaseExpiryIndex is the schema version just before
// migration 033, which indexes the leases by their end.
const lastBeforeLeaseExpiryIndex int32 = 32

// sqlLeaseExpiryIndexValid counts migration 033's index if it is built
// and valid: a CONCURRENTLY build that failed leaves it invalid.
const sqlLeaseExpiryIndexValid = `
SELECT count(*) FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
WHERE c.relname = 'usage_lease_expires' AND i.indisvalid`

// A store at version 32, holding records, migrates past 033 — which runs
// outside a transaction, a statement at a time — with its index built and
// valid.
func TestMigrationToTheLeaseExpiryIndex(t *testing.T) {
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
	if err := migrateAllTenants(ctx, conn, func(ctx context.Context) error { return m.MigrateTo(ctx, lastBeforeLeaseExpiryIndex) }); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, sqlSetSession, pgschema.SettingAllTenants, pgschema.SettingAllTenantsOn); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, sqlSeedCapability, uuid.New(), nil, uuid.New(), 0); err != nil {
		t.Fatal(err)
	}
	if err := runMigrations(ctx, conn, m); err != nil {
		t.Fatalf("migrate past 033: %v", err)
	}
	var valid int
	if err := conn.QueryRow(ctx, sqlLeaseExpiryIndexValid).Scan(&valid); err != nil {
		t.Fatal(err)
	}
	if valid != 1 {
		t.Errorf("033's index valid %d times, want once", valid)
	}
}
