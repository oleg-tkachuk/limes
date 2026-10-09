package limes

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// This module must be usable by a third party that runs neither object
// storage nor a particular database, so its RESOLVED dependency graph — not
// its go.mod, a transitive pull disqualifies it just as much — may contain no
// database driver and no object-storage SDK. The check lives in the module's
// own suite so that `task verify-all`, and through it CI, cannot run without
// it.

// Labels for the two properties, as the failure message states them.
const (
	labelDatabaseDriver = "a database driver"
	labelStorageSDK     = "an object-storage SDK"
)

// modulePath is this module's own import path. The live check
// requires it in the listing, so an empty or wrong-directory `go list`
// cannot pass as a clean graph.
const modulePath = "github.com/oleg-tkachuk/limes"

// goListPattern selects every package of the module from its root, which is
// the working directory `go test` gives this root package.
const goListPattern = "./..."

// forbiddenImport is an import-path prefix that must not appear in the graph.
type forbiddenImport struct {
	prefix string
	label  string
}

// forbiddenImports is the one list of what the module may not depend on,
// written as full module paths and matched on path-segment boundaries: a
// substring match would also hit unrelated names such as
// `github.com/lib/pqueue`. Matching on segments means aws-sdk-go does not
// cover aws-sdk-go-v2 by accident, so v2 is listed in its own right.
//
// Third-party drivers only: the stdlib `database/sql/driver` package is an
// interface definition that google/uuid imports to implement sql.Scanner.
var forbiddenImports = []forbiddenImport{
	{prefix: "github.com/jackc/pgx", label: labelDatabaseDriver},
	{prefix: "github.com/lib/pq", label: labelDatabaseDriver},
	{prefix: "github.com/go-sql-driver/mysql", label: labelDatabaseDriver},
	{prefix: "github.com/mattn/go-sqlite3", label: labelDatabaseDriver},
	{prefix: "modernc.org/sqlite", label: labelDatabaseDriver},
	{prefix: "github.com/jmoiron/sqlx", label: labelDatabaseDriver},
	{prefix: "github.com/aws/aws-sdk-go", label: labelStorageSDK},
	{prefix: "github.com/aws/aws-sdk-go-v2", label: labelStorageSDK},
	{prefix: "github.com/minio/minio-go", label: labelStorageSDK},
	{prefix: "cloud.google.com/go/storage", label: labelStorageSDK},
	{prefix: "github.com/Azure/azure-sdk-for-go", label: labelStorageSDK},
}

// dependencyViolation is one forbidden package found in the graph.
type dependencyViolation struct {
	importPath string
	label      string
}

// matchesPrefix reports whether importPath is prefix itself or a package
// beneath it — never a sibling that merely starts with the same characters.
func matchesPrefix(importPath, prefix string) bool {
	return importPath == prefix || strings.HasPrefix(importPath, prefix+"/")
}

// findForbidden returns every import path that falls under an entry of
// forbidden, in input order.
func findForbidden(importPaths []string, forbidden []forbiddenImport) []dependencyViolation {
	var found []dependencyViolation
	for _, p := range importPaths {
		for _, f := range forbidden {
			if matchesPrefix(p, f.prefix) {
				found = append(found, dependencyViolation{importPath: p, label: f.label})
				break
			}
		}
	}
	return found
}

// goListPackage is the one field this check reads from `go list -json`.
type goListPackage struct {
	ImportPath string
}

// decodeImportPaths reads the stream `go list -json` prints: concatenated
// objects, not an array.
func decodeImportPaths(r io.Reader) ([]string, error) {
	var paths []string
	dec := json.NewDecoder(r)
	for {
		var pkg goListPackage
		err := dec.Decode(&pkg)
		if errors.Is(err, io.EOF) {
			return paths, nil
		}
		if err != nil {
			return nil, err
		}
		paths = append(paths, pkg.ImportPath)
	}
}

func TestFindForbidden(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		paths []string
		want  []dependencyViolation
	}{
		{
			name:  "clean graph",
			paths: []string{"fmt", "github.com/google/uuid", "go.opentelemetry.io/otel", modulePath},
		},
		{
			name:  "one database driver",
			paths: []string{"fmt", "github.com/lib/pq", "github.com/google/uuid"},
			want:  []dependencyViolation{{importPath: "github.com/lib/pq", label: labelDatabaseDriver}},
		},
		{
			name:  "subpackage of a forbidden module",
			paths: []string{"github.com/jackc/pgx/v5/pgconn"},
			want:  []dependencyViolation{{importPath: "github.com/jackc/pgx/v5/pgconn", label: labelDatabaseDriver}},
		},
		{
			name:  "aws sdk v2 is caught in its own right",
			paths: []string{"github.com/aws/aws-sdk-go-v2/service/s3"},
			want:  []dependencyViolation{{importPath: "github.com/aws/aws-sdk-go-v2/service/s3", label: labelStorageSDK}},
		},
		{
			name:  "one of each kind",
			paths: []string{"modernc.org/sqlite/lib", "github.com/minio/minio-go/v7"},
			want: []dependencyViolation{
				{importPath: "modernc.org/sqlite/lib", label: labelDatabaseDriver},
				{importPath: "github.com/minio/minio-go/v7", label: labelStorageSDK},
			},
		},
		{
			name:  "near-miss names do not match",
			paths: []string{"github.com/lib/pqueue", "github.com/jackc/pgxlisten", "github.com/jackc/puddle/v2", "cloud.google.com/go/storagetransfer"},
		},
		{
			name:  "stdlib database/sql/driver is an interface, not a driver",
			paths: []string{"database/sql", "database/sql/driver"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := findForbidden(tt.paths, forbiddenImports)
			if !slices.Equal(got, tt.want) {
				t.Errorf("findForbidden(%q) = %+v, want %+v", tt.paths, got, tt.want)
			}
		})
	}
}

func TestDecodeImportPaths(t *testing.T) {
	t.Parallel()

	t.Run("concatenated objects", func(t *testing.T) {
		t.Parallel()

		in := `{"ImportPath":"fmt"}` + "\n" + `{"ImportPath":"github.com/lib/pq"}`
		got, err := decodeImportPaths(strings.NewReader(in))
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		want := []string{"fmt", "github.com/lib/pq"}
		if !slices.Equal(got, want) {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("malformed input is an error", func(t *testing.T) {
		t.Parallel()

		if _, err := decodeImportPaths(strings.NewReader(`{"ImportPath":`)); err == nil {
			t.Error("want an error for truncated JSON, got nil")
		}
	})
}

// TestModuleDependencyIsolation runs the check against the module's real,
// resolved, non-test dependency graph.
func TestModuleDependencyIsolation(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(t.Context(), "go", "list", "-deps", "-json=ImportPath", goListPattern)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("go list -deps: %v\n%s", err, stderr.String())
	}

	paths, err := decodeImportPaths(&stdout)
	if err != nil {
		t.Fatalf("decode go list output: %v", err)
	}
	if !slices.Contains(paths, modulePath) {
		t.Fatalf("go list did not report %s itself; the graph it returned (%d packages) is not this module's", modulePath, len(paths))
	}

	found := findForbidden(paths, forbiddenImports)
	if len(found) == 0 {
		return
	}
	var msg strings.Builder
	for _, v := range found {
		msg.WriteString("\n  " + v.importPath + " — " + v.label)
	}
	t.Fatalf("the module's dependency graph contains forbidden packages:%s\n\n"+
		"It must be consumable by a third party that uses neither object storage nor a\n"+
		"specific database. Code that needs them belongs in the consumer.", msg.String())
}
