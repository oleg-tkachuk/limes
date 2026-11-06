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

// chargeWithCallbackRoundTrips is a Charge whose onCharged runs: the write
// batch goes before it without COMMIT, so that onCharged's writes see the
// charge's row, and COMMIT after it, with the check that onCharged left the
// transaction open. The locks are held to COMMIT with or without it.
const chargeWithCallbackRoundTrips = chargeRoundTrips + 1

// The calls that end what another made, each with COMMIT in its write
// batch. Settle locks the hold, then reads the lineage, then locks as a
// charge does; Release locks the hold, then the counters and the tenant's
// aggregate. Refund reads the charge, locks its counters, then locks the
// charge, a keyed refund's record read beside it and written with the
// counters. A keyed Reserve looks its id up first, before the lineage.
const (
	settleRoundTrips       = 5
	releaseRoundTrips      = 4
	refundRoundTrips       = 5
	keyedRefundRoundTrips  = 5
	keyedReserveRoundTrips = reserveRoundTrips + 1
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
	// held is the hold the next Settle or Release ends, and charged the
	// charge the next Refund refunds: what their setup makes, uncounted.
	var held meter.Reservation
	var charged meter.ChargeReceipt
	hold := func() (err error) {
		held, err = usage.Reserve(ctx, meter.ReserveRequest{Subject: subject, Amount: 1})
		return err
	}
	charge := func() (err error) {
		charged, err = usage.Charge(ctx, meter.ChargeRequest{Subject: subject, Amount: 1}, nil)
		return err
	}
	calls := map[string]struct {
		want  int64
		call  func() error
		setup func() error
	}{
		"Charge": {chargeRoundTrips, func() error {
			_, err := usage.Charge(ctx, meter.ChargeRequest{Subject: subject, Amount: 1, ExternalRef: uuid.New().String()}, nil)
			return err
		}, nil},
		"Charge with onCharged": {chargeWithCallbackRoundTrips, func() error {
			_, err := usage.Charge(ctx, meter.ChargeRequest{Subject: subject, Amount: 1, ExternalRef: uuid.New().String()}, noopCharged)
			return err
		}, nil},
		"Bump": {bumpRoundTrips, func() error {
			_, err := usage.Bump(ctx, meter.BumpRequest{Subject: subject})
			return err
		}, nil},
		"Reserve": {reserveRoundTrips, func() error {
			_, err := usage.Reserve(ctx, meter.ReserveRequest{Subject: subject, Amount: 1})
			return err
		}, nil},
		"Bump under a lease": {leasedBumpRoundTrips, func() error {
			_, err := usage.Bump(ctx, meter.BumpRequest{Subject: leased})
			return err
		}, nil},
		"keyed Reserve": {keyedReserveRoundTrips, func() error {
			_, err := usage.Reserve(ctx, meter.ReserveRequest{Subject: subject, Amount: 1, ReservationID: uuid.New()})
			return err
		}, nil},
		"Settle": {settleRoundTrips, func() error {
			_, err := usage.Settle(ctx, meter.SettleRequest{Subject: subject, ReservationID: held.ID, Amount: 1}, nil)
			return err
		}, hold},
		"Release": {releaseRoundTrips, func() error {
			return usage.Release(ctx, meter.ReleaseRequest{Subject: subject, ReservationID: held.ID})
		}, hold},
		"Refund": {refundRoundTrips, func() error {
			_, err := usage.Refund(ctx, meter.RefundRequest{ChargeID: charged.ChargeID, Amount: 1})
			return err
		}, charge},
		"keyed Refund": {keyedRefundRoundTrips, func() error {
			_, err := usage.Refund(ctx, meter.RefundRequest{ChargeID: charged.ChargeID, Amount: 1, RefundID: uuid.New()})
			return err
		}, charge},
	}
	setUp := func(name string, setup func() error) {
		if setup == nil {
			return
		}
		if err := setup(); err != nil {
			t.Fatalf("%s, setting up: %v", name, err)
		}
	}
	// A first call creates its capability's rows, and prepares its
	// statements on the connection; what is counted is a call after it.
	for name, tc := range calls {
		setUp(name, tc.setup)
		if err := tc.call(); err != nil {
			t.Fatalf("%s, warming: %v", name, err)
		}
	}
	for name, tc := range calls {
		setUp(name, tc.setup)
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

// noopCharged is an onCharged that sends nothing.
func noopCharged(context.Context, meter.Charged) error { return nil }

// roundTripChain records a chain roundTripChainDepth deep in tenant, with
// no ceilings, and returns its leaf.
func roundTripChain(t *testing.T, records *Store, tenant uuid.UUID) uuid.UUID {
	t.Helper()
	chain := recordChain(t, records, tenant)
	return chain[len(chain)-1]
}

// recordChain records a chain roundTripChainDepth deep in tenant, with no
// ceilings, and returns its ids, root first.
func recordChain(t *testing.T, records *Store, tenant uuid.UUID) []uuid.UUID {
	t.Helper()
	ctx := WithTenant(t.Context(), tenant)
	expiry := time.Now().Add(time.Hour)
	var chain []uuid.UUID
	parent := uuid.Nil()
	for i := range roundTripChainDepth {
		c := limes.Capability{
			ID: uuid.New(), ParentID: parent, ExpiresAt: expiry,
			Subject: limes.Principal{Type: limes.PrincipalTypeAgent, TenantID: tenant, Subject: "agent:" + string(rune('a'+i))},
			Caveats: limes.Caveats{
				Ops: []limes.Op{limes.OpGet}, Requests: limes.RequestCeiling{Unlimited: true}, Budget: limes.BudgetCeiling{Unlimited: true, UnitCode: testUnit},
				Copies: limes.CopyQuota{Unlimited: true},
			},
		}
		if err := records.Insert(ctx, limes.InsertRequest{Capability: c}); err != nil {
			t.Fatal(err)
		}
		parent, chain = c.ID, append(chain, c.ID)
	}
	return chain
}
