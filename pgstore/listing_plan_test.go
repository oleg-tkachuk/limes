package pgstore

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/oleg-tkachuk/limes"
)

// seedListing records, as the owner, records of tenant mine and of another
// tenant in bulk — of three principal types and many subjects, some
// expired, some delegated, some revoked — and analyzes the tables, so the
// planner plans for a tenant of that size.
const seedListing = `
INSERT INTO limes.capabilities (id, parent_id, tenant_id, principal_type, subject, expires_at, depth, capability, issued_by)
SELECT uuidv7(), NULL, t.tenant, (ARRAY['agent', 'user', 'service'])[1 + i % 3], 'subject:' || (i % 2000),
       $3::timestamptz + CASE WHEN i % 10 = 0 THEN interval '-1 hour' ELSE interval '1 hour' END,
       0, '{}', '{}'
FROM (VALUES ($1::uuid, $4::int), ($2::uuid, $4::int * 3)) t (tenant, n), generate_series(1, t.n) i;

INSERT INTO limes.capabilities (id, parent_id, tenant_id, principal_type, subject, expires_at, depth, capability, issued_by)
SELECT uuidv7(), p.id, p.tenant_id, p.principal_type, p.subject, p.expires_at, 1, '{}', '{}'
FROM (SELECT * FROM limes.capabilities ORDER BY id LIMIT $4::int / 10) p;

INSERT INTO limes.revocations (capability_id, tenant_id, revoked_at, note, actor, cascaded)
SELECT id, tenant_id, $3::timestamptz, 'plan', 'test', false
FROM limes.capabilities WHERE depth = 0 AND hashtext(id::text) % 50 = 0;

ANALYZE limes.capabilities;
ANALYZE limes.revocations`

// planListed is how many records seedListing records for the tenant listed.
const planListed = 40000

// Every shape of ListRecords request reads a tenant of tens of thousands of
// records through an index: no sequential scan of the records or of the
// revocation entries, under the runtime role's row-level security.
func TestListRecordsReadsByIndex(t *testing.T) {
	fresh(t)
	mine, theirs := newTenant(t), newTenant(t)
	now := time.Now().UTC()
	if _, err := owner.Exec(t.Context(), seedListing, pgx.QueryExecModeSimpleProtocol, mine, theirs, now, planListed); err != nil {
		t.Fatalf("seed: %v", err)
	}
	census, err := NewCensus(Config{DB: app, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	after := uuid.Must(uuid.NewV7())
	active := []limes.RecordDisposition{limes.RecordDispositionActive}
	for name, req := range map[string]limes.ListRecordsRequest{
		"the tenant":                  {},
		"the tenant, newest first":    {Order: limes.ListOrderDescending},
		"active, newest first":        {Dispositions: active, Order: limes.ListOrderDescending},
		"a principal type":            {PrincipalType: limes.PrincipalTypeAgent, Order: limes.ListOrderDescending},
		"a subject":                   {PrincipalType: limes.PrincipalTypeUser, Subject: "subject:7"},
		"a subject of any type":       {Subject: "subject:7", Order: limes.ListOrderDescending},
		"a subject prefix":            {SubjectPrefix: "subject:19", Dispositions: active, Order: limes.ListOrderDescending},
		"revoked":                     {Dispositions: []limes.RecordDisposition{limes.RecordDispositionRevoked}},
		"a type and a subject, paged": {PrincipalType: limes.PrincipalTypeService, Subject: "subject:5", Order: limes.ListOrderDescending, Cursor: "after"},
		"the tenant, paged":           {Cursor: "after"},
	} {
		req.TenantID = mine
		var cursor *uuid.UUID
		if req.Cursor != "" {
			cursor = &after
		}
		query, args := listRecordsSQL(req, now, cursor, limes.DefaultListLimit+1)
		var plan []byte
		err := census.inTenantReadTx(WithTenant(t.Context(), mine), mine, func(tx pgx.Tx) error {
			return tx.QueryRow(t.Context(), "EXPLAIN (FORMAT JSON) "+query, args...).Scan(&plan)
		})
		if err != nil {
			t.Fatalf("%s: EXPLAIN: %v", name, err)
		}
		var nodes []map[string]any
		if err := json.Unmarshal(plan, &nodes); err != nil {
			t.Fatalf("%s: plan: %v", name, err)
		}
		for _, scan := range seqScans(nodes[0]["Plan"].(map[string]any)) {
			if strings.HasPrefix(scan, "capabilities") || strings.HasPrefix(scan, "revocations") {
				t.Errorf("%s: a sequential scan of %s:\n%s", name, scan, plan)
			}
		}
	}
}

// seqScans is the relations a plan node, or a node under it, scans
// sequentially.
func seqScans(node map[string]any) []string {
	var out []string
	if node["Node Type"] == "Seq Scan" {
		rel, _ := node["Relation Name"].(string)
		out = append(out, rel)
	}
	children, _ := node["Plans"].([]any)
	for _, c := range children {
		out = append(out, seqScans(c.(map[string]any))...)
	}
	return out
}
