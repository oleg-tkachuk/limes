package pgstore

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/oleg-tkachuk/limes/meter"
)

// planCharges is how many charges seedCharges records, spread over twice
// the retention, so a purge finds old and young ones among many.
const planCharges = 50_000

// seedCharges records, as the owner, planCharges charges of tenant $1 taken
// from $2 back over twice MinChargeRetention ($3 seconds), and analyzes the
// ledger, so the planner plans for a ledger of that size.
const seedCharges = `
INSERT INTO limes.charges (id, capability_id, tenant_id, amount, unit_code, op, actor, at, ancestors, copies, overrun, tenant_period)
SELECT uuidv7(), uuidv7(), $1, 1, 'USD', 'get', 'plan', $2::timestamptz - make_interval(secs => $3::double precision * 2 * i / $4::int), '{}', '{}', false, 0
FROM generate_series(1, $4::int) i;
ANALYZE limes.charges`

// A purge across tenants reads the ledger by when each charge was taken,
// oldest first, and stops at its limit: no sequential scan of the ledger,
// however many charges it holds.
func TestPurgeChargesReadsByTime(t *testing.T) {
	fresh(t)
	tenant := newTenant(t)
	now := time.Now().UTC()
	if _, err := owner.Exec(t.Context(), seedCharges, pgx.QueryExecModeSimpleProtocol, tenant, now, meter.MinChargeRetention.Seconds(), planCharges); err != nil {
		t.Fatalf("seed: %v", err)
	}
	ledger, err := NewLedger(Config{DB: app, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	var plan []byte
	err = ledger.inTx(WithAllTenants(t.Context()), func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), "EXPLAIN (FORMAT JSON) "+sqlPurgeCharges, now.Add(-meter.MinChargeRetention), int64(meter.HousekeepingLimit(0))).Scan(&plan)
	})
	if err != nil {
		t.Fatalf("EXPLAIN: %v", err)
	}
	var nodes []map[string]any
	if err := json.Unmarshal(plan, &nodes); err != nil {
		t.Fatalf("plan: %v", err)
	}
	for _, scan := range seqScans(nodes[0]["Plan"].(map[string]any)) {
		if scan == "charges" {
			t.Errorf("the purge scans the ledger sequentially:\n%s", plan)
		}
	}
	r, err := ledger.PurgeCharges(WithAllTenants(t.Context()), meter.PurgeChargesRequest{})
	if err != nil || r.Purged != int64(meter.HousekeepingLimit(0)) {
		t.Errorf("PurgeCharges = %+v, %v; want a full batch of the old half", r, err)
	}
}
