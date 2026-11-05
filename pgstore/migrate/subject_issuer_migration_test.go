package migrate

import (
	"context"
	"io/fs"
	"slices"
	"testing"
	"uuid"

	"github.com/jackc/pgx/v5"
	tern "github.com/jackc/tern/v2/migrate"

	"github.com/oleg-tkachuk/limes/pgstore/internal/pgschema"
)

// lastBeforeSubjectIssuer is the schema version just before migration 039,
// which records the subject token's issuer a bulk revocation selects by.
const lastBeforeSubjectIssuer int32 = 38

// exchangedIssuer is the subject token's issuer the seeded exchange
// recorded.
const exchangedIssuer = "https://idp.example"

const (
	sqlSeedExchangeOf = `INSERT INTO limes.exchanges (tenant_id, issuer, token_id, capability_id, token_expires_at)
VALUES ($1, $2, $3, $4, now())`
	sqlReadSubjectIssuer = `SELECT subject_issuer FROM limes.capabilities WHERE id = $1`
	// sqlSubjectIssuerIndexValid is whether migration 040's index is built
	// and valid.
	sqlSubjectIssuerIndexValid = `
SELECT EXISTS (SELECT 1 FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
WHERE c.relname = 'capabilities_subject_issuer_tenant' AND i.indisvalid)`
)

// The migration fills in the subject issuer an exchange recorded, for the
// capability it issued and those delegated from it, so a bulk revocation
// by subject issuer reaches records issued before it; a record no exchange
// issued keeps NULL; a record that is its own parent, which no insert
// writes, ends the walk rather than looping it. The lineage view gains the
// column, appended, and 040's index is built.
func TestSubjectIssuerFillsTheExchangedRecords(t *testing.T) {
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
	if err := migrateAllTenants(ctx, conn, func(ctx context.Context) error { return m.MigrateTo(ctx, lastBeforeSubjectIssuer) }); err != nil {
		t.Fatal(err)
	}
	tenant := uuid.New()
	exchanged, child, grandchild, issued, looped := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{sqlSetSession, []any{pgschema.SettingAllTenants, pgschema.SettingAllTenantsOn}},
		{sqlSeedCapability, []any{exchanged, nil, tenant, 0}},
		{sqlSeedCapability, []any{child, exchanged, tenant, 1}},
		{sqlSeedCapability, []any{grandchild, child, tenant, 2}},
		{sqlSeedCapability, []any{issued, nil, tenant, 0}},
		{sqlSeedCapability, []any{looped, looped, tenant, 0}},
		{sqlSeedExchangeOf, []any{tenant, exchangedIssuer, "jti-1", exchanged}},
		{sqlSeedExchangeOf, []any{tenant, exchangedIssuer, "jti-2", looped}},
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
	want := exchangedIssuer
	for id, want := range map[uuid.UUID]*string{exchanged: &want, child: &want, grandchild: &want, looped: &want, issued: nil} {
		var got *string
		if err := conn.QueryRow(ctx, sqlReadSubjectIssuer, id).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if (got == nil) != (want == nil) || got != nil && *got != *want {
			t.Errorf("%s subject_issuer = %v, want %v", id, got, want)
		}
	}
	rows, err := conn.Query(ctx, sqlViewColumns, schema, lineageViewName)
	if err != nil {
		t.Fatal(err)
	}
	columns, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	wantColumns := []string{
		"id", "parent_id", "tenant_id", "expires_at", "requests", "budget", "copies",
		"revoked", "subject", "on_behalf_of", "client_id", "session", "run_id", "subject_issuer",
	}
	if !slices.Equal(columns, wantColumns) {
		t.Errorf("%s columns = %v, want %v", lineageViewName, columns, wantColumns)
	}
	var valid bool
	if err := conn.QueryRow(ctx, sqlSubjectIssuerIndexValid).Scan(&valid); err != nil || !valid {
		t.Errorf("040's index valid = %v, %v; want valid", valid, err)
	}
}
