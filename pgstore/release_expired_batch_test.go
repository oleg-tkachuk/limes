package pgstore

import (
	"sync/atomic"
	"testing"
	"uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/internal/link"
	"github.com/oleg-tkachuk/limes/meter"
)

// releaseExpiredRoundTrips is a ReleaseExpired of one batch of holds, of any
// size: one round trip reading and locking the holds due, one locking and
// reading every counter they hold, one writing them all and deleting the
// holds with COMMIT, and the transaction that looks for leases due, two.
const releaseExpiredRoundTrips = 5

// releasedHolds is how many holds the larger batch releases.
const releasedHolds = 6

// heldAmounts are the amounts of the holds a release is compared on, each
// made on the chain's leaf, its middle and its root in turn, every other
// one under a copy.
var heldAmounts = []limes.Nanos{3, 5, 7, 11, 13, 17}

// heldCharge is spent on the leaf before the holds, so a release that
// touched spend would show.
const heldCharge limes.Nanos = 2

// A ReleaseExpired of many holds makes as many round trips as one of a
// single hold, however many it releases.
func TestReleaseExpiredRoundTrips(t *testing.T) {
	pool, sent := latencyPoolOf(t, 0, roundTripConns)
	fresh(t)
	clock := newFakeClock()
	records, err := NewStore(Config{DB: pool, Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	usage, err := NewUsageStore(Config{DB: pool, Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	r := expiredReleaser{records: records, usage: usage, clock: clock, sent: sent}
	r.release(t, releasedHolds) // prepares the statements on the connection
	if got := r.release(t, 1); got != releaseExpiredRoundTrips {
		t.Errorf("ReleaseExpired of one hold made %d round trips, want %d", got, releaseExpiredRoundTrips)
	}
	if got := r.release(t, releasedHolds); got != releaseExpiredRoundTrips {
		t.Errorf("ReleaseExpired of %d holds made %d round trips, want %d", releasedHolds, got, releaseExpiredRoundTrips)
	}
}

// expiredReleaser makes holds that expire and releases them, counting the
// round trips of the release.
type expiredReleaser struct {
	records *Store
	usage   *UsageStore
	clock   *fakeClock
	sent    *atomic.Int64
}

// release makes holds holds in a tenant of their own, lets them expire, and
// returns the round trips the ReleaseExpired that releases them makes.
func (r expiredReleaser) release(t *testing.T, holds int) int64 {
	t.Helper()
	tenant := uuid.New()
	ctx := WithTenant(t.Context(), tenant)
	chain := recordChain(t, r.records, tenant)
	for i := range holds {
		if _, err := r.usage.Reserve(ctx, meter.ReserveRequest{Subject: heldSubject(chain, tenant, i), Amount: 1}); err != nil {
			t.Fatal(err)
		}
	}
	r.clock.Advance(meter.DefaultReservationTTL)
	before := r.sent.Load()
	res, err := r.usage.ReleaseExpired(WithAllTenants(t.Context()), meter.ReleaseExpiredRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Released != int64(holds) {
		t.Fatalf("ReleaseExpired released %d holds, want %d", res.Released, holds)
	}
	return r.sent.Load() - before
}

// Released together, the holds leave every counter as releasing them one
// by one does: two tenants hold the same amounts on chains of one shape,
// and one releases them with Release, the other with ReleaseExpired.
func TestReleaseExpiredMatchesReleasingOneByOne(t *testing.T) {
	fresh(t)
	clock := newFakeClock()
	records, err := NewStore(Config{DB: app, Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	usage, err := NewUsageStore(Config{DB: app, Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	type side struct {
		tenant uuid.UUID
		chain  []uuid.UUID
		holds  []meter.Reservation
	}
	sides := [2]*side{{tenant: uuid.New()}, {tenant: uuid.New()}}
	for _, sd := range sides {
		ctx := WithTenant(t.Context(), sd.tenant)
		sd.chain = recordChain(t, records, sd.tenant)
		if _, err := usage.Charge(ctx, meter.ChargeRequest{Subject: heldSubject(sd.chain, sd.tenant, 0), Amount: heldCharge}, nil); err != nil {
			t.Fatal(err)
		}
		for i, amount := range heldAmounts {
			r, err := usage.Reserve(ctx, meter.ReserveRequest{Subject: heldSubject(sd.chain, sd.tenant, i), Amount: amount})
			if err != nil {
				t.Fatal(err)
			}
			sd.holds = append(sd.holds, r)
		}
	}
	one := sides[0]
	for i, h := range one.holds {
		if err := usage.Release(WithTenant(t.Context(), one.tenant), meter.ReleaseRequest{Subject: heldSubject(one.chain, one.tenant, i), ReservationID: h.ID}); err != nil {
			t.Fatal(err)
		}
	}
	clock.Advance(meter.DefaultReservationTTL)
	if res, err := usage.ReleaseExpired(WithAllTenants(t.Context()), meter.ReleaseExpiredRequest{}); err != nil || res.Released != int64(len(heldAmounts)) {
		t.Fatalf("ReleaseExpired = %+v, %v; want %d released", res, err, len(heldAmounts))
	}
	read := func(sd *side) (out []meter.Usage, budget meter.TenantBudget) {
		ctx := WithTenant(t.Context(), sd.tenant)
		for _, id := range sd.chain {
			u, err := usage.GetUsage(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, u)
		}
		budget, err := usage.GetTenantBudget(ctx, sd.tenant)
		if err != nil {
			t.Fatal(err)
		}
		return out, budget
	}
	oneByOne, oneBudget := read(sides[0])
	together, togetherBudget := read(sides[1])
	for depth := range oneByOne {
		a, b := oneByOne[depth], together[depth]
		if a.SpentAmount != b.SpentAmount || a.ReservedAmount != b.ReservedAmount || a.RequestCount != b.RequestCount || a.UnitCode != b.UnitCode {
			t.Errorf("counters at depth %d: one by one %+v, together %+v", depth, a, b)
		}
	}
	if oneBudget.SpentAmount != togetherBudget.SpentAmount || oneBudget.ReservedAmount != togetherBudget.ReservedAmount {
		t.Errorf("tenant aggregate: one by one %+v, together %+v", oneBudget, togetherBudget)
	}
	if oneBudget.ReservedAmount != 0 || togetherBudget.SpentAmount != heldCharge {
		t.Errorf("tenant aggregate after the releases = %+v; want nothing held, %v spent", togetherBudget, heldCharge)
	}
}

// heldSubject is the subject of the i-th hold on chain, root first: on the
// leaf, the middle and the root in turn, every other one under a copy of
// its capability.
func heldSubject(chain []uuid.UUID, tenant uuid.UUID, i int) limes.MeterSubject {
	capID := chain[len(chain)-1-i%len(chain)]
	fields := link.MeterSubjectFields{
		CapabilityID: capID, TenantID: tenant,
		Requests: limes.RequestCeiling{Unlimited: true}, Budget: limes.BudgetCeiling{Unlimited: true, UnitCode: testUnit},
		CopyQuota: limes.CopyQuota{Unlimited: true},
	}
	if i%2 == 1 {
		fields.Copies = []limes.CopyCeiling{{RevocationID: []byte(capID.String()), Requests: &limes.RequestCeiling{Unlimited: true}}}
	}
	return link.NewMeterSubject(fields).(limes.MeterSubject)
}
