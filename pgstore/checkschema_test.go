package pgstore

import (
	"errors"
	"testing"

	"github.com/oleg-tkachuk/limes/pgstore/migrate"
)

// freshDatabase is the database a check that needs no schema starts in.
const freshDatabase = "limes_fresh"

// The schema's version reads back as the package expects once migrated, and
// CheckSchema tells a schema behind or ahead of it, readable as the
// runtime role.
func TestCheckSchema(t *testing.T) {
	ctx := t.Context()
	v, err := ReadSchemaVersion(ctx, app)
	if err != nil {
		t.Fatalf("ReadSchemaVersion as the runtime role: %v", err)
	}
	if v.Applied == 0 || v.Applied != v.Expected {
		t.Errorf("ReadSchemaVersion = %+v, want the migrations this package carries applied", v)
	}
	if err := CheckSchema(ctx, app); err != nil {
		t.Errorf("CheckSchema as the runtime role: %v", err)
	}
	conn := pg.FreshConn(t, freshDatabase)
	// A database Migrate never ran in has no version table, nor even the
	// schema: it is at version 0, which is a mismatch, not an outage.
	if got, err := ReadSchemaVersion(ctx, conn); err != nil || got.Applied != 0 {
		t.Errorf("ReadSchemaVersion before any Migrate = %+v, %v; want version 0", got, err)
	}
	if err := CheckSchema(ctx, conn); !errors.Is(err, ErrSchemaMismatch) {
		t.Errorf("CheckSchema before any Migrate: err = %v, want ErrSchemaMismatch", err)
	}
	if err := migrate.Run(ctx, conn, migrate.Options{}); err != nil {
		t.Fatal(err)
	}
	for name, shift := range map[string]int{"behind": -1, "ahead": 1} {
		if _, err := conn.Exec(ctx, `UPDATE `+versionTable+` SET version = $1`, v.Expected+shift); err != nil {
			t.Fatal(err)
		}
		if err := CheckSchema(ctx, conn); !errors.Is(err, ErrSchemaMismatch) {
			t.Errorf("CheckSchema of a schema %s: err = %v, want ErrSchemaMismatch", name, err)
		}
	}
}
