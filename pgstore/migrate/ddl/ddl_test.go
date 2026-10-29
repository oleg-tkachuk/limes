package ddl

import (
	"io/fs"
	"path"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/oleg-tkachuk/limes/pgstore/internal/pgschema"
)

// statementTerminator ends every statement a tool is handed.
const statementTerminator = ";"

// dollarQuote opens and closes a dollar-quoted body, which a statement at
// a line's end does not end.
const dollarQuote = "$$"

// lineComment opens a comment to the line's end.
const lineComment = "--"

// The steps are the embedded migrations, numbered from one, the last at
// Version, each outside a transaction exactly when its file asks for that.
func TestStepsAreTheMigrationFiles(t *testing.T) {
	steps, err := Steps()
	if err != nil {
		t.Fatal(err)
	}
	files, err := fs.Glob(Files(), MigrationsDir+"/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != len(files) || len(steps) != Version() {
		t.Fatalf("%d steps from %d files; Version is %d", len(steps), len(files), Version())
	}
	for i, s := range steps {
		raw, err := fs.ReadFile(Files(), files[i])
		if err != nil {
			t.Fatal(err)
		}
		if s.Version != i+1 || path.Base(files[i]) != s.Name {
			t.Errorf("step %d is version %d, %s; want version %d, %s", i, s.Version, s.Name, i+1, files[i])
		}
		if want := !disableTxPattern.Match(raw); s.InTransaction != want {
			t.Errorf("%s: InTransaction %v, want %v", s.Name, s.InTransaction, want)
		}
		if strings.TrimSpace(string(raw)) != s.Migration {
			t.Errorf("%s: Migration is not its file", s.Name)
		}
	}
}

// SQL records the step's version after the migration, ending with a
// terminator, and a transactional step opens read committed and scoped to
// every tenant.
func TestStepSQL(t *testing.T) {
	in := Step{Version: 7, Migration: "SELECT 1", InTransaction: true}
	out := Step{Version: 8, Migration: "SELECT 2", InTransaction: false}
	readCommitted := "SET TRANSACTION ISOLATION LEVEL READ COMMITTED;"
	for _, tc := range []struct {
		step   Step
		prefix string
		suffix string
	}{
		{in, readCommitted, "UPDATE " + versionTable + " SET version = 7;"},
		{out, "SELECT 2", "UPDATE " + versionTable + " SET version = 8;"},
	} {
		sql := tc.step.SQL()
		if !strings.HasPrefix(sql, tc.prefix) || !strings.HasSuffix(sql, tc.suffix) || !strings.Contains(sql, tc.step.Migration) {
			t.Errorf("Step%+v.SQL() = %q, want it to begin %q, hold the migration, and end %q", tc.step, sql, tc.prefix, tc.suffix)
		}
	}
	if !strings.Contains(in.SQL(), "'"+pgschema.SettingAllTenants+"', '"+pgschema.SettingAllTenantsOn+"', true") {
		t.Errorf("a step in a transaction is not scoped to every tenant: %q", in.SQL())
	}
	if strings.Contains(out.SQL(), pgschema.SettingAllTenants) || strings.Contains(out.SQL(), readCommitted) {
		t.Errorf("a step outside a transaction sets what only a transaction holds: %q", out.SQL())
	}
}

// Everything a tool is handed ends with a statement terminator.
func TestEverySQLEndsWithATerminator(t *testing.T) {
	steps, err := Steps()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(Setup(), statementTerminator) {
		t.Errorf("Setup() ends %q", Setup()[len(Setup())-1:])
	}
	for _, s := range steps {
		if !strings.HasSuffix(s.SQL(), statementTerminator) {
			t.Errorf("%s: SQL() does not end with %q", s.Name, statementTerminator)
		}
		for _, stmt := range s.Statements() {
			if !strings.HasSuffix(stmt, statementTerminator) {
				t.Errorf("%s: a statement does not end with %q: %q", s.Name, statementTerminator, stmt)
			}
		}
	}
}

// code is sql without its comment lines and blank lines, each line
// trimmed: what PostgreSQL runs.
func code(sql string) []string {
	var out []string
	for _, line := range strings.Split(sql, "\n") {
		if l := strings.TrimSpace(line); l != "" && !strings.HasPrefix(l, lineComment) {
			out = append(out, l)
		}
	}
	return out
}

// A transactional step is its SQL, whole. A non-transactional one is each
// of its statements, then the version record: each statement one
// statement, ending at its line's end, none dollar-quoted, and together
// every line of the migration.
func TestStatements(t *testing.T) {
	steps, err := Steps()
	if err != nil {
		t.Fatal(err)
	}
	var outside int
	for _, s := range steps {
		got := s.Statements()
		if s.InTransaction {
			if !slices.Equal(got, []string{s.SQL()}) {
				t.Errorf("%s: Statements() is not its SQL, whole", s.Name)
			}
			continue
		}
		outside++
		if strings.Contains(s.Migration, dollarQuote) {
			t.Errorf("%s: runs outside a transaction and holds a dollar-quoted body, which Statements cannot split", s.Name)
		}
		if last := got[len(got)-1]; last != s.record() {
			t.Errorf("%s: the last statement is %q, want the version record", s.Name, last)
		}
		var joined []string
		for _, stmt := range got[:len(got)-1] {
			lines := code(stmt)
			if n := strings.Count(strings.Join(lines, "\n"), statementTerminator); n != 1 {
				t.Errorf("%s: a statement holds %d terminators: %q", s.Name, n, stmt)
			}
			joined = append(joined, lines...)
		}
		if want := code(s.Migration); !slices.Equal(joined, want) {
			t.Errorf("%s: the statements hold %q, the migration %q", s.Name, joined, want)
		}
	}
	if outside == 0 {
		t.Error("no step runs outside a transaction; the split is untested")
	}
}

func TestSplitStatements(t *testing.T) {
	sql := "-- a comment\nDROP INDEX x;\n-- another\nCREATE INDEX x\n  ON t (id);\n-- trailing"
	want := []string{"-- a comment\nDROP INDEX x;", "-- another\nCREATE INDEX x\n  ON t (id);"}
	if got := splitStatements(sql); !slices.Equal(got, want) {
		t.Errorf("splitStatements = %q, want %q", got, want)
	}
}

// quoteIdentifier quotes as pgx does, which the stores' own SQL uses.
func TestQuoteIdentifierIsPgxs(t *testing.T) {
	for _, parts := range [][]string{{"limes"}, {"limes", "schema_version"}, {`a"b`}, {"app\x00role"}, {"MixedCase"}, {""}} {
		if got, want := quoteIdentifier(parts...), pgx.Identifier(parts).Sanitize(); got != want {
			t.Errorf("quoteIdentifier(%q) = %s, pgx's %s", parts, got, want)
		}
	}
}

// Grants refuses a runtime role that is not one role, and grants nothing
// for none.
func TestGrantsRefusesARoleThatIsNotOne(t *testing.T) {
	for _, role := range []string{"", "public", "PUBLIC", "app\x00role"} {
		if stmts, err := Grants([]string{role}); err == nil {
			t.Errorf("Grants(%q) = %d statements, want an error", role, len(stmts))
		}
	}
	if stmts, err := Grants(nil); err != nil || len(stmts) != 0 {
		t.Errorf("Grants(nil) = %v, %v; want nothing", stmts, err)
	}
}
