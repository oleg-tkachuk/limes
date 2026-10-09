package memstore

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes"
)

// fakeTx is a transaction handle the store threads to onCharged.
type fakeTx struct{ id int }

// The factory's handle is the one a charge's callback receives.
func TestWithTxFactoryThreadsTheHandle(t *testing.T) {
	const handleID = 42
	u := NewUsage[*fakeTx](nil).WithTxFactory(func() *fakeTx { return &fakeTx{id: handleID} })
	var got *fakeTx
	if _, err := u.Charge(context.Background(), limes.ChargeRequest{
		CapabilityID: uuid.New(), TenantID: uuid.New(), Amount: limes.MustParseAmount("1"),
	}, func(_ context.Context, tx *fakeTx) error { got = tx; return nil }); err != nil {
		t.Fatal(err)
	}
	if got == nil || got.id != handleID {
		t.Fatalf("onCharged received %+v, want the factory's handle", got)
	}
}

// The record store's clock decides what PurgeExpired counts as expired.
func TestStoreWithClockDrivesPurgeExpired(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	s := New[struct{}]().WithClock(func() time.Time { return now })
	tenant := uuid.New()
	c := limes.Capability{
		ID: uuid.New(), ExpiresAt: now.Add(time.Minute),
		Subject: limes.Principal{Type: limes.PrincipalAgent, TenantID: tenant, Subject: "a"},
	}
	if err := s.Insert(ctx, c, limes.Principal{Subject: "op"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Revoke(ctx, limes.RevokeRequest{ID: c.ID}); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.PurgeExpired(ctx, 0); n != 0 {
		t.Fatalf("purged %d before the capability expired", n)
	}
	now = now.Add(2 * time.Minute)
	if n, _ := s.PurgeExpired(ctx, 0); n != 1 {
		t.Fatalf("purged %d once the clock passed its expiry, want 1", n)
	}
}
