package migrate

import (
	"context"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	tern "github.com/jackc/tern/v2/migrate"
)

// lockedThrough is the last migration that may build an index on a table an
// earlier migration created with a plain CREATE INDEX, which holds the
// table's writes until the index is built. Those migrations are applied in
// the wild and stay as they are. 014 was the last of them until it was
// rewritten to build CONCURRENTLY: the same indexes, so a store that ran it
// before is where it would be now (migrate.go).
const lockedThrough = 13

// disableTx is tern's marker of a migration run outside a transaction, one
// statement at a time, which CREATE INDEX CONCURRENTLY needs.
const disableTx = "---- tern: disable-tx ----"

// migrationFile is one migration: its sequence number and its SQL.
type migrationFile struct {
	seq  int
	name string
	sql  string
}

var (
	lineComment   = regexp.MustCompile(`--[^\n]*`)
	dollarQuoted  = regexp.MustCompile(`(?s)\$\$.*?\$\$`)
	indexInBody   = regexp.MustCompile(`(?i)\bcreate\s+(?:unique\s+)?index\b`)
	createTable   = regexp.MustCompile(`(?i)^create\s+table\s+(?:if\s+not\s+exists\s+)?([\w."]+)`)
	createIndex   = regexp.MustCompile(`(?i)^create\s+(?:unique\s+)?index\s+(concurrently\s+)?(?:if\s+not\s+exists\s+)?([\w"]+)\s+on\s+(?:only\s+)?([\w."]+)`)
	dropIndex     = regexp.MustCompile(`(?i)^drop\s+index\s+concurrently\s+if\s+exists\s+([\w."]+)\s*$`)
	alterTable    = regexp.MustCompile(`(?i)^alter\s+table\s+(?:if\s+exists\s+)?(?:only\s+)?([\w."]+)`)
	addsAnIndex   = regexp.MustCompile(`(?i)\badd\s+(?:constraint\s+[\w"]+\s+)?(?:primary\s+key|unique|exclude)\b`)
	usingAnIndex  = regexp.MustCompile(`(?i)\busing\s+index\b`)
	migrationName = regexp.MustCompile(`^(\d+)_.*\.sql$`)
)

// relation is a table or index name as the migrations write it, without
// quotes and in lower case; baseName drops its schema.
func relation(name string) string { return strings.ToLower(strings.ReplaceAll(name, `"`, "")) }

func baseName(name string) string {
	n := relation(name)
	return n[strings.LastIndex(n, ".")+1:]
}

