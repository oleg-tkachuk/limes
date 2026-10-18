package pgstore

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes"
)

// A hold ends with its capability at the latest, and ReleaseExpired takes
// the hold of a revoked capability without waiting for its expiry: no one
// can settle either any more, and both would keep the budget held.
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
	r, err := f.usage.Reserve(mine, limes.ReserveRequest{Subject: meterSubject(c.ID, f.mine), Amount: 1, TTL: farTTL})
	if err != nil {
		t.Fatal(err)
	}
	if want := c.ExpiresAt.Truncate(timeResolution); !r.ExpiresAt.Equal(want) {
		t.Errorf("a hold asked for %s expires at %s, want its capability's expiry %s", farTTL, r.ExpiresAt, want)
	}

	if got, err := f.usage.ReleaseExpired(every, limes.ReleaseExpiredRequest{}); err != nil || got.Released != 0 {
		t.Fatalf("ReleaseExpired before any revocation = %+v, %v; want nothing released", got, err)
	}
	if err := f.records.Revoke(mine, limes.RevokeRequest{CapabilityID: c.ID}); err != nil {
		t.Fatal(err)
	}
	if got, err := f.usage.ReleaseExpired(every, limes.ReleaseExpiredRequest{}); err != nil || got.Released != 1 {
		t.Fatalf("ReleaseExpired after the revocation = %+v, %v; want the hold released", got, err)
	}
	if usage, err := f.usage.GetUsage(mine, c.ID); err != nil || usage.ReservedAmount != 0 {
		t.Errorf("after the release the capability holds %+v, %v; want nothing", usage, err)
	}
}
