package pgstore

import (
	"errors"
	"testing"
	"time"
	"uuid"

	"github.com/oleg-tkachuk/limes/meter"
)

// clockAhead is how far TestExpiryFollowsTheDatabasesClock moves the
// database's clock ahead of the store's: past a hold's and a lease's default
// TTL, and short of the hour the recorded chains live (recordChain).
const clockAhead = 30 * time.Minute

// With Config.Now nil, a hold's and a lease's expiry follow the database's
// clock, not the store's: where the database's runs ahead, a hold made
// there expires that much later than the store's clock says, and a hold or
// a lease made by the database's clock as it is has expired.
func TestExpiryFollowsTheDatabasesClock(t *testing.T) {
	fresh(t)
	records, err := NewStore(Config{DB: app})
	if err != nil {
		t.Fatal(err)
	}
	here, err := NewUsageStore(Config{DB: app})
	if err != nil {
		t.Fatal(err)
	}
	ahead, err := NewUsageStore(Config{DB: clocked})
	if err != nil {
		t.Fatal(err)
	}
	newDBClock(t).Shift(clockAhead)
	tenant := uuid.New()
	ctx := WithTenant(t.Context(), tenant)
	subject := meterSubject(roundTripChain(t, records, tenant), tenant)
	reserve := func(on *UsageStore) meter.Reservation {
		t.Helper()
		r, err := on.Reserve(ctx, meter.ReserveRequest{Subject: subject, Amount: 1})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}

	if r := reserve(ahead); !r.ExpiresAt.After(time.Now().Add(clockAhead)) {
		t.Errorf("a hold made by a database clock ahead expires at %s, want past %s", r.ExpiresAt, time.Now().Add(clockAhead))
	}
	if _, err := ahead.Settle(ctx, meter.SettleRequest{Subject: subject, ReservationID: reserve(here).ID, Amount: 1}, nil); !errors.Is(err, meter.ErrReservationNotFound) {
		t.Errorf("Settle by a database clock past the hold's expiry: err = %v, want ErrReservationNotFound", err)
	}
	// Of the two holds, the one made by the clock as it is has expired by
	// the clock ahead; the one made by the clock ahead has not.
	if res, err := ahead.ReleaseExpired(WithAllTenants(t.Context()), meter.ReleaseExpiredRequest{}); err != nil || res.Released != 1 {
		t.Errorf("ReleaseExpired by a database clock past one hold's expiry = %+v, %v; want that one released", res, err)
	}

	leased := meterSubject(roundTripChain(t, records, tenant), tenant)
	if _, err := here.Lease(ctx, meter.LeaseRequest{Subject: leased, Requests: roundTripLeasedRequests}); err != nil {
		t.Fatal(err)
	}
	if _, err := ahead.Bump(ctx, meter.BumpRequest{Subject: leased}, nil); err != nil {
		t.Fatal(err)
	}
	if l, err := here.GetLease(ctx, leased.CapabilityID()); err != nil || l.RequestsSpent != 0 {
		t.Errorf("the lease after a bump by a database clock past its end = %+v, %v; want nothing made under it", l, err)
	}
}
