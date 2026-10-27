package pgstore

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5"

	"github.com/oleg-tkachuk/limes"
)

// The two statements a level of limes.mark_revoked runs
// (migrations/015_revoked_flag.sql), its level the parameter: a test holds
// them to the function's own.
const (
	sqlMarkLevel     = `UPDATE limes.capabilities SET revoked = true WHERE id = ANY ($1) AND NOT revoked RETURNING id`
	sqlNextLevel     = `SELECT array_agg(id) FROM limes.capabilities WHERE parent_id = ANY ($1) AND NOT revoked`
	sqlMarkRevokedFn = `SELECT prosrc FROM pg_proc WHERE oid = 'limes.mark_revoked'::regproc`
)

// spaces is any run of white space, read as one.
var spaces = regexp.MustCompile(`\s+`)

// sameStatement reports whether the function's source holds stmt, its
// parameter spelled as the function's variable, white space and the
// function's INTO aside.
func sameStatement(src, stmt string) bool {
	norm := func(s string) string { return strings.TrimSpace(spaces.ReplaceAllString(s, " ")) }
	return strings.Contains(strings.ReplaceAll(norm(src), " INTO level", ""), norm(strings.ReplaceAll(stmt, "$1", "level")))
}

// A revoke marks the subtree below the capability a generation at a time,
// each by key and by parent, over a wide tree and down a deep chain alike:
// neither statement of a level scans the records sequentially, however many
// records a level holds.
func TestRevokeMarksTheSubtreeByIndex(t *testing.T) {
	f := newTenantFixture(t)
	mine := f.mine
	now := time.Now().UTC()
	// The tree among a store of many more records, as the planner meets it.
	if _, err := owner.Exec(t.Context(), seedListing, pgx.QueryExecModeSimpleProtocol, mine, f.theirs, now, planListed); err != nil {
		t.Fatalf("seed: %v", err)
	}
	seed, _, _ := strings.Cut(seedTree, "INSERT INTO limes.revocations")
	seed = strings.Replace(seed, "%d", fmt.Sprint(planDeepChain+1), 1) + "ANALYZE limes.capabilities"
	if _, err := owner.Exec(t.Context(), seed, pgx.QueryExecModeSimpleProtocol, mine, now, planWideChildren); err != nil {
		t.Fatalf("seed: %v", err)
	}
	var src string
	if err := owner.QueryRow(t.Context(), sqlMarkRevokedFn).Scan(&src); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{sqlMarkLevel, sqlNextLevel} {
		if !sameStatement(src, stmt) {
			t.Fatalf("limes.mark_revoked no longer runs %q:\n%s", stmt, src)
		}
	}
	idsOf := func(subject string) []uuid.UUID {
		rows, err := owner.Query(t.Context(), `SELECT id FROM limes.capabilities WHERE tenant_id = $1 AND subject = $2 ORDER BY id`, mine, subject)
		if err != nil {
			t.Fatal(err)
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
		if err != nil {
			t.Fatal(err)
		}
		return ids
	}
	roots, wide, deep := idsOf("root"), idsOf("wide"), idsOf("deep")
	ctx := WithTenant(t.Context(), mine)
	for name, level := range map[string][]uuid.UUID{"the root": roots, "a wide level": wide, "a deep record": deep[len(deep)/2:][:1]} {
		for _, stmt := range []string{sqlMarkLevel, sqlNextLevel} {
			var plan []byte
			err := f.records.inTx(ctx, func(tx pgx.Tx) error {
				return tx.QueryRow(ctx, "EXPLAIN (FORMAT JSON) "+stmt, level).Scan(&plan)
			})
			if err != nil {
				t.Fatalf("%s: EXPLAIN: %v", name, err)
			}
			var nodes []map[string]any
			if err := json.Unmarshal(plan, &nodes); err != nil {
				t.Fatalf("%s: plan: %v", name, err)
			}
			for _, scan := range seqScans(nodes[0]["Plan"].(map[string]any)) {
				if scan == "capabilities" {
					t.Errorf("%s: %s scans the records sequentially:\n%s", name, stmt, plan)
				}
			}
		}
	}
	start := time.Now()
	if _, err := f.records.Revoke(ctx, limes.RevokeRequest{CapabilityID: roots[0]}); err != nil {
		t.Fatal(err)
	}
	t.Logf("a revoke marking %d records took %s", 1+len(wide)+len(deep), time.Since(start))
	if drift := flagDrift(t); len(drift) > 0 {
		t.Errorf("%d records' revoked flag disagrees with IsRevoked after the revoke", len(drift))
	}
}
