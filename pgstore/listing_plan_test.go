package pgstore

import (
	"encoding/json"
	"fmt"
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
FROM limes.capabilities WHERE depth = 0 AND hashtext(id::text) % 50 = 0
ON CONFLICT (capability_id) DO NOTHING;

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

// seedPage records, as the owner, planPageRecords records for tenant $1, a
// half of them revoked: every other one (interleaved), or the older half
// (block), whose records an ascending listing of the active ones would pass
// over first.
const (
	planPageRecords = 10000
	seedPage        = `
INSERT INTO limes.capabilities (id, parent_id, tenant_id, principal_type, subject, expires_at, depth, capability, issued_by)
SELECT uuidv7(), NULL, $1, 'agent', 'subject:' || (i % 100), $2::timestamptz + interval '1 hour', 0, '{}', '{}'
FROM generate_series(1, $3::int) i ORDER BY i;
INSERT INTO limes.revocations (capability_id, tenant_id, revoked_at, note, actor, cascaded)
SELECT id, tenant_id, $2, 'plan', 'test', false
FROM (SELECT id, tenant_id, row_number() OVER (ORDER BY id) i FROM limes.capabilities) c
WHERE CASE $4 WHEN 'block' THEN i <= $3::int / 2 ELSE i % 2 = 0 END;
ANALYZE limes.capabilities;
ANALYZE limes.revocations`
)

// scanned is the rows a plan read from limes.capabilities, those it kept and
// those its filters removed, and whether it walked anything recursively: a
// recursive union that yielded a row. A listing's walk up to a revoking
// ancestor is in every plan, and runs for no record revoked by its own
// entry or not revoked at all.
func scanned(node map[string]any) (rows float64, recursive bool) {
	r, _ := node["Actual Rows"].(float64)
	loops, _ := node["Actual Loops"].(float64)
	if node["Relation Name"] == "capabilities" {
		removed, _ := node["Rows Removed by Filter"].(float64)
		rows += (r + removed) * loops
	}
	if node["Node Type"] == "Recursive Union" && r*loops > 0 {
		recursive = true
	}
	children, _ := node["Plans"].([]any)
	for _, c := range children {
		r, rec := scanned(c.(map[string]any))
		rows, recursive = rows+r, recursive || rec
	}
	return rows, recursive
}

// A page of a listing reads the records it lists and not many more,
// however many of the tenant's records are revoked and wherever they fall:
// of planPageRecords, half revoked, a page of active records or of revoked
// ones reads at most two of its LIMIT from limes.capabilities, and walks no
// revocation.
func TestListRecordsPageReadsItsLimit(t *testing.T) {
	const limit = limes.DefaultListLimit + 1
	for _, layout := range []string{"interleaved", "block"} {
		fresh(t)
		mine := newTenant(t)
		now := time.Now().UTC()
		if _, err := owner.Exec(t.Context(), seedPage, pgx.QueryExecModeSimpleProtocol, mine, now, planPageRecords, layout); err != nil {
			t.Fatalf("seed: %v", err)
		}
		census, err := NewCensus(Config{DB: app, Now: func() time.Time { return now }})
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range []limes.RecordDisposition{limes.RecordDispositionActive, limes.RecordDispositionRevoked} {
			for _, order := range []limes.ListOrder{limes.ListOrderAscending, limes.ListOrderDescending} {
				name := fmt.Sprintf("%s, %s, %s", layout, d, order)
				req := limes.ListRecordsRequest{TenantID: mine, Dispositions: []limes.RecordDisposition{d}, Order: order}
				query, args := listRecordsSQL(req, now, nil, limit)
				var plan []byte
				err := census.inTenantReadTx(WithTenant(t.Context(), mine), mine, func(tx pgx.Tx) error {
					return tx.QueryRow(t.Context(), "EXPLAIN (ANALYZE, FORMAT JSON) "+query, args...).Scan(&plan)
				})
				if err != nil {
					t.Fatalf("%s: EXPLAIN: %v", name, err)
				}
				var nodes []map[string]any
				if err := json.Unmarshal(plan, &nodes); err != nil {
					t.Fatalf("%s: plan: %v", name, err)
				}
				rows, recursive := scanned(nodes[0]["Plan"].(map[string]any))
				t.Logf("%s: %.0f records read in %.2f ms", name, rows, nodes[0]["Execution Time"])
				if recursive || rows > 2*limit {
					t.Errorf("%s: read %.0f records (at most %d), walked recursively %v:\n%s", name, rows, 2*limit, recursive, plan)
				}
			}
		}
	}
}

// seedTree records, as the owner, a tree for tenant $1 under one revoked
// root: planWideChildren children of the root, and a chain of planDeepChain
// generations below the first of them. Each of the tree's records is
// revoked only through the root.
const (
	planWideChildren = 2000
	planDeepChain    = limes.MaxDelegationDepth - 2
	seedTree         = `
WITH root AS (
    INSERT INTO limes.capabilities (id, parent_id, tenant_id, principal_type, subject, expires_at, depth, capability, issued_by)
    VALUES (uuidv7(), NULL, $1, 'agent', 'root', $2::timestamptz + interval '1 hour', 0, '{}', '{}')
    RETURNING id
)
INSERT INTO limes.capabilities (id, parent_id, tenant_id, principal_type, subject, expires_at, depth, capability, issued_by)
SELECT uuidv7(), root.id, $1, 'agent', 'wide', $2::timestamptz + interval '1 hour', 1, '{}', '{}'
FROM root, generate_series(1, $3::int);
DO $$
DECLARE parent uuid := (SELECT id FROM limes.capabilities WHERE subject = 'wide' ORDER BY id LIMIT 1);
DECLARE child uuid;
BEGIN
    FOR generation IN 2..%d LOOP
        child := uuidv7();
        INSERT INTO limes.capabilities (id, parent_id, tenant_id, principal_type, subject, expires_at, depth, capability, issued_by)
        SELECT child, id, tenant_id, 'agent', 'deep', expires_at, generation, '{}', '{}' FROM limes.capabilities WHERE id = parent;
        parent := child;
    END LOOP;
END $$;
INSERT INTO limes.revocations (capability_id, tenant_id, revoked_at, note, actor, cascaded)
SELECT id, tenant_id, $2, 'plan', 'test', false FROM limes.capabilities WHERE subject = 'root';
ANALYZE limes.capabilities;
ANALYZE limes.revocations`
)

// walked is the records a plan's walks up read from limes.capabilities —
// the scans under a recursive union — and the relations they scan
// sequentially.
func walked(node map[string]any, inWalk bool) (rows float64, seq []string) {
	inWalk = inWalk || node["Node Type"] == "Recursive Union"
	if inWalk && node["Relation Name"] == "capabilities" {
		r, _ := node["Actual Rows"].(float64)
		removed, _ := node["Rows Removed by Filter"].(float64)
		loops, _ := node["Actual Loops"].(float64)
		rows += (r + removed) * loops
		if node["Node Type"] == "Seq Scan" {
			seq = append(seq, "capabilities")
		}
	}
	children, _ := node["Plans"].([]any)
	for _, c := range children {
		r, s := walked(c.(map[string]any), inWalk)
		rows, seq = rows+r, append(seq, s...)
	}
	return rows, seq
}

// seedStepped records under tenant %[1]s's root a chain of %[2]d
// generations, subject "stepped", each even generation with an entry of its
// own.
const seedStepped = `
DO $$
DECLARE parent uuid := (SELECT id FROM limes.capabilities WHERE tenant_id = '%[1]s' AND subject = 'root');
DECLARE child uuid;
BEGIN
    FOR generation IN 1..%[2]d LOOP
        child := uuidv7();
        INSERT INTO limes.capabilities (id, parent_id, tenant_id, principal_type, subject, expires_at, depth, capability, issued_by)
        SELECT child, id, tenant_id, 'agent', 'stepped', expires_at, generation, '{}', '{}' FROM limes.capabilities WHERE id = parent;
        IF generation %% 2 = 0 THEN
            INSERT INTO limes.revocations (capability_id, tenant_id, revoked_at, note, actor, cascaded)
            VALUES (child, '%[1]s', now(), 'step', 'test', false);
        END IF;
        parent := child;
    END LOOP;
END $$;
ANALYZE limes.capabilities;
ANALYZE limes.revocations`

// A listing of records revoked through an ancestor walks up to it a parent
// at a time by key, over a wide tree and down a deep chain alike: the walk
// scans no records sequentially, and reads a parent for each generation it
// climbs — one for each of the wide tree's children, and for each record of
// the chain every generation above it.
func TestListRecordsWalksUpByKey(t *testing.T) {
	const limit = limes.DefaultListLimit + 1
	fresh(t)
	mine := newTenant(t)
	now := time.Now().UTC()
	seed := strings.Replace(seedTree, "%d", fmt.Sprint(planDeepChain+1), 1)
	if _, err := owner.Exec(t.Context(), seed, pgx.QueryExecModeSimpleProtocol, mine, now, planWideChildren); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// A chain whose every other generation has an entry of its own: each
	// record between them finds its parent's at the first step.
	if _, err := owner.Exec(t.Context(), fmt.Sprintf(seedStepped, mine, planDeepChain), pgx.QueryExecModeSimpleProtocol); err != nil {
		t.Fatalf("seed the stepped chain: %v", err)
	}
	census, err := NewCensus(Config{DB: app, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	revoked := []limes.RecordDisposition{limes.RecordDispositionRevoked}
	for name, tc := range map[string]struct {
		req              limes.ListRecordsRequest
		minRows, maxRows float64
	}{
		// The root, the children's parent, has the entry: no record read.
		"wide": {limes.ListRecordsRequest{TenantID: mine, Subject: "wide", Dispositions: revoked}, 0, 0},
		"deep": {limes.ListRecordsRequest{TenantID: mine, Subject: "deep", Dispositions: revoked}, 1, limit * (planDeepChain + 1)},
		// Stopped at the nearest entry, never walked on to the root.
		"stepped": {limes.ListRecordsRequest{TenantID: mine, Subject: "stepped", Dispositions: revoked}, 0, 0},
	} {
		query, args := listRecordsSQL(tc.req, now, nil, limit)
		var plan []byte
		err := census.inTenantReadTx(WithTenant(t.Context(), mine), mine, func(tx pgx.Tx) error {
			return tx.QueryRow(t.Context(), "EXPLAIN (ANALYZE, FORMAT JSON) "+query, args...).Scan(&plan)
		})
		if err != nil {
			t.Fatalf("%s: EXPLAIN: %v", name, err)
		}
		var nodes []map[string]any
		if err := json.Unmarshal(plan, &nodes); err != nil {
			t.Fatalf("%s: plan: %v", name, err)
		}
		rows, seq := walked(nodes[0]["Plan"].(map[string]any), false)
		t.Logf("%s: the walks read %.0f records in %.2f ms", name, rows, nodes[0]["Execution Time"])
		if rows < tc.minRows || rows > tc.maxRows || len(seq) > 0 {
			t.Errorf("%s: the walks read %.0f records (%.0f to %.0f), %d sequentially:\n%s", name, rows, tc.minRows, tc.maxRows, len(seq), plan)
		}
		page, err := census.ListRecords(WithTenant(t.Context(), mine), tc.req)
		if err != nil || len(page.Items) == 0 {
			t.Fatalf("%s: ListRecords = %d items, %v", name, len(page.Items), err)
		}
		for _, l := range page.Items {
			if l.StoppedBy == nil || l.StoppedBy.Revocation.Note == "" {
				t.Fatalf("%s: %s stopped by %+v, want an entry", name, l.Record.Capability.ID, l.StoppedBy)
			}
		}
	}
}
