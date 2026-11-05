package pgstore

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/meter"
)

// The round trips each meter call makes, on a chain of any depth, when
// Config.DB lends the call a connection of its own
// (docs/adr/0018-fewer-round-trips-per-charge.md): one batch of BEGIN, the
// scope, the erase lock, the erased check, a replay's lookup and the lineage
// held; one creating and locking the counters; one creating and locking the
// tenant's aggregate, a replay looked up again under the locks; one writing
// them all and the ledger row or the hold, then COMMIT. Reserve reads its
// capability's expiry beside them. A Bump under a lease locks the counters
// up to the leaseholder and no tenant's aggregate, as a Bump does, so it
// makes as many. A change that sends one more is a change to the tenant's
// throughput across a network, and fails here.
const (
	chargeRoundTrips     = 4
	bumpRoundTrips       = 3
	reserveRoundTrips    = 5
	leasedBumpRoundTrips = 3
)

// separateTxRoundTrips is what a DB that lends no connection adds to each
// call: BEGIN and COMMIT, each a round trip of its own.
const separateTxRoundTrips = 2

// roundTripLeasedRequests is how many requests the leased leaf holds: more
// than the calls make.
const roundTripLeasedRequests = 10

// roundTripConns is the one connection the calls share: pgx prepares a
// statement the first time a connection runs it, a round trip the calls
// counted here do not make once warmed on it.
const roundTripConns = 1

// roundTripChainDepth is how deep the chain the calls count against is.
const roundTripChainDepth = 3

// Charge, Bump and Reserve make the round trips their constants name, on
// the leaf of a chain, through a pool.
func TestMeterRoundTrips(t *testing.T) {
	pool, sent := latencyPoolOf(t, 0, roundTripConns)
	meterRoundTrips(t, pool, sent, 0)
}

// On a *pgx.Conn, BEGIN and COMMIT go in the calls' batches as they do on
// a pool.
func TestMeterRoundTripsOnAConn(t *testing.T) {
	pool, sent := latencyPoolOf(t, 0, roundTripConns)
	pc, err := pool.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	conn := pc.Hijack()
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	meterRoundTrips(t, conn, sent, 0)
}

// A DB of one's own that lends its connections (ConnLender) batches as a
// pool does.
func TestMeterRoundTripsOnAConnLender(t *testing.T) {
	pool, sent := latencyPoolOf(t, 0, roundTripConns)
	meterRoundTrips(t, lender{pool}, sent, 0)
}

// A wrapper that embeds a pool keeps BEGIN and COMMIT a round trip each,
// its own BeginTx opening every transaction.
func TestMeterRoundTripsOnAWrapperEmbeddingAPool(t *testing.T) {
	pool, sent := latencyPoolOf(t, 0, roundTripConns)
	meterRoundTrips(t, &embeddingPool{Pool: pool}, sent, separateTxRoundTrips)
}

// A DB of one's own that lends no connection keeps BEGIN and COMMIT a round
// trip each.
func TestMeterRoundTripsOnASeparateTxDB(t *testing.T) {
	pool, sent := latencyPoolOf(t, 0, roundTripConns)
	meterRoundTrips(t, beginOnly{pool}, sent, separateTxRoundTrips)
}

// beginOnly is a DB of one's own: its pool's BeginTx and QueryRow, nothing
// else.
type beginOnly struct{ pool *pgxpool.Pool }

func (b beginOnly) BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error) {
	return b.pool.BeginTx(ctx, opts)
}

func (b beginOnly) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return b.pool.QueryRow(ctx, sql, args...)
}

// meterRoundTrips checks the meter calls on db make the round trips their
// constants name and extra more, as sent counts them.
func meterRoundTrips(t *testing.T, db DB, sent *atomic.Int64, extra int64) {
	t.Helper()
	fresh(t)
	records, err := NewStore(Config{DB: db})
	if err != nil {
		t.Fatal(err)
	}
	usage, err := NewUsageStore(Config{DB: db})
	if err != nil {
		t.Fatal(err)
	}
	tenant := uuid.New()
	ctx := WithTenant(t.Context(), tenant)
	subject := meterSubject(roundTripChain(t, records, tenant), tenant)
	leased := meterSubject(roundTripChain(t, records, tenant), tenant)
	if _, err := usage.Lease(ctx, meter.LeaseRequest{Subject: leased, Requests: roundTripLeasedRequests}); err != nil {
		t.Fatal(err)
	}
	calls := map[string]struct {
		want int64
		call func() error
	}{
		"Charge": {chargeRoundTrips, func() error {
			_, err := usage.Charge(ctx, meter.ChargeRequest{Subject: subject, Amount: 1, ExternalRef: uuid.New().String()}, nil)
			return err
		}},
		"Bump": {bumpRoundTrips, func() error {
			_, err := usage.Bump(ctx, meter.BumpRequest{Subject: subject})
			return err
		}},
		"Reserve": {reserveRoundTrips, func() error {
			_, err := usage.Reserve(ctx, meter.ReserveRequest{Subject: subject, Amount: 1})
			return err
		}},
		"Bump under a lease": {leasedBumpRoundTrips, func() error {
			_, err := usage.Bump(ctx, meter.BumpRequest{Subject: leased})
			return err
		}},
	}
	// A first call creates its capability's rows, and prepares its
	// statements on the connection; what is counted is a call after it.
	for name, tc := range calls {
		if err := tc.call(); err != nil {
			t.Fatalf("%s, warming: %v", name, err)
		}
	}
	for name, tc := range calls {
		before := sent.Load()
		if err := tc.call(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got, want := sent.Load()-before, tc.want+extra; got != want {
			t.Errorf("%s made %d round trips, want %d", name, got, want)
		}
	}
	// Warmed and counted, both bumps of the leased leaf went under its lease.
	const leasedBumps = 2
	if l, err := usage.GetLease(ctx, leased.CapabilityID()); err != nil || l.RequestsSpent != leasedBumps {
		t.Errorf("the leased leaf's lease = %+v, %v; want %d requests made under it", l, err, leasedBumps)
	}
}

// roundTripChain records a chain roundTripChainDepth deep in tenant, with
// no ceilings, and returns its leaf.
func roundTripChain(t *testing.T, records *Store, tenant uuid.UUID) uuid.UUID {
	t.Helper()
	ctx := WithTenant(t.Context(), tenant)
	expiry := time.Now().Add(time.Hour)
	var leaf uuid.UUID
	parent := uuid.Nil()
	for i := range roundTripChainDepth {
		c := limes.Capability{
			ID: uuid.New(), ParentID: parent, ExpiresAt: expiry,
			Subject: limes.Principal{Type: limes.PrincipalTypeAgent, TenantID: tenant, Subject: "agent:" + string(rune('a'+i))},
			Caveats: limes.Caveats{Ops: []limes.Op{limes.OpGet}, Requests: limes.RequestCeiling{Unlimited: true}, Budget: limes.BudgetCeiling{Unlimited: true, UnitCode: testUnit}},
		}
		if err := records.Insert(ctx, limes.InsertRequest{Capability: c}); err != nil {
			t.Fatal(err)
		}
		parent, leaf = c.ID, c.ID
	}
	return leaf
}
