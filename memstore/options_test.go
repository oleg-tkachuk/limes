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
func TestTxFactoryThreadsTheHandle(t *testing.T) {
	const handleID = 42
	u, err := NewUsageStore(UsageStoreConfig[*fakeTx]{Records: newTestStore(), TxFactory: func() *fakeTx { return &fakeTx{id: handleID} }})
	if err != nil {
		t.Fatal(err)
	}
	var got *fakeTx
	ctx := t.Context()
	if _, err := u.Charge(ctx, limes.ChargeRequest{
		Subject: recordedSubject(ctx, t, u, subjectSpec{capID: uuid.New(), tenant: uuid.New()}),
		Amount:  limes.MustParseAmount("1"),
	}, func(_ context.Context, tx *fakeTx) error { got = tx; return nil }); err != nil {
		t.Fatal(err)
	}
	if got == nil || got.id != handleID {
		t.Fatalf("onCharged received %+v, want the factory's handle", got)
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
		Subject: limes.Principal{Type: limes.PrincipalAgent, TenantID: tenant, Subject: "a"},
	}
	if err := s.Insert(ctx, limes.InsertRequest{Capability: c, IssuedBy: limes.Principal{Subject: "op"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Revoke(ctx, limes.RevokeRequest{CapabilityID: c.ID}); err != nil {
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
