package pgstore

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5"

	"github.com/oleg-tkachuk/limes"
)

const (
	// spreadTenants is how many tenants one client holds a capability in,
	// past what one batch revokes in.
	spreadTenants = maxTenantsPerBatch + 2
	// lockWait is how long a revocation across tenants is given while a
	// tenant's revoke lock is held: long enough to have taken it if it did
	// not wait.
	lockWait = 500 * time.Millisecond
	// planRecords is how many records the plan check seeds across tenants,
	// one in planEvery the client's and the session's.
	planRecords = 40000
	planEvery   = 1000
)

// everyTenantFixture is a store, and a client's capabilities in tenants of
// their own.
type everyTenantFixture struct {
	records *Store
	client  string
	tenants []uuid.UUID
	ids     []uuid.UUID
}

// newEveryTenantFixture records one capability of a fresh client in each of
// n fresh tenants.
func newEveryTenantFixture(t *testing.T, n int) everyTenantFixture {
	t.Helper()
	fresh(t)
	clock := newFakeClock()
	records, err := NewStore(Config{DB: app, Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	f := everyTenantFixture{records: records, client: "client-" + uuid.New().String()}
	for range n {
		tenant := uuid.New()
		c := limes.Capability{
			ID: uuid.New(), ExpiresAt: clock.Now().Add(rlsTTL), ClientID: f.client,
			Subject: limes.Principal{Type: limes.PrincipalTypeAgent, TenantID: tenant, Subject: "agent"},
			Caveats: limes.Caveats{Ops: []limes.Op{limes.OpGet}, Requests: limes.RequestCeiling{Unlimited: true}},
		}
		if err := records.Insert(WithTenant(t.Context(), tenant), limes.InsertRequest{Capability: c}); err != nil {
			t.Fatal(err)
		}
		f.tenants, f.ids = append(f.tenants, tenant), append(f.ids, c.ID)
	}
	return f
}

// request is a revocation of f's client across tenants.
func (f everyTenantFixture) request() limes.RevokeMatchingRequest {
	return limes.RevokeMatchingRequest{AllTenants: true, ClientID: f.client, Note: "client deleted", Actor: "test"}
}

// revokedCount is how many of f's capabilities verify as revoked.
func (f everyTenantFixture) revokedCount(t *testing.T) int {
	t.Helper()
	n := 0
	for _, id := range f.ids {
		r, err := f.records.IsRevoked(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		if r {
			n++
		}
	}
	return n
}

// A batch across tenants revokes in at most maxTenantsPerBatch tenants,
// whatever its limit, and says more may match; the next takes the rest.
func TestRevokeAcrossTenantsTakesABoundedNumberOfTenants(t *testing.T) {
	f := newEveryTenantFixture(t, spreadTenants)
	ctx := WithAllTenants(t.Context())
	first, err := f.records.RevokeMatching(ctx, f.request())
	if err != nil {
		t.Fatal(err)
	}
	if first.Revoked != maxTenantsPerBatch || len(first.Tenants) != maxTenantsPerBatch || !first.More {
		t.Fatalf("first batch = %d revoked in %d tenants, more %v; want %d in %d and more",
			first.Revoked, len(first.Tenants), first.More, maxTenantsPerBatch, maxTenantsPerBatch)
	}
	rest, err := f.records.RevokeMatching(ctx, f.request())
	if err != nil {
		t.Fatal(err)
	}
	if want := spreadTenants - maxTenantsPerBatch; rest.Revoked != int64(want) || rest.More {
		t.Fatalf("second batch = %+v, want %d revoked and no more", rest, want)
	}
	if got := slices.Concat(first.Tenants, rest.Tenants); !slices.IsSortedFunc(got, compareUUID) || len(got) != spreadTenants {
		t.Errorf("the batches' tenants %v are not every tenant once, in order", got)
	}
	if n := f.revokedCount(t); n != spreadTenants {
		t.Errorf("%d of %d capabilities revoked", n, spreadTenants)
	}
}

// A revocation across tenants takes each tenant's revoke lock before it
// writes, as a revoke in one tenant does: while another transaction holds
// one tenant's, it waits, and revokes nothing in any tenant.
func TestRevokeAcrossTenantsWaitsForEachTenantsRevokeLock(t *testing.T) {
	f := newEveryTenantFixture(t, 2)
	held, err := owner.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Rollback(context.WithoutCancel(t.Context())) }()
	if _, err := held.Exec(t.Context(), sqlTakeTenantLock, revokeLockClass, f.tenants[1]); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(WithAllTenants(t.Context()), lockWait)
	defer cancel()
	if _, err := f.records.RevokeMatching(ctx, f.request()); !errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
		t.Fatalf("RevokeMatching under a held revoke lock = %v, want it to wait", err)
	}
	if n := f.revokedCount(t); n != 0 {
		t.Fatalf("%d capabilities revoked while a tenant's revoke lock was held", n)
	}
	if err := held.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	got, err := f.records.RevokeMatching(WithAllTenants(t.Context()), f.request())
	if err != nil || got.Revoked != 2 {
		t.Fatalf("RevokeMatching once the lock is free = %+v, %v; want 2 revoked", got, err)
	}
}

// sqlSeedEveryTenant records, as the owner, planRecords records each of a
// tenant of its own, one in planEvery of them under client $2 and session
// $3, the rest under others, and revokes half of them, as a store that
// revokes as often as it lets tokens expire holds.
const sqlSeedEveryTenant = `
INSERT INTO limes.capabilities (id, parent_id, tenant_id, principal_type, subject, expires_at, depth, capability, issued_by, client_id, session)
SELECT uuidv7(), NULL, gen_random_uuid(), 'agent', 'subject:' || i, $1::timestamptz + interval '1 hour', 0, '{}', '{}',
       CASE WHEN i % $5::int = 0 THEN $2 ELSE 'client:' || (i % 500) END,
       CASE WHEN i % $5::int = 0 THEN $3 ELSE 'sid:' || i END
FROM generate_series(1, $4::int) i;

INSERT INTO limes.revocations (capability_id, tenant_id, revoked_at, note, actor, cascaded)
SELECT id, tenant_id, $1::timestamptz, 'plan', 'test', false
FROM limes.capabilities WHERE hashtext(id::text) % 2 = 0;

ANALYZE limes.capabilities;
ANALYZE limes.revocations`

// The selection of a batch across tenants, by client or by session, reads
// tens of thousands of records of as many tenants through an index, under
// the runtime role's row-level security.
func TestRevokeAcrossTenantsSelectsByIndex(t *testing.T) {
	fresh(t)
	now := time.Now().UTC()
	client, session := "client-"+uuid.New().String(), "sid-"+uuid.New().String()
	if _, err := owner.Exec(t.Context(), sqlSeedEveryTenant, pgx.QueryExecModeSimpleProtocol, now, client, session, planRecords, planEvery); err != nil {
		t.Fatalf("seed: %v", err)
	}
	records, err := NewStore(Config{DB: app, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithAllTenants(t.Context())
	for name, req := range map[string]limes.RevokeMatchingRequest{
		"by client":  {AllTenants: true, ClientID: client},
		"by session": {AllTenants: true, Session: session},
	} {
		sql, args := selectEveryTenantSQL(req)
		var plan []byte
		err := records.inEveryTenantTx(ctx, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, "EXPLAIN (FORMAT JSON) "+sql, args...).Scan(&plan)
		})
		if err != nil {
			t.Fatalf("%s: EXPLAIN: %v", name, err)
		}
		var nodes []map[string]any
		if err := json.Unmarshal(plan, &nodes); err != nil {
			t.Fatalf("%s: plan: %v", name, err)
		}
		if scans := seqScans(nodes[0]["Plan"].(map[string]any)); len(scans) > 0 {
			t.Errorf("%s: the selection scans %v sequentially:\n%s", name, scans, plan)
		}
	}
}

// compareUUID orders ids bytewise, as PostgreSQL orders uuid.
func compareUUID(a, b uuid.UUID) int {
	for i := range a {
		if a[i] != b[i] {
			return int(a[i]) - int(b[i])
		}
	}
	return 0
}

// firstTenants keeps the matches of the first maxTenantsPerBatch tenants and
// says when it left any out.
func TestFirstTenantsCutsAtTheBound(t *testing.T) {
	var batch []selected
	for range maxTenantsPerBatch + 1 {
		tenant := uuid.New()
		batch = append(batch, selected{ID: uuid.New(), Tenant: tenant}, selected{ID: uuid.New(), Tenant: tenant})
	}
	ids, tenants, cut := firstTenants(batch)
	if len(tenants) != maxTenantsPerBatch || len(ids) != 2*maxTenantsPerBatch || !cut {
		t.Errorf("firstTenants = %d ids in %d tenants, cut %v; want %d in %d, cut", len(ids), len(tenants), cut, 2*maxTenantsPerBatch, maxTenantsPerBatch)
	}
	ids, tenants, cut = firstTenants(batch[:2])
	if len(tenants) != 1 || len(ids) != 2 || cut {
		t.Errorf("firstTenants of one tenant = %d ids in %d tenants, cut %v; want 2 in 1, not cut", len(ids), len(tenants), cut)
	}
}

// A census across tenants by client or by session — the preview of a
// revocation across tenants — reads the records through an index, as the
// revocation's selection does.
func TestCensusAcrossTenantsBySelectorReadsByIndex(t *testing.T) {
	fresh(t)
	now := time.Now().UTC()
	client, session := "client-"+uuid.New().String(), "sid-"+uuid.New().String()
	if _, err := owner.Exec(t.Context(), sqlSeedEveryTenant, pgx.QueryExecModeSimpleProtocol, now, client, session, planRecords, planEvery); err != nil {
		t.Fatalf("seed: %v", err)
	}
	census, err := NewCensus(Config{DB: app, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithAllTenantsReadOnly(t.Context())
	for name, req := range map[string]limes.CountRecordsRequest{
		"by client":  {AllTenants: true, ByTenant: true, ClientID: client},
		"by session": {AllTenants: true, ByTenant: true, Session: session},
	} {
		args, err := census.countArgs(req)
		if err != nil {
			t.Fatal(err)
		}
		sql, args := countSQL(req, append(args, limes.PageLimit(0)+1))
		var plan []byte
		err = census.inEveryTenantReadTx(ctx, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, "EXPLAIN (FORMAT JSON) "+sql, args...).Scan(&plan)
		})
		if err != nil {
			t.Fatalf("%s: EXPLAIN: %v", name, err)
		}
		var nodes []map[string]any
		if err := json.Unmarshal(plan, &nodes); err != nil {
			t.Fatalf("%s: plan: %v", name, err)
		}
		if scans := seqScans(nodes[0]["Plan"].(map[string]any)); len(scans) > 0 {
			t.Errorf("%s: the census scans %v sequentially:\n%s", name, scans, plan)
		}
	}
}

// sqlSeedRuns records, as the owner, planRecords records of tenant $1, one
// in planEvery of them of agent run $2, the rest of runs of their own, and
// revokes half of them.
const sqlSeedRuns = `
INSERT INTO limes.capabilities (id, parent_id, tenant_id, principal_type, subject, expires_at, depth, capability, issued_by, run_id)
SELECT uuidv7(), NULL, $1, 'agent', 'subject:' || i, $3::timestamptz + interval '1 hour', 0, '{}', '{}',
       CASE WHEN i % $5::int = 0 THEN $2::uuid ELSE gen_random_uuid() END
FROM generate_series(1, $4::int) i;

INSERT INTO limes.revocations (capability_id, tenant_id, revoked_at, note, actor, cascaded)
SELECT id, tenant_id, $3::timestamptz, 'plan', 'test', false
FROM limes.capabilities WHERE hashtext(id::text) % 2 = 0;

ANALYZE limes.capabilities;
ANALYZE limes.revocations`

// A revocation by agent run, and its preview, read a tenant of tens of
// thousands of records through migration 030's index.
func TestRevokeByRunReadsByIndex(t *testing.T) {
	fresh(t)
	now := time.Now().UTC()
	tenant, run := newTenant(t), uuid.New()
	if _, err := owner.Exec(t.Context(), sqlSeedRuns, pgx.QueryExecModeSimpleProtocol, tenant, run, now, planRecords, planEvery); err != nil {
		t.Fatalf("seed: %v", err)
	}
	records, err := NewStore(Config{DB: app, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	census, err := NewCensus(Config{DB: app, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithTenant(t.Context(), tenant)
	revokeSQL, revokeArgs := revokeMatchingSQL(limes.RevokeMatchingRequest{TenantID: tenant, RunID: run}, now)
	countReq := limes.CountRecordsRequest{TenantID: tenant, RunID: run}
	countArgs, err := census.countArgs(countReq)
	if err != nil {
		t.Fatal(err)
	}
	countSQLText, countArgs := countSQL(countReq, append(countArgs, limes.PageLimit(0)+1))
	listSQL, listArgs := listRecordsSQL(limes.ListRecordsRequest{TenantID: tenant, RunID: run}, now, nil, limes.PageLimit(0)+1)
	for name, q := range map[string]struct {
		sql  string
		args []any
	}{
		"the revocation": {revokeSQL, revokeArgs},
		"the count":      {countSQLText, countArgs},
		"the listing":    {listSQL, listArgs},
	} {
		var plan []byte
		err := records.inTx(ctx, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, "EXPLAIN (FORMAT JSON) "+q.sql, q.args...).Scan(&plan)
		})
		if err != nil {
			t.Fatalf("%s: EXPLAIN: %v", name, err)
		}
		var nodes []map[string]any
		if err := json.Unmarshal(plan, &nodes); err != nil {
			t.Fatalf("%s: plan: %v", name, err)
		}
		if scans := seqScans(nodes[0]["Plan"].(map[string]any)); slices.Contains(scans, "capabilities") {
			t.Errorf("%s scans the records sequentially:\n%s", name, plan)
		}
	}
}
