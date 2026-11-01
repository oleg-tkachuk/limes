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

// lastBeforeCountersBorn is the schema version just before migration 037,
// which numbers counter rows and charges from one sequence.
const lastBeforeCountersBorn int32 = 36

const (
	sqlSeedCharge = `
INSERT INTO limes.charges (id, capability_id, tenant_id, amount, unit_code, op, actor, at, ancestors, copies, overrun, tenant_period)
VALUES ($1, $2, $3, 1, 'USD', 'get', 'actor', now(), '{}', '{}', false, 0)`
	sqlUsageBorn = `SELECT born FROM limes.usage WHERE capability_id = $1`
	sqlCopyBorn  = `SELECT born FROM limes.copy_usage WHERE revocation_id = $1`
	sqlChargeSeq = `SELECT seq FROM limes.charges WHERE id = $1`
)

// The migration takes the counters and charges already on record as older
// than everything to come, the counters older than the charges, so a
// refund of a charge already taken still returns to them; a row created
// since is younger than every charge before it and older than every one
// after.
func TestCountersAreBornBeforeTheirCharges(t *testing.T) {
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
	if err := migrateAllTenants(ctx, conn, func(ctx context.Context) error { return m.MigrateTo(ctx, lastBeforeCountersBorn) }); err != nil {
		t.Fatal(err)
	}
	tenant, old, oldCharge := uuid.New(), uuid.New(), uuid.New()
	oldCopy := []byte("copy-before")
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := conn.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(sqlSetSession, pgschema.SettingAllTenants, pgschema.SettingAllTenantsOn)
	exec(sqlSeedUsage, old, tenant)
	exec(sqlSeedCopy, oldCopy, old, tenant)
	exec(sqlSeedCharge, oldCharge, old, tenant)
	if err := runMigrations(ctx, conn, m); err != nil {
		t.Fatal(err)
	}
	exec(sqlSetSession, pgschema.SettingAllTenants, pgschema.SettingAllTenantsOn)
	read := func(sql string, arg any) int64 {
		t.Helper()
		var n int64
		if err := conn.QueryRow(ctx, sql, arg).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	oldBorn, oldCopyBorn, oldSeq := read(sqlUsageBorn, old), read(sqlCopyBorn, oldCopy), read(sqlChargeSeq, oldCharge)
	if oldBorn >= oldSeq || oldCopyBorn >= oldSeq {
		t.Errorf("a counter on record born %d, a copy %d, a charge on record %d; want both counters before the charge", oldBorn, oldCopyBorn, oldSeq)
	}
	fresh, newCharge := uuid.New(), uuid.New()
	newCopy := []byte("copy-after")
	exec(sqlSeedUsage, fresh, tenant)
	exec(sqlSeedCopy, newCopy, fresh, tenant)
	exec(sqlSeedCharge, newCharge, fresh, tenant)
	born, copyBorn, seq := read(sqlUsageBorn, fresh), read(sqlCopyBorn, newCopy), read(sqlChargeSeq, newCharge)
	if born <= oldSeq || copyBorn <= oldSeq {
		t.Errorf("a counter created since born %d, a copy %d; want both after the charge on record, %d", born, copyBorn, oldSeq)
	}
	if seq <= born || seq <= copyBorn {
		t.Errorf("a charge taken since numbered %d; want it after the counters created before it, %d and %d", seq, born, copyBorn)
	}
}
