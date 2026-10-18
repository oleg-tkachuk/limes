package pgstore

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/meter"
)

// A hold ends with its capability at the latest. A revoked capability's
// hold still counts until then, and can still be settled: a cost already
// incurred belongs in the ledger. ReleaseExpired takes it only once it
// expires.
func TestAHoldEndsWithItsCapability(t *testing.T) {
	// farTTL outlives the capability the fixture records.
	const farTTL = 24 * time.Hour
	f := newTenantFixture(t)
	mine := WithTenant(context.Background(), f.mine)
	every := WithAllTenants(context.Background())
	c := f.parent
	c.ID, c.Subject.Subject = uuid.New(), "agent:holder"
	if err := f.records.Insert(mine, limes.InsertRequest{Capability: c}); err != nil {
		t.Fatal(err)
	}
	subject := meterSubject(c.ID, f.mine)
	reserve := func() meter.Reservation {
		t.Helper()
		r, err := f.usage.Reserve(mine, meter.ReserveRequest{Subject: subject, Amount: 1, TTL: farTTL})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	settled, left := reserve(), reserve()
	if want := c.ExpiresAt.Truncate(timeResolution); !settled.ExpiresAt.Equal(want) {
		t.Errorf("a hold asked for %s expires at %s, want its capability's expiry %s", farTTL, settled.ExpiresAt, want)
	}
	if err := f.records.Revoke(mine, limes.RevokeRequest{CapabilityID: c.ID}); err != nil {
		t.Fatal(err)
	}
	if got, err := f.usage.ReleaseExpired(every, meter.ReleaseExpiredRequest{}); err != nil || got.Released != 0 {
		t.Fatalf("ReleaseExpired after the revocation = %+v, %v; want nothing released before the holds expire", got, err)
	}
	if _, err := f.usage.Settle(mine, meter.SettleRequest{Subject: subject, ReservationID: settled.ID, Amount: 1}, nil); err != nil {
		t.Fatalf("settling a revoked capability's hold: %v", err)
	}
	f.clock.Advance(left.ExpiresAt.Sub(f.clock.Now()))
	if got, err := f.usage.ReleaseExpired(every, meter.ReleaseExpiredRequest{}); err != nil || got.Released != 1 {
		t.Fatalf("ReleaseExpired once the capability expired = %+v, %v; want the other hold released", got, err)
	}
	if usage, err := f.usage.GetUsage(mine, c.ID); err != nil || usage.ReservedAmount != 0 || usage.SpentAmount != 1 {
		t.Errorf("after both the capability = %+v, %v; want nothing held and 1 spent", usage, err)
	}
}
