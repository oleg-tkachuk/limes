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

// lastBeforeRevokeByRun is the schema version just before migration 029,
// which adds the agent run a bulk revocation selects by.
const lastBeforeRevokeByRun int32 = 28

const (
	sqlReadRun = `SELECT run_id FROM limes.capabilities WHERE id = $1`
	// sqlRunIndexValid is whether migration 030's index is built and valid.
	sqlRunIndexValid = `
SELECT EXISTS (SELECT 1 FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
WHERE c.relname = 'capabilities_tenant_run' AND i.indisvalid)`
)

// The migration fills in each record's agent run from its document, so a
// bulk revocation by run reaches records issued before it; a record of no
// agent, or of an agent naming no run, keeps NULL. 030's index is built.
func TestRevokeByRunFillsTheRecordsAlreadyThere(t *testing.T) {
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
	if err := migrateAllTenants(ctx, conn, func(ctx context.Context) error { return m.MigrateTo(ctx, lastBeforeRevokeByRun) }); err != nil {
		t.Fatal(err)
	}
	tenant, run := uuid.New(), uuid.New()
	withRun, noRun, noAgent := uuid.New(), uuid.New(), uuid.New()
	for id, doc := range map[uuid.UUID]string{
		withRun: `{"subject": {"agent": {"run_id": "` + run.String() + `"}}}`,
		noRun:   `{"subject": {"agent": {"run_id": "00000000-0000-0000-0000-000000000000"}}}`,
		noAgent: `{"subject": {"subject": "user"}}`,
	} {
		if _, err := conn.Exec(ctx, sqlSetSession, pgschema.SettingAllTenants, pgschema.SettingAllTenantsOn); err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(ctx, sqlSeedDocument, id, tenant, doc); err != nil {
			t.Fatal(err)
		}
	}
	if err := runMigrations(ctx, conn, m); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, sqlSetSession, pgschema.SettingAllTenants, pgschema.SettingAllTenantsOn); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[uuid.UUID]*uuid.UUID{withRun: &run, noRun: nil, noAgent: nil} {
		var got *uuid.UUID
		if err := conn.QueryRow(ctx, sqlReadRun, id).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if (got == nil) != (want == nil) || got != nil && *got != *want {
			t.Errorf("%s run_id = %v, want %v", id, got, want)
		}
	}
	var valid bool
	if err := conn.QueryRow(ctx, sqlRunIndexValid).Scan(&valid); err != nil || !valid {
		t.Errorf("030's index valid = %v, %v; want valid", valid, err)
	}
}
