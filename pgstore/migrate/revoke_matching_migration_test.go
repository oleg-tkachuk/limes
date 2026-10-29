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

// lastBeforeRevokeMatching is the schema version just before the migration
// that adds the columns a bulk revocation selects by.
const lastBeforeRevokeMatching int32 = 24

const (
	sqlSeedDocument = `
INSERT INTO limes.capabilities (id, parent_id, tenant_id, principal_type, subject, depth, capability, issued_by)
VALUES ($1, NULL, $2, 'agent', 'agent', 0, $3, '{}')`
	sqlReadSelectors = `SELECT client_id, on_behalf_of, session FROM limes.capabilities WHERE id = $1`
)

// The migration fills in the client and the principal acted for from each
// record's document, so a bulk revocation by them reaches records issued
// before it; a record with neither keeps NULL, and none has a session.
func TestRevokeMatchingFillsTheRecordsAlreadyThere(t *testing.T) {
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
	if err := migrateAllTenants(ctx, conn, func(ctx context.Context) error { return m.MigrateTo(ctx, lastBeforeRevokeMatching) }); err != nil {
		t.Fatal(err)
	}
	tenant, both, bare := uuid.New(), uuid.New(), uuid.New()
	for _, seed := range []struct {
		id  uuid.UUID
		doc string
	}{
		{both, `{"client_id": "client-1", "on_behalf_of": {"subject": "user-1"}}`},
		{bare, `{"client_id": ""}`},
	} {
		if _, err := conn.Exec(ctx, sqlSetSession, pgschema.SettingAllTenants, pgschema.SettingAllTenantsOn); err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(ctx, sqlSeedDocument, seed.id, tenant, seed.doc); err != nil {
			t.Fatal(err)
		}
	}
	if err := runMigrations(ctx, conn, m); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, sqlSetSession, pgschema.SettingAllTenants, pgschema.SettingAllTenantsOn); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[uuid.UUID][3]*string{both: {new("client-1"), new("user-1"), nil}, bare: {nil, nil, nil}} {
		var got [3]*string
		if err := conn.QueryRow(ctx, sqlReadSelectors, id).Scan(&got[0], &got[1], &got[2]); err != nil {
			t.Fatal(err)
		}
		for i := range got {
			if (got[i] == nil) != (want[i] == nil) || got[i] != nil && *got[i] != *want[i] {
				t.Errorf("%s column %d = %v, want %v", id, i, deref(got[i]), deref(want[i]))
			}
		}
	}
}

// deref is *s, or "<nil>".
func deref(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}
