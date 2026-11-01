// Package ddl is pgstore's schema as data: the migrations, the SQL that
// creates the version table before them, and the grants after, for a
// consumer that applies them with a migration tool of its own
// (docs/adr/0011-migrations-as-data.md). It imports nothing outside the
// standard library, so a program that vendors the SQL links neither tern nor
// a driver; pgstore/migrate runs the same migrations itself.
//
// A tool applies Setup once, then each Step not yet applied, in order, then
// Grants. Each transactional step runs in a transaction of its own: it opens
// by setting the transaction READ COMMITTED, which PostgreSQL accepts only
// as a transaction's first statement, and which migration 016 needs, since
// it reads each level of records by the next statement's snapshot. A
// non-transactional step runs a statement at a time outside any transaction,
// as CREATE INDEX CONCURRENTLY needs (Step.Statements).
package ddl

import (
	"embed"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/oleg-tkachuk/limes/internal/errs"
	"github.com/oleg-tkachuk/limes/pgstore/internal/pgschema"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// MigrationsDir is where Files keeps the migrations.
const MigrationsDir = "migrations"

// Files are the migrations as files, under MigrationsDir: for a tool that
// reads a directory, and for pgstore/migrate's tern.
func Files() fs.FS { return migrationFiles }

// Version is the last migration's: the version a schema is at once every
// step has run, and the one pgstore.CheckSchema expects. A vendored copy
// that a newer pgstore outgrew is behind it. A function, not a constant, so
// that a release adding a migration changes the value, not the API.
func Version() int { return pgschema.Version }

// The schema and its version table.
const (
	schema           = pgschema.Name
	versionTableName = pgschema.VersionTableName
	versionTable     = pgschema.VersionTable
)

// Step is one migration as a consumer's own migration tool applies it.
type Step struct {
	// Version is the schema's version once the step has run: tern's
	// numbering, from one without a gap.
	Version int
	// Name is the migration's file name, such as "001_records.sql".
	Name string
	// Migration is the migration's SQL as its file holds it. Apply SQL or
	// Statements rather than this.
	Migration string
	// InTransaction says how to run the step: SQL as one transaction of its
	// own; or, when false, Statements a statement at a time outside any
	// transaction, as CREATE INDEX CONCURRENTLY needs.
	InTransaction bool
}

// stepPrelude begins a step run in a transaction: read committed, and
// scoped to every tenant for that transaction alone, without which a
// migration that moves rows sees none under the forced row-level security.
var stepPrelude = "SET TRANSACTION ISOLATION LEVEL READ COMMITTED;\nSELECT set_config('" +
	pgschema.SettingAllTenants + "', '" + pgschema.SettingAllTenantsOn + "', true);\n"

// stepSeparator ends a migration's last statement before the one SQL adds,
// whether or not the migration ends it itself.
const stepSeparator = "\n;\n"

// record is the statement that records the step's version, so
// pgstore.CheckSchema reads it and pgstore/migrate carries on from it.
func (s Step) record() string {
	return fmt.Sprintf("UPDATE %s SET version = %d;", versionTable, s.Version)
}

// SQL is what a tool applies for a transactional step: the prelude, the
// migration, then the version record, ending with a statement terminator.
// For a non-transactional step it is the migration and the record as one
// text, which a tool that sends one string runs as an implicit transaction;
// send Statements instead.
func (s Step) SQL() string {
	if !s.InTransaction {
		return s.Migration + stepSeparator + s.record()
	}
	return stepPrelude + s.Migration + stepSeparator + s.record()
}

// Statements is the step as a tool sends it. A transactional step is SQL
// alone, run as one transaction. A non-transactional step is each of its
// statements and then the version record, to send one at a time outside
// any transaction. Such a migration holds no dollar-quoted body and ends
// each statement at a line's end, which is what lets it be split here
// without a parser; a test holds every one to that.
func (s Step) Statements() []string {
	if s.InTransaction {
		return []string{s.SQL()}
	}
	return append(splitStatements(s.Migration), s.record())
}

// splitStatements is sql's statements, each ending at a line that ends with
// ";", with the comments before it; text holding only comments is dropped.
func splitStatements(sql string) []string {
	var stmts []string
	var cur []string
	for _, line := range strings.Split(sql, "\n") {
		cur = append(cur, line)
		if strings.HasSuffix(strings.TrimSpace(line), ";") {
			stmts = append(stmts, strings.TrimSpace(strings.Join(cur, "\n")))
			cur = nil
		}
	}
	return stmts
}

// disableTxPattern is the line by which a migration asks tern to run it
// outside a transaction, a statement at a time; tern matches it as a whole
// line.
var disableTxPattern = regexp.MustCompile(`(?m)^---- tern: disable-tx ----$`)

// migrationName is a migration's file name as tern reads it: its sequence
// number, then a name.
var migrationName = regexp.MustCompile(`^(\d+)_.+\.sql$`)

// sqlSetupVersion creates the schema and the version table at version 0,
// as pgstore/migrate's tern does before the first migration.
const sqlSetupVersion = `CREATE SCHEMA IF NOT EXISTS ` + schema + `;
CREATE TABLE IF NOT EXISTS ` + versionTable + `(version int4 NOT NULL PRIMARY KEY);
INSERT INTO ` + versionTable + `(version) SELECT 0 WHERE 0 = (SELECT count(*) FROM ` + versionTable + `);`

// Setup is the SQL a consumer's tool runs once, before the first Step: it
// creates the schema and the version table at version 0. Run it as the role
// that owns the schema and applies the steps.
func Setup() string { return sqlSetupVersion }

// Steps are the migrations in order, numbered from one without a gap, as
// tern numbers them: Setup first, then the steps not yet applied, then
// Grants. The tool keeps its own record of what it applied; the steps keep
// pgstore's (Step.SQL).
func Steps() ([]Step, error) {
	entries, err := fs.ReadDir(migrationFiles, MigrationsDir)
	if err != nil {
		return nil, fmt.Errorf("ddl: %w", err)
	}
	steps := make([]Step, 0, len(entries))
	for _, e := range entries {
		m := migrationName.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		n, err := strconv.Atoi(m[1])
		if err != nil {
			return nil, fmt.Errorf("ddl: %s: %w", e.Name(), err)
		}
		raw, err := fs.ReadFile(migrationFiles, path.Join(MigrationsDir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("ddl: %w", err)
		}
		up := strings.TrimSpace(string(raw))
		steps = append(steps, Step{Version: n, Name: e.Name(), Migration: up, InTransaction: !disableTxPattern.MatchString(up)})
	}
	slices.SortFunc(steps, func(a, b Step) int { return a.Version - b.Version })
	for i, s := range steps {
		if s.Version != i+1 {
			return nil, errs.Broken("ddl: migration %s is number %d, want %d: the numbers have a gap", s.Name, s.Version, i+1)
		}
	}
	return steps, nil
}

// tablePrivileges are what a runtime role may do to a table of the store.
const tablePrivileges = "SELECT, INSERT, UPDATE, DELETE"

// sequencePrivileges are what a runtime role may do to a sequence of the
// store: draw from it, as a column's default does.
const sequencePrivileges = "USAGE"

// publicRole is the name PostgreSQL reads as every role there is, quoted or
// not, in a GRANT.
const publicRole = "public"

// CheckRuntimeRoles refuses a runtime role that is not one role: an empty
// name, "public" in any case, which would grant every role, and a name with
// a NUL, which the identifier quoting drops, granting another role than the
// one named.
func CheckRuntimeRoles(roles []string) error {
	for _, r := range roles {
		switch {
		case r == "":
			return errs.Config("ddl: an empty runtime role name")
		case strings.EqualFold(r, publicRole):
			return errs.Config("ddl: runtime role %q names every role", r)
		case strings.ContainsRune(r, 0):
			return errs.Config("ddl: runtime role %q holds a NUL", r)
		}
	}
	return nil
}

// quoteIdentifier quotes a PostgreSQL identifier of parts, as pgx's
// Identifier.Sanitize does (a test holds the two equal): each part in
// double quotes, a double quote in it doubled, a NUL dropped, the parts
// joined by dots.
func quoteIdentifier(parts ...string) string {
	quoted := make([]string, len(parts))
	for i, p := range parts {
		p = strings.ReplaceAll(p, "\x00", "")
		quoted[i] = `"` + strings.ReplaceAll(p, `"`, `""`) + `"`
	}
	return strings.Join(quoted, ".")
}

// Grants are the statements that give each runtime role what the stores
// need of the schema: USAGE on it, SELECT, INSERT, UPDATE and DELETE on its
// tables, SELECT alone on the version table, EXECUTE on its functions,
// USAGE on its sequences, which the counters' defaults draw from, and the
// same on every table and sequence a later step creates. Run them in one transaction
// after the last step, and again after each later one. It refuses a name
// that is not one role (CheckRuntimeRoles).
func Grants(roles []string) ([]string, error) {
	if err := CheckRuntimeRoles(roles); err != nil {
		return nil, err
	}
	schemaName := quoteIdentifier(schema)
	versions := quoteIdentifier(schema, versionTableName)
	var stmts []string
	for _, r := range roles {
		role := quoteIdentifier(r)
		stmts = append(stmts,
			"GRANT USAGE ON SCHEMA "+schemaName+" TO "+role,
			"GRANT "+tablePrivileges+" ON ALL TABLES IN SCHEMA "+schemaName+" TO "+role,
			// The version table is the migrator's; a runtime role only
			// reads it.
			"REVOKE "+tablePrivileges+" ON "+versions+" FROM "+role,
			"GRANT SELECT ON "+versions+" TO "+role,
			// The schema's functions: the policies' scope checks and the
			// revocation checks, which no other role may call.
			"GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA "+schemaName+" TO "+role,
			"GRANT "+sequencePrivileges+" ON ALL SEQUENCES IN SCHEMA "+schemaName+" TO "+role,
			"ALTER DEFAULT PRIVILEGES IN SCHEMA "+schemaName+" GRANT "+tablePrivileges+" ON TABLES TO "+role,
			"ALTER DEFAULT PRIVILEGES IN SCHEMA "+schemaName+" GRANT "+sequencePrivileges+" ON SEQUENCES TO "+role,
		)
	}
	return stmts, nil
}
