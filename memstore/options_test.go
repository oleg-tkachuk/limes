package memstore

import (
	"context"
	"testing"
	"time"
	"uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/meter"
)

// callerKey keys a value a test puts in the caller's context.
type callerKey struct{}

// With no transaction of its own, the store hands onCharged the caller's
// context: what the caller put in it is there.
func TestOnChargedRunsOnTheCallersContext(t *testing.T) {
	const marker = "the caller's"
	u, err := NewUsageStore(UsageStoreConfig{Records: newTestStore()})
	if err != nil {
		t.Fatal(err)
	}
	var got any
	ctx := context.WithValue(t.Context(), callerKey{}, marker)
	if _, err := u.Charge(ctx, meter.ChargeRequest{
		Subject: recordedSubject(ctx, t, u, subjectSpec{capID: uuid.New(), tenant: uuid.New()}),
		Amount:  limes.MustParseAmount("1"),
	}, func(ctx context.Context, _ meter.Charged) error { got = ctx.Value(callerKey{}); return nil }); err != nil {
		t.Fatal(err)
	}
	if got != marker {
		t.Fatalf("onCharged's context holds %v, want the caller's value", got)
	}
}

// The record store's clock decides what Purge counts as expired.
func TestStoreClockDrivesPurge(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	s, err := NewStore(StoreConfig{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	tenant := uuid.New()
	c := limes.Capability{
		ID: uuid.New(), ExpiresAt: now.Add(time.Minute),
		Subject: limes.Principal{Type: limes.PrincipalTypeAgent, TenantID: tenant, Subject: "a"},
	}
	if err := s.Insert(ctx, limes.InsertRequest{Capability: c, IssuedBy: limes.Principal{Subject: "op"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Revoke(ctx, limes.RevokeRequest{CapabilityID: c.ID}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Purge(ctx, limes.PurgeRequest{}); got.Purged != 0 {
		t.Fatalf("purged %d before the capability expired", got.Purged)
	}
	// Expired, but within MinPurgeRetention: a verifier may still accept
	// the token, so its revocation stays.
	now = c.ExpiresAt.Add(limes.MinPurgeRetention)
	if got, _ := s.Purge(ctx, limes.PurgeRequest{}); got.Purged != 0 {
		t.Fatalf("purged %d within the minimum retention", got.Purged)
	}
	now = now.Add(time.Second)
	if got, _ := s.Purge(ctx, limes.PurgeRequest{}); got.Purged != 1 {
		t.Fatalf("purged %d once the retention passed, want 1", got.Purged)
	}
}

// A negative quota of copies is refused rather than read as none.
func TestNewUsageStoreRefusesANegativeCopyQuota(t *testing.T) {
	if _, err := NewUsageStore(UsageStoreConfig{Records: newTestStore(), MaxCopiesPerCapability: -1}); err == nil {
		t.Fatal("a negative MaxCopiesPerCapability was accepted")
	}
}
