package pgstore

import (
	"context"
	"errors"
	"os"
	"regexp"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The view's name in the catalog, and what the checks ask of it.
const (
	lineageViewName = "capability_lineage"
	sqlCountLineage = `SELECT count(*) FROM ` + lineageView
	// sqlSecurityInvoker is whether view $2 of schema $1 runs as its
	// caller.
	sqlSecurityInvoker = `
SELECT coalesce('security_invoker=true' = ANY (c.reloptions), false)
FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relname = $2`
	sqlLockLineage = `SELECT id FROM ` + lineageView + ` WHERE id = $1 FOR KEY SHARE`
	// sqlNoWait makes a statement that would wait on a lock fail at once.
	sqlNoWait          = `SET LOCAL lock_timeout = '1ms'`
	sqlDeleteRecord    = `DELETE FROM ` + schema + `.capabilities WHERE id = $1`
	sqlMarkRecord      = `UPDATE ` + schema + `.capabilities SET revoked = NOT revoked WHERE id = $1`
	recordsInFixture   = 2
	noRecordsInFixture = 0
)

// The view runs as its caller, so the forced row-level security of the
// records binds whoever reads it — the runtime role and the owner alike —
// to the tenant it is scoped to.
func TestTheLineageViewKeepsTenantsApart(t *testing.T) {
	var invoker bool
	if err := owner.QueryRow(t.Context(), sqlSecurityInvoker, schema, lineageViewName).Scan(&invoker); err != nil || !invoker {
		t.Fatalf("%s runs as its caller: %v, %v; want true", lineageView, invoker, err)
	}
	f := newTenantFixture(t)
	for role, pool := range map[string]*pgxpool.Pool{"the runtime role": app, "the owner": migrator} {
		for name, tc := range map[string]struct {
			ctx  context.Context
			want int
		}{
			"no scope":       {t.Context(), noRecordsInFixture},
			"another tenant": {WithTenant(t.Context(), f.theirs), noRecordsInFixture},
			"its tenant":     {WithTenant(t.Context(), f.mine), recordsInFixture},
		} {
			var seen int
			err := pgx.BeginFunc(tc.ctx, pool, func(tx pgx.Tx) error {
				if err := ScopeFromContext(tc.ctx, tx); err != nil {
					return err
				}
				return tx.QueryRow(tc.ctx, sqlCountLineage).Scan(&seen)
			})
			if err != nil {
				t.Fatalf("%s, %s: %v", role, name, err)
			}
			if seen != tc.want {
				t.Errorf("%s, %s: sees %d records through the view, want %d", role, name, seen, tc.want)
			}
		}
	}
}

// A FOR KEY SHARE through the view holds the record row itself: Purge's
// delete waits on it, a revocation's mark does not.
func TestALockThroughTheLineageViewHoldsTheRecord(t *testing.T) {
	f := newTenantFixture(t)
	ctx := WithTenant(t.Context(), f.mine)
	holder, err := app.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Rollback(context.WithoutCancel(ctx)) }()
	if err := ScopeFromContext(ctx, holder); err != nil {
		t.Fatal(err)
	}
	if _, err := holder.Exec(ctx, sqlLockLineage, f.child.ID); err != nil {
		t.Fatal(err)
	}
	try := func(sql string) error {
		return pgx.BeginFunc(t.Context(), owner, func(tx pgx.Tx) error {
			if _, err := tx.Exec(t.Context(), sqlNoWait); err != nil {
				return err
			}
			_, err := tx.Exec(t.Context(), sql, f.child.ID)
			return err
		})
	}
	if err := try(sqlDeleteRecord); !isLockNotAvailable(err) {
		t.Errorf("deleting a record held through the view: err = %v, want it to wait on the lock", err)
	}
	if err := try(sqlMarkRecord); err != nil {
		t.Errorf("marking a record held through the view: %v, want no wait", err)
	}
}

// isLockNotAvailable reports whether err is a statement that gave up
// waiting on a lock.
func isLockNotAvailable(err error) bool {
	pgErr, ok := errors.AsType[*pgconn.PgError](err)
	return ok && pgErr.Code == lockNotAvailable
}

// The files of the meter, the ledger and the charges: they read the
// records only through the view.
var usageFiles = []string{"charge.go", "ledger.go", "meter.go", "usage.go"}

// recordsTable is the records table as a query names it.
var recordsTable = regexp.MustCompile("limes\\.capabilities\\b|schema\\s*\\+\\s*`\\.capabilities\\b")

func TestUsageReadsTheRecordsThroughTheView(t *testing.T) {
	for _, file := range usageFiles {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if loc := recordsTable.FindIndex(src); loc != nil {
			t.Errorf("%s names the records table: %q", file, src[loc[0]:loc[1]])
		}
	}
	if recordsTable.MatchString(sqlForgetErased) {
		t.Errorf("sqlForgetErased reads the records table: %s", sqlForgetErased)
	}
}
