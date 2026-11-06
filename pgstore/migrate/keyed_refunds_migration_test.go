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

// lastBeforeKeyedRefunds is the schema version just before migration 041,
// which records the refunds made under a RefundID.
const lastBeforeKeyedRefunds int32 = 40

// The charge is seeded with sqlSeedCharge, of counters_born_migration_test.
const (
	sqlSeedRefund = `
INSERT INTO limes.refunds (id, charge_id, tenant_id, amount, remaining, refunded, created_at)
VALUES ($1, $2, $3, 1, false, 1, $4)`
	sqlCountRefunds = `SELECT count(*) FROM limes.refunds WHERE charge_id = $1`
	sqlDropCharge   = `DELETE FROM limes.charges WHERE id = $1`
	// sqlRefundsForceRLS is whether row-level security binds limes.refunds,
	// its owner too, as it binds limes.charges.
	sqlRefundsForceRLS = `
SELECT c.relrowsecurity AND c.relforcerowsecurity FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = 'limes' AND c.relname = 'refunds'`
)

// A store at version 40, holding a charge, migrates past 041: a refund of
// the charge is recorded under row-level security its owner is bound by too,
// and goes with the charge when the charge is removed.
func TestMigrationToKeyedRefunds(t *testing.T) {
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
	if err := migrateAllTenants(ctx, conn, func(ctx context.Context) error { return m.MigrateTo(ctx, lastBeforeKeyedRefunds) }); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, sqlSetSession, pgschema.SettingAllTenants, pgschema.SettingAllTenantsOn); err != nil {
		t.Fatal(err)
	}
	charge, tenant := uuid.New(), uuid.New()
	if _, err := conn.Exec(ctx, sqlSeedCharge, charge, uuid.New(), tenant); err != nil {
		t.Fatal(err)
	}
	if err := runMigrations(ctx, conn, m); err != nil {
		t.Fatalf("migrate past 041: %v", err)
	}
	if _, err := conn.Exec(ctx, sqlSetSession, pgschema.SettingAllTenants, pgschema.SettingAllTenantsOn); err != nil {
		t.Fatal(err)
	}
	var forced bool
	if err := conn.QueryRow(ctx, sqlRefundsForceRLS).Scan(&forced); err != nil || !forced {
		t.Errorf("row-level security forced on limes.refunds = %v, %v; want forced", forced, err)
	}
	if _, err := conn.Exec(ctx, sqlSeedRefund, uuid.New(), charge, tenant, time.Now()); err != nil {
		t.Fatalf("a refund of a charge on record: %v", err)
	}
	if _, err := conn.Exec(ctx, sqlSeedRefund, uuid.New(), uuid.New(), tenant, time.Now()); err == nil {
		t.Errorf("a refund of no charge on record was recorded")
	}
	if _, err := conn.Exec(ctx, sqlDropCharge, charge); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := conn.QueryRow(ctx, sqlCountRefunds, charge).Scan(&left); err != nil || left != 0 {
		t.Errorf("refunds of a removed charge = %d, %v; want none", left, err)
	}
}