// statements is sql split at its semicolons, comments and the bodies of
// functions and DO blocks ($$…$$) dropped, each trimmed. The migrations
// quote no semicolon elsewhere, so the split is the statements.
func statements(sql string) []string {
	var out []string
	for _, s := range strings.Split(dollarQuoted.ReplaceAllString(lineComment.ReplaceAllString(sql, ""), "$$$$"), ";") {
		if s = strings.Join(strings.Fields(s), " "); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// lintIndexBuilds is every way a migration after lockedThrough would hold
// an existing table's writes while it builds an index, or would not run
// again after a failed build:
//
//   - an index on a table an earlier migration created is built
//     CONCURRENTLY, in a migration tern runs outside a transaction (the
//     disable-tx marker);
//   - such a migration holds nothing but those builds, each after a DROP
//     INDEX CONCURRENTLY IF EXISTS of its index: a build that fails leaves
//     an invalid index behind, and the migration, its version not recorded,
//     runs again from the drop;
//   - no constraint that builds an index (PRIMARY KEY, UNIQUE, EXCLUDE) is
//     added to an existing table but over an index built so: USING INDEX.
func lintIndexBuilds(files []migrationFile) []string {
	files = slices.Clone(files)
	slices.SortFunc(files, func(a, b migrationFile) int { return a.seq - b.seq })
	existing := map[string]bool{}
	var problems []string
	for _, f := range files {
		created := map[string]bool{}
		marked := strings.Contains(f.sql, disableTx)
		dropped := map[string]bool{}
		concurrent := false
		for _, s := range statements(f.sql) {
			if m := createTable.FindStringSubmatch(s); m != nil {
				created[relation(m[1])] = true
				continue
			}
			if f.seq <= lockedThrough {
				continue
			}
			if m := dropIndex.FindStringSubmatch(s); m != nil {
				dropped[baseName(m[1])] = true
				continue
			}
			if m := createIndex.FindStringSubmatch(s); m != nil {
				isConcurrent, index, table := m[1] != "", baseName(m[2]), relation(m[3])
				concurrent = concurrent || isConcurrent
				switch {
				case !existing[table]:
					// A table of this migration's own has no writes to hold.
				case !isConcurrent:
					problems = append(problems, f.name+": "+index+" is built on "+table+" without CONCURRENTLY, holding its writes")
				case !dropped[index]:
					problems = append(problems, f.name+": "+index+" is not dropped first (DROP INDEX CONCURRENTLY IF EXISTS), so a failed build is not run again")
				}
				continue
			}
			if m := alterTable.FindStringSubmatch(s); m != nil && existing[relation(m[1])] && addsAnIndex.MatchString(s) && !usingAnIndex.MatchString(s) {
				problems = append(problems, f.name+": a constraint on "+relation(m[1])+" builds its index under a lock; build it CONCURRENTLY first and add it USING INDEX")
			}
			if marked {
				problems = append(problems, f.name+": runs outside a transaction, so it holds only index builds, not: "+s)
			}
		}
		switch {
		case concurrent && !marked:
			problems = append(problems, f.name+": builds an index CONCURRENTLY without the "+disableTx+" marker, which a transaction refuses")
		case marked && f.seq <= lockedThrough:
		case marked && len(dropped) == 0:
			problems = append(problems, f.name+": carries the "+disableTx+" marker but builds no index")
		}
		if f.seq > lockedThrough {
			for _, body := range dollarQuoted.FindAllString(lineComment.ReplaceAllString(f.sql, ""), -1) {
				if indexInBody.MatchString(body) {
					problems = append(problems, f.name+": builds an index inside a function or DO block, where it cannot be built CONCURRENTLY")
				}
			}
		}
		for t := range created {
			existing[t] = true
		}
	}
	return problems
}

// embeddedMigrations is every migration the package carries.
func embeddedMigrations(t *testing.T) []migrationFile {
	t.Helper()
	entries, err := fs.ReadDir(migrationFiles, migrationsDir)
	if err != nil {
		t.Fatal(err)
	}
	var out []migrationFile
	for _, e := range entries {
		m := migrationName.FindStringSubmatch(e.Name())
		if m == nil {
			t.Fatalf("migration %s is not named NNN_name.sql", e.Name())
		}
		seq, _ := strconv.Atoi(m[1])
		raw, err := fs.ReadFile(migrationFiles, path.Join(migrationsDir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, migrationFile{seq: seq, name: e.Name(), sql: string(raw)})
	}
	return out
}

// No migration after lockedThrough builds an index on an existing table
// under a lock, or so that a failed build would not run again.
func TestMigrationsBuildIndexesWithoutALock(t *testing.T) {
	files := embeddedMigrations(t)
	if last := files[len(files)-1].seq; last < lockedThrough {
		t.Fatalf("the migrations end at %d, before lockedThrough %d", last, lockedThrough)
	}
	for _, p := range lintIndexBuilds(files) {
		t.Error(p)
	}
}

// The rules lintIndexBuilds holds a migration to, one case each.
func TestLintIndexBuilds(t *testing.T) {
	base := []migrationFile{
		{seq: 1, name: "001_records.sql", sql: "CREATE TABLE limes.capabilities (id uuid PRIMARY KEY, tenant_id uuid);\nCREATE INDEX capabilities_tenant ON limes.capabilities (tenant_id);"},
		{seq: lockedThrough, name: "014_record_listing.sql", sql: "-- locked, applied in the wild\nCREATE INDEX capabilities_tenant_id ON limes.capabilities (tenant_id, id);"},
	}
	const next = lockedThrough + 1
	concurrent := disableTx + "\nDROP INDEX CONCURRENTLY IF EXISTS limes.capabilities_x;\nCREATE INDEX CONCURRENTLY capabilities_x ON limes.capabilities (id);"
	for name, tc := range map[string]struct {
		sql  string
		want string // a problem's substring; empty for none
	}{
		"a concurrent build, dropped first":             {concurrent, ""},
		"a unique concurrent build":                     {disableTx + "\nDROP INDEX CONCURRENTLY IF EXISTS limes.capabilities_u;\nCREATE UNIQUE INDEX CONCURRENTLY capabilities_u ON limes.capabilities (id);", ""},
		"an index on a table of the same migration":     {"CREATE TABLE limes.fresh (id uuid);\nCREATE INDEX fresh_id ON limes.fresh (id);", ""},
		"a plain build on an existing table":            {"CREATE INDEX capabilities_x ON limes.capabilities (id);", "without CONCURRENTLY"},
		"a plain build, its name quoted":                {`CREATE INDEX IF NOT EXISTS "capabilities_x" ON ONLY "limes"."capabilities" (id);`, "without CONCURRENTLY"},
		"a concurrent build without the marker":         {"DROP INDEX CONCURRENTLY IF EXISTS limes.capabilities_x;\nCREATE INDEX CONCURRENTLY capabilities_x ON limes.capabilities (id);", "without the"},
		"a concurrent build not dropped first":          {disableTx + "\nCREATE INDEX CONCURRENTLY capabilities_x ON limes.capabilities (id);", "not dropped first"},
		"another statement beside a concurrent build":   {concurrent + "\nALTER TABLE limes.capabilities ADD COLUMN flag boolean;", "holds only index builds"},
		"a unique constraint on an existing table":      {"ALTER TABLE limes.capabilities ADD CONSTRAINT one_tenant UNIQUE (tenant_id);", "builds its index under a lock"},
		"a primary key on an existing table":            {"ALTER TABLE ONLY limes.capabilities ADD PRIMARY KEY (id);", "builds its index under a lock"},
		"a constraint over an index built concurrently": {"ALTER TABLE limes.capabilities ADD CONSTRAINT one_id UNIQUE USING INDEX capabilities_u;", ""},
		"a column added to an existing table":           {"ALTER TABLE limes.capabilities ADD COLUMN note text;", ""},
		"the marker with no index":                      {disableTx + "\n-- nothing", "builds no index"},
		"a function whose body holds semicolons":        {"CREATE FUNCTION limes.f() RETURNS trigger LANGUAGE plpgsql AS $$\nBEGIN\n    UPDATE limes.capabilities SET id = id;\n    RETURN NULL;\nEND\n$$;", ""},
		"an index built in a DO block":                  {"DO $$\nBEGIN\n    EXECUTE 'CREATE INDEX x ON limes.capabilities (id)';\nEND\n$$;", "inside a function or DO block"},
	} {
		problems := lintIndexBuilds(append(slices.Clone(base), migrationFile{seq: next, name: "015_next.sql", sql: tc.sql}))
		switch {
		case tc.want == "" && len(problems) > 0:
			t.Errorf("%s: problems %q, want none", name, problems)
		case tc.want != "" && !slices.ContainsFunc(problems, func(p string) bool { return strings.Contains(p, tc.want) }):
			t.Errorf("%s: problems %q, want one saying %q", name, problems, tc.want)
		}
	}
	if problems := lintIndexBuilds(base); len(problems) > 0 {
		t.Errorf("migrations through lockedThrough: problems %q, want none", problems)
	}
}

const (
	createIndexed = `CREATE TABLE public.indexed (id int)`
	fillIndexed   = `INSERT INTO public.indexed SELECT g FROM generate_series(1, 100) g`
	// buildIndexed builds the index as a migration after lockedThrough must.
	buildIndexed = disableTx + `
DROP INDEX CONCURRENTLY IF EXISTS public.indexed_id;
CREATE UNIQUE INDEX CONCURRENTLY indexed_id ON public.indexed (id);`
	sqlIndexValid = `SELECT indisvalid FROM pg_index WHERE indexrelid = 'public.indexed_id'::regclass`
)

// indexedMigrator is a migrator of the indexed table and its index, on
// probeVersionTable.
func indexedMigrator(t *testing.T, conn *pgx.Conn, build string) *tern.Migrator {
	t.Helper()
	m, err := tern.NewMigrator(t.Context(), conn, probeVersionTable)
	if err != nil {
		t.Fatal(err)
	}
	m.AppendMigration("create the table", createIndexed, "")
	m.AppendMigration("fill it", fillIndexed, "")
	m.AppendMigration("build the index", build, "")
	return m
}

// A migration marked to run outside a transaction builds its index
// CONCURRENTLY through the run Migrate makes, scoped to every tenant; the
// same migration unmarked is refused, a transaction holding it.
func TestAMigrationBuildsAnIndexConcurrently(t *testing.T) {
	conn := freshConn(t)
	ctx := t.Context()
	unmarked := strings.Replace(buildIndexed, disableTx, "", 1)
	if err := runMigrations(ctx, conn, indexedMigrator(t, conn, unmarked)); err == nil || !strings.Contains(err.Error(), "transaction block") {
		t.Fatalf("an unmarked concurrent build: err = %v, want refused inside a transaction block", err)
	}
	if err := runMigrations(ctx, conn, indexedMigrator(t, conn, buildIndexed)); err != nil {
		t.Fatalf("a marked concurrent build: %v", err)
	}
	var valid bool
	if err := conn.QueryRow(ctx, sqlIndexValid).Scan(&valid); err != nil || !valid {
		t.Fatalf("the index after the build: valid = %v, %v; want valid", valid, err)
	}
}

// A concurrent build that fails leaves an invalid index and its version
// unrecorded; the migration runs again from its drop and builds the index
// once what made it fail is gone.
func TestAFailedConcurrentBuildRunsAgain(t *testing.T) {
	conn := freshConn(t)
	ctx := t.Context()
	m := indexedMigrator(t, conn, buildIndexed)
	m.Migrations[1].UpSQL = fillIndexed + `; INSERT INTO public.indexed VALUES (1)` // a duplicate the unique build refuses
	if err := runMigrations(ctx, conn, m); err == nil {
		t.Fatal("a unique build over a duplicate: err = nil, want it refused")
	}
	var valid bool
	if err := conn.QueryRow(ctx, sqlIndexValid).Scan(&valid); err != nil || valid {
		t.Fatalf("the index the failed build left: valid = %v, %v; want an invalid index", valid, err)
	}
	if _, err := conn.Exec(ctx, `DELETE FROM public.indexed WHERE ctid = (SELECT max(ctid) FROM public.indexed WHERE id = 1)`); err != nil {
		t.Fatal(err)
	}
	if err := runMigrations(ctx, conn, indexedMigrator(t, conn, buildIndexed)); err != nil {
		t.Fatalf("the build run again: %v", err)
	}
	if err := conn.QueryRow(ctx, sqlIndexValid).Scan(&valid); err != nil || !valid {
		t.Fatalf("the index after the build ran again: valid = %v, %v; want valid", valid, err)
	}
	var version int32
	if err := conn.QueryRow(context.WithoutCancel(ctx), `SELECT version FROM `+probeVersionTable).Scan(&version); err != nil || version != 3 {
		t.Fatalf("version after the build ran again = %d, %v; want 3", version, err)
	}
}
