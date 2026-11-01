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

// lastBeforeExchangesOutlive is the schema version just before migration
// 035, after which an exchange outlives its capability.
const lastBeforeExchangesOutlive int32 = 34

const (
	sqlSetExpiry      = `UPDATE limes.capabilities SET expires_at = $2 WHERE id = $1`
	sqlSeedExchange   = `INSERT INTO limes.exchanges (tenant_id, issuer, token_id, capability_id) VALUES ($1, 'https://idp.example', $2, $3)`
	sqlReadExchange   = `SELECT token_expires_at FROM limes.exchanges WHERE capability_id = $1`
	sqlDropCapability = `DELETE FROM limes.capabilities WHERE id = $1`
	// sqlExpiryIndexValid is whether migration 036's index is built and
	// valid.
	sqlExpiryIndexValid = `
SELECT EXISTS (SELECT 1 FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
WHERE c.relname = 'exchanges_token_expires_at' AND i.indisvalid)`
)

// The migration keeps every exchange already recorded, giving it its
// capability's expiry, and lets it outlive that capability; 036's index is
// built.
func TestExchangesOutliveTheirCapabilities(t *testing.T) {
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
	if err := migrateAllTenants(ctx, conn, func(ctx context.Context) error { return m.MigrateTo(ctx, lastBeforeExchangesOutlive) }); err != nil {
		t.Fatal(err)
	}
	tenant, id := uuid.New(), uuid.New()
	expires := time.Now().Add(time.Hour).UTC().Truncate(time.Microsecond)
	if _, err := conn.Exec(ctx, sqlSetSession, pgschema.SettingAllTenants, pgschema.SettingAllTenantsOn); err != nil {
		t.Fatal(err)
	}
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{sqlSeedDocument, []any{id, tenant, `{"subject": {"subject": "user"}}`}},
		{sqlSetExpiry, []any{id, expires}},
		{sqlSeedExchange, []any{tenant, id.String(), id}},
	} {
		if _, err := conn.Exec(ctx, q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := runMigrations(ctx, conn, m); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, sqlSetSession, pgschema.SettingAllTenants, pgschema.SettingAllTenantsOn); err != nil {
		t.Fatal(err)
	}
	var got time.Time
	if err := conn.QueryRow(ctx, sqlReadExchange, id).Scan(&got); err != nil || !got.Equal(expires) {
		t.Errorf("token_expires_at = %v, %v; want the capability's expiry %v", got, err, expires)
	}
	if _, err := conn.Exec(ctx, sqlDropCapability, id); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(ctx, sqlReadExchange, id).Scan(&got); err != nil {
		t.Errorf("the exchange once its capability went: %v, want it kept", err)
	}
	var valid bool
	if err := conn.QueryRow(ctx, sqlExpiryIndexValid).Scan(&valid); err != nil || !valid {
		t.Errorf("036's index valid = %v, %v; want valid", valid, err)
	}
}
