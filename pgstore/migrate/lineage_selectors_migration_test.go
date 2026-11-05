package migrate

import (
	"io/fs"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"
	tern "github.com/jackc/tern/v2/migrate"
)

const (
	// lineageViewName is the view the meter reads the records through.
	lineageViewName = "capability_lineage"
	// sqlViewColumns are the columns of view $2 of schema $1, in order.
	sqlViewColumns = `
SELECT column_name FROM information_schema.columns
WHERE table_schema = $1 AND table_name = $2 ORDER BY ordinal_position`
)

// Migration 038 appends to the lineage view what ReleaseMatching selects
// by, and keeps the columns 022 gave it where they were: a query of the
// view written before reads what it read.
func TestLineageSelectorsAppendToTheView(t *testing.T) {
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
	if err := runMigrations(ctx, conn, m); err != nil {
		t.Fatal(err)
	}
	rows, err := conn.Query(ctx, sqlViewColumns, schema, lineageViewName)
	if err != nil {
		t.Fatal(err)
	}
	got, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"id", "parent_id", "tenant_id", "expires_at", "requests", "budget", "copies",
		"revoked", "subject", "on_behalf_of", "client_id", "session", "run_id",
	}
	// A later step may append more (039), never move these.
	if len(got) < len(want) || !slices.Equal(got[:len(want)], want) {
		t.Errorf("%s columns = %v, want them to begin %v", lineageViewName, got, want)
	}
}
