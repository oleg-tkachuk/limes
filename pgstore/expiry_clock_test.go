package pgstore

import (
	"errors"
	"testing"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/oleg-tkachuk/limes/meter"
)

// clockAheadSchema holds a now() clockAhead ahead of PostgreSQL's, which a
// session finds first on its search path: a database clock ahead of the
// store's, as a test cannot move the database's own. clockAhead is past
// a hold's and a lease's default TTL, and short of the hour the recorded
// chains live (recordChain).
const (
	clockAheadSchema = "limes_test_clock_ahead"
	clockAhead       = 30 * time.Minute
	// clockAheadInterval is clockAhead as SQL spells it.
	clockAheadInterval = "30 minutes"
	// clockAheadPath is the search path of a session whose now() is
	// clockAheadSchema's.
	clockAheadPath = clockAheadSchema + `, pg_catalog, "$user", public`
)

const (
	sqlCreateClockAhead = `CREATE SCHEMA IF NOT EXISTS ` + clockAheadSchema
	sqlClockAheadNow    = `CREATE OR REPLACE FUNCTION ` + clockAheadSchema + `.now() RETURNS timestamptz
LANGUAGE sql STABLE AS $$ SELECT pg_catalog.now() + interval '` + clockAheadInterval + `' $$`
	sqlGrantClockAhead = `GRANT USAGE ON SCHEMA ` + clockAheadSchema + ` TO ` + appUser
	sqlDropClockAhead  = `DROP SCHEMA IF EXISTS ` + clockAheadSchema + ` CASCADE`
)

// searchPathParam is the run-time parameter a session's search path is.
const searchPathParam = "search_path"

// clockAheadPool is a pool as appUser whose sessions' now() runs clockAhead
// ahead of the database's.
func clockAheadPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	for _, q := range []string{sqlCreateClockAhead, sqlClockAheadNow, sqlGrantClockAhead} {
		if _, err := owner.Exec(t.Context(), q); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _, _ = owner.Exec(t.Context(), sqlDropClockAhead) })
	cfg, err := pgxpool.ParseConfig(ownerDSN)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.User, cfg.ConnConfig.Password = appUser, appPassword
	cfg.ConnConfig.RuntimeParams[searchPathParam] = clockAheadPath
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

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
	ahead, err := NewUsageStore(Config{DB: clockAheadPool(t)})
	if err != nil {
		t.Fatal(err)
	}
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
	if _, err := ahead.Bump(ctx, meter.BumpRequest{Subject: leased}); err != nil {
		t.Fatal(err)
	}
	if l, err := here.GetLease(ctx, leased.CapabilityID()); err != nil || l.RequestsSpent != 0 {
		t.Errorf("the lease after a bump by a database clock past its end = %+v, %v; want nothing made under it", l, err)
	}
}
