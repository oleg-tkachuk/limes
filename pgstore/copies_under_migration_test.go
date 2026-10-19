package pgstore

import (
	"context"
	"io/fs"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/tern/v2/migrate"
)

// lastBeforeCopiesUnder is the schema version just before the migration
// that adds limes.usage.copies_under.
const lastBeforeCopiesUnder int32 = 12

const (
	sqlSeedCapability = `
INSERT INTO limes.capabilities (id, parent_id, tenant_id, principal_type, subject, depth, capability, issued_by)
VALUES ($1, $2, $3, 'agent', 'agent', $4, '{}', '{}')`
	sqlSeedUsage = `INSERT INTO limes.usage (capability_id, tenant_id) VALUES ($1, $2)`
	sqlSeedCopy  = `INSERT INTO limes.copy_usage (revocation_id, capability_id, tenant_id) VALUES ($1, $2, $3)`
	sqlReadUnder = `SELECT copies_under FROM limes.usage WHERE capability_id = $1`
)

// beforeCopiesUnder is a fresh database migrated to just before the
// migration under test, and the migrator that runs the rest.
func beforeCopiesUnder(t *testing.T) (*pgx.Conn, *migrate.Migrator) {
	t.Helper()
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
	if err := migrateAllTenants(ctx, conn, func(ctx context.Context) error { return m.MigrateTo(ctx, lastBeforeCopiesUnder) }); err != nil {
		t.Fatal(err)
	}
	return conn, m
}

// The migration that adds the count of copies under each capability counts
// the copies already on record: each under its capability and every
// ancestor, so a quota set before the upgrade binds as it did.
func TestCopiesUnderCountsTheCopiesAlreadyRecorded(t *testing.T) {
	conn, m := beforeCopiesUnder(t)
	ctx := t.Context()
	tenant, root, child, grandchild := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	seed := func(sql string, args ...any) {
		t.Helper()
		if _, err := conn.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	seed(sqlSetSession, SettingAllTenants, SettingAllTenantsOn)
	seed(sqlSeedCapability, root, nil, tenant, 0)
	seed(sqlSeedCapability, child, root, tenant, 1)
	seed(sqlSeedCapability, grandchild, child, tenant, 2)
	for _, id := range []uuid.UUID{root, child, grandchild} {
		seed(sqlSeedUsage, id, tenant)
	}
	copiesOf := map[uuid.UUID]int{root: 1, child: 2, grandchild: 3}
	for id, n := range copiesOf {
		for i := range n {
			seed(sqlSeedCopy, []byte{byte(i + 1)}, id, tenant)
		}
	}

	if err := runMigrations(ctx, conn, m); err != nil {
		t.Fatal(err)
	}
	seed(sqlSetSession, SettingAllTenants, SettingAllTenantsOn)
	for id, want := range map[uuid.UUID]int64{root: 6, child: 5, grandchild: 3} {
		var got int64
		if err := conn.QueryRow(ctx, sqlReadUnder, id).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("copies_under of %s = %d, want %d", id, got, want)
		}
	}
}

// migrationDeadline bounds a migration a test expects to finish at once.
const migrationDeadline = 30 * time.Second

// A capability recorded by hand as its own parent, which the store never
// records, does not keep the count from finishing: it counts its copies
// once.
func TestCopiesUnderEndsOnAParentWrittenByHand(t *testing.T) {
	conn, m := beforeCopiesUnder(t)
	ctx, cancel := context.WithTimeout(t.Context(), migrationDeadline)
	defer cancel()
	tenant, looped := uuid.New(), uuid.New()
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{sqlSetSession, []any{SettingAllTenants, SettingAllTenantsOn}},
		{sqlSeedCapability, []any{looped, looped, tenant, 0}},
		{sqlSeedUsage, []any{looped, tenant}},
		{sqlSeedCopy, []any{[]byte{1}, looped, tenant}},
	} {
		if _, err := conn.Exec(ctx, q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := runMigrations(ctx, conn, m); err != nil {
		t.Fatalf("the migration over a capability that is its own parent: %v", err)
	}
	if _, err := conn.Exec(ctx, sqlSetSession, SettingAllTenants, SettingAllTenantsOn); err != nil {
		t.Fatal(err)
	}
	var got int64
	if err := conn.QueryRow(ctx, sqlReadUnder, looped).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != 1 {
		t.Errorf("copies_under of a capability that is its own parent = %d, want 1", got)
	}
}
