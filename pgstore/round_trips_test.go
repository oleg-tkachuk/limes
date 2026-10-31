package pgstore

import (
	"testing"
	"time"
	"uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/meter"
)

// The round trips each meter call makes, on a chain of any depth
// (docs/adr/0018-fewer-round-trips-per-charge.md): BEGIN, then one batch of
// the scope, the erase lock, the erased check, a replay's lookup and the
// lineage held, then the counters, then COMMIT. A change that sends one
// more is a change to the tenant's throughput across a network, and fails
// here.
const (
	chargeRoundTrips  = 11
	bumpRoundTrips    = 6
	reserveRoundTrips = 13
)

// roundTripChainDepth is how deep the chain the calls count against is.
const roundTripChainDepth = 3

// Charge, Bump and Reserve make the round trips their constants name, on
// the leaf of a chain.
func TestMeterRoundTrips(t *testing.T) {
	fresh(t)
	pool, sent := latencyPool(t, 0)
	records, err := NewStore(Config{DB: pool})
	if err != nil {
		t.Fatal(err)
	}
	usage, err := NewUsageStore(Config{DB: pool})
	if err != nil {
		t.Fatal(err)
	}
	tenant := uuid.New()
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
	subject := meterSubject(leaf, tenant)
	// The first call of a capability creates its rows; what is counted is a
	// call on rows that exist.
	if _, err := usage.Charge(ctx, meter.ChargeRequest{Subject: subject, Amount: 1, ExternalRef: "warm"}, nil); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		want int64
		call func() error
	}{
		"Charge": {chargeRoundTrips, func() error {
			_, err := usage.Charge(ctx, meter.ChargeRequest{Subject: subject, Amount: 1, ExternalRef: "counted"}, nil)
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
	} {
		before := sent.Load()
		if err := tc.call(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := sent.Load() - before; got != tc.want {
			t.Errorf("%s made %d round trips, want %d", name, got, tc.want)
		}
	}
}
