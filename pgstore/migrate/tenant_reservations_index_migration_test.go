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

// lastBeforeTenantReservationsIndex is the schema version just before
// migration 031, which indexes a tenant's holds.
const lastBeforeTenantReservationsIndex int32 = 30

// sqlTenantReservationsIndexValid counts migration 031's index if it is
// built and valid: a CONCURRENTLY build that failed leaves it invalid.
const sqlTenantReservationsIndexValid = `
SELECT count(*) FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
WHERE c.relname = 'reservations_tenant' AND i.indisvalid`

// A store at version 30, holding records, migrates past 031 — which runs
// outside a transaction, a statement at a time — with its index built and
// valid.
func TestMigrationToTheTenantReservationsIndex(t *testing.T) {
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
	if err := migrateAllTenants(ctx, conn, func(ctx context.Context) error { return m.MigrateTo(ctx, lastBeforeTenantReservationsIndex) }); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, sqlSetSession, pgschema.SettingAllTenants, pgschema.SettingAllTenantsOn); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, sqlSeedCapability, uuid.New(), nil, uuid.New(), 0); err != nil {
		t.Fatal(err)
	}
	if err := runMigrations(ctx, conn, m); err != nil {
		t.Fatalf("migrate past 031: %v", err)
	}
	var valid int
	if err := conn.QueryRow(ctx, sqlTenantReservationsIndexValid).Scan(&valid); err != nil {
		t.Fatal(err)
	}
	if valid != 1 {
		t.Errorf("031's index valid %d times, want once", valid)
	}
}
