package pgstore

import (
	"errors"
	"log/slog"
	"slices"
	"testing"
	"time"
	"uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/meter"
)

// Whether a capability has expired is its verifier's to decide, by its own
// clock and leeway, against an expiry the issuer's clock wrote. The store,
// whose clock with Config.Now nil is the database's, refuses Reserve and
// Lease with ErrExpired only past what any verifier could still accept, and
// ends a hold or a lease there at the latest — never at an instant already
// past, which no Settle could reach.
func TestCapabilityExpiryIsTheVerifiersDecision(t *testing.T) {
	// longTTL outlives the capability by more than the grace, so every hold
	// and lease ends by the capability's.
	const longTTL = 2 * capabilityExpiryTestTTL
	// margin keeps the database's clock clear of the instant a case tests,
	// by more than the test takes to run.
	const margin = time.Minute
	cases := []struct {
		name string
		// past is how far the database's clock stands past the capability's
		// expiry; negative is short of it.
		past    time.Duration
		expired bool
		// logged is the level each of Reserve and Lease logs its decision
		// at; none when the capability has not expired.
		logged []slog.Level
	}{
		{name: "the database's clock behind the issuer's", past: -margin},
		{name: "past the expiry, within a verifier's leeway", past: capabilityExpiryGrace - margin, logged: []slog.Level{slog.LevelInfo, slog.LevelInfo}},
		{name: "past every verifier's leeway", past: capabilityExpiryGrace + margin, expired: true, logged: []slog.Level{slog.LevelWarn, slog.LevelWarn}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fresh(t)
			records, err := NewStore(Config{DB: app})
			if err != nil {
				t.Fatal(err)
			}
			logged := &logRecords{}
			usage, err := NewUsageStore(Config{DB: clocked, Logger: slog.New(logged)})
			if err != nil {
				t.Fatal(err)
			}
			tenant := uuid.New()
			ctx := WithTenant(t.Context(), tenant)
			expires := time.Now().Add(capabilityExpiryTestTTL).UTC().Truncate(timeResolution)
			held := meterSubject(recordExpiring(t, records, tenant, expires), tenant)
			leased := meterSubject(recordExpiring(t, records, tenant, expires), tenant)
			newDBClock(t).Shift(time.Until(expires.Add(tc.past)))
			last := expires.Add(capabilityExpiryGrace)

			r, err := usage.Reserve(ctx, meter.ReserveRequest{Subject: held, Amount: 1, TTL: longTTL})
			l, lerr := usage.Lease(ctx, meter.LeaseRequest{Subject: leased, Requests: roundTripLeasedRequests, TTL: longTTL})
			if got := expiryLevels(logged); !slices.Equal(got, tc.logged) {
				t.Errorf("Reserve and Lease logged their capability's expiry at %v, want %v", got, tc.logged)
			}
			if tc.expired {
				if !errors.Is(err, limes.ErrExpired) || !errors.Is(lerr, limes.ErrExpired) {
					t.Fatalf("Reserve and Lease past every leeway: errs = %v, %v; want ErrExpired", err, lerr)
				}
				return
			}
			if err != nil || lerr != nil {
				t.Fatalf("Reserve and Lease: %v, %v", err, lerr)
			}
			if !r.ExpiresAt.Equal(last) || !l.ExpiresAt.Equal(last) {
				t.Errorf("the hold ends at %s and the lease at %s; want both at %s, the capability's expiry and the grace", r.ExpiresAt, l.ExpiresAt, last)
			}
			if _, err := usage.Settle(ctx, meter.SettleRequest{Subject: held, ReservationID: r.ID, Amount: 1}, nil); err != nil {
				t.Errorf("Settle of the hold: %v", err)
			}
			if _, err := usage.Bump(ctx, meter.BumpRequest{Subject: leased}); err != nil {
				t.Fatal(err)
			}
			if got, err := usage.GetLease(ctx, leased.CapabilityID()); err != nil || got.RequestsSpent != 1 {
				t.Errorf("the lease after a bump = %+v, %v; want the bump made under it", got, err)
			}
		})
	}
}

// expiryLevels are the levels of the lines in logged that name how long
// past its expiry a capability stood.
func expiryLevels(logged *logRecords) []slog.Level {
	logged.mu.Lock()
	defer logged.mu.Unlock()
	var levels []slog.Level
	for _, r := range logged.records {
		r.Attrs(func(a slog.Attr) bool {
			if a.Key == logKeyExpiredFor {
				levels = append(levels, r.Level)
				return false
			}
			return true
		})
	}
	return levels
}

// capabilityExpiryTestTTL is how long the capabilities of
// TestCapabilityExpiryIsTheVerifiersDecision live by the issuer's clock.
const capabilityExpiryTestTTL = time.Hour

// recordExpiring records a root capability of tenant, with no ceilings, that
// expires at expires, and returns its id.
func recordExpiring(t *testing.T, records *Store, tenant uuid.UUID, expires time.Time) uuid.UUID {
	t.Helper()
	c := limes.Capability{
		ID: uuid.New(), ExpiresAt: expires,
		Subject: limes.Principal{Type: limes.PrincipalTypeAgent, TenantID: tenant, Subject: "agent"},
		Caveats: limes.Caveats{
			Ops: []limes.Op{limes.OpGet}, Requests: limes.RequestCeiling{Unlimited: true}, Budget: limes.BudgetCeiling{Unlimited: true, UnitCode: testUnit},
			Copies: limes.CopyQuota{Unlimited: true},
		},
	}
	if err := records.Insert(WithTenant(t.Context(), tenant), limes.InsertRequest{Capability: c}); err != nil {
		t.Fatal(err)
	}
	return c.ID
}
