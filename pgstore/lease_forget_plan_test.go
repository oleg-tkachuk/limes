package pgstore

import (
	"encoding/json"
	"testing"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5"
)

const (
	// planUsageRows is how many counters seedLeases records, none holding
	// a lease; planLeases is how many more it records that hold one.
	planUsageRows = 50_000
	planLeases    = 10

	// seedLeases records, as the owner, planUsageRows counters of tenant
	// $1 with no lease and planLeases holding one against ancestor $2, and
	// analyzes the counters, so the planner plans for a store where few
	// hold a lease.
	seedLeases = `
INSERT INTO limes.usage (capability_id, tenant_id)
SELECT uuidv7(), $1 FROM generate_series(1, $3::int);
INSERT INTO limes.usage (capability_id, tenant_id, lease_id, lease_expires, lease_ancestors)
SELECT uuidv7(), $1, uuidv7(), now() + interval '1 hour', ARRAY[$2::uuid] FROM generate_series(1, $4::int);
ANALYZE limes.usage`
)

// Forgetting dropped counters among the leases' ancestors reads only the
// counters that hold a lease, through the partial index of them: a row
// with no lease has no ancestors to forget, and a sequential scan of every
// counter would grow with the store rather than with its leases.
func TestForgetLeaseAncestorsReadsOnlyLeases(t *testing.T) {
	fresh(t)
	tenant, gone := newTenant(t), uuid.New()
	if _, err := owner.Exec(t.Context(), seedLeases, pgx.QueryExecModeSimpleProtocol, tenant, gone, planUsageRows, planLeases); err != nil {
		t.Fatalf("seed: %v", err)
	}
	usage, err := NewUsageStore(Config{DB: app, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithAllTenants(t.Context())
	var plan []byte
	err = usage.inTx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, "EXPLAIN (FORMAT JSON) "+sqlForgetLeaseAncestors, []uuid.UUID{gone}).Scan(&plan)
	})
	if err != nil {
		t.Fatalf("EXPLAIN: %v", err)
	}
	var nodes []map[string]any
	if err := json.Unmarshal(plan, &nodes); err != nil {
		t.Fatalf("plan: %v", err)
	}
	for _, scan := range seqScans(nodes[0]["Plan"].(map[string]any)) {
		if scan == "usage" {
			t.Errorf("forgetting a lease ancestor scans every counter:\n%s", plan)
		}
	}
	if err := usage.forgetLeaseAncestors(ctx, []uuid.UUID{gone}); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := owner.QueryRow(t.Context(), `SELECT count(*) FROM limes.usage WHERE lease_ancestors && $1`, []uuid.UUID{gone}).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Errorf("%d leases still name a forgotten ancestor, want 0", left)
	}
}
