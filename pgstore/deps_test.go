package pgstore

import (
	"os/exec"
	"strings"
	"testing"
)

// A binary that serves links what it serves with: the migrations and tern
// only in a migration job, which imports pgstore/migrate, and the test
// container only in tests. Checked over every package of the module, as
// `go list` resolves each one's dependencies, not as its imports read.

// The module's packages, and the ones allowed what the others are not.
const (
	pgstorePath       = "github.com/oleg-tkachuk/limes/pgstore"
	migratePath       = pgstorePath + "/migrate"
	pgtestPath        = pgstorePath + "/internal/pgtest"
	ternPath          = "github.com/jackc/tern"
	testcontainerPath = "github.com/testcontainers/testcontainers-go"
)

// depsFormat prints each package and then its dependencies, one package a
// line.
const depsFormat = `{{.ImportPath}}{{range .Deps}} {{.}}{{end}}`

// within reports whether path is prefix or a package beneath it.
func within(path, prefix string) bool { return path == prefix || strings.HasPrefix(path, prefix+"/") }

// forbiddenDeps is what each package of the module may not depend on: tern
// and pgstore/migrate outside pgstore/migrate, the test container outside
// pgtest.
func forbiddenDeps(pkg string, deps []string) []string {
	var found []string
	for _, d := range deps {
		switch {
		case within(d, ternPath) && pkg != migratePath,
			d == migratePath && pkg != migratePath,
			within(d, testcontainerPath) && pkg != pgtestPath:
			found = append(found, d)
		}
	}
	return found
}

func TestForbiddenDeps(t *testing.T) {
	tern := ternPath + "/v2/migrate"
	postgresModule := testcontainerPath + "/modules/postgres"
	for _, tc := range []struct {
		pkg  string
		deps []string
		want int
	}{
		{pgstorePath, []string{"fmt", "github.com/jackc/pgx/v5"}, 0},
		{pgstorePath, []string{tern}, 1},
		{pgstorePath, []string{migratePath}, 1},
		{pgstorePath, []string{"github.com/jackc/ternary"}, 0},
		{migratePath, []string{tern}, 0},
		{migratePath, []string{postgresModule}, 1},
		{pgtestPath, []string{postgresModule}, 0},
		{pgtestPath, []string{tern}, 1},
	} {
		if got := forbiddenDeps(tc.pkg, tc.deps); len(got) != tc.want {
			t.Errorf("forbiddenDeps(%s, %v) = %v, want %d", tc.pkg, tc.deps, got, tc.want)
		}
	}
}

func TestNoPackageLinksWhatItDoesNotRun(t *testing.T) {
	out, err := exec.CommandContext(t.Context(), "go", "list", "-f", depsFormat, "./...").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	seen := map[string]bool{}
	for line := range strings.Lines(string(out)) {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		seen[fields[0]] = true
		if found := forbiddenDeps(fields[0], fields[1:]); len(found) > 0 {
			t.Errorf("%s depends on %v", fields[0], found)
		}
	}
	for _, p := range []string{pgstorePath, migratePath, pgtestPath} {
		if !seen[p] {
			t.Errorf("go list did not list %s: the check read no graph", p)
		}
	}
}
