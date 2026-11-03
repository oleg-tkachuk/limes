package memstore

import (
	"context"
	"errors"
	"testing"
	"time"
	"uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/meter"
)

// An erased tenant stays erased while any of its records is left, so a
// capability on record cannot be metered and none is recorded beside it;
// PurgeOrphans forgets the mark once the last record is gone, as nothing of
// the tenant can be metered then.
func TestPurgeOrphansForgetsAnErasedTenantWithNoRecordLeft(t *testing.T) {
	ctx, u, capID, tenant := newCopyFixture(t)
	if err := u.EraseTenantUsage(ctx, meter.EraseTenantUsageRequest{TenantID: tenant}); err != nil {
		t.Fatal(err)
	}
	setBudget := func() error {
		_, err := u.SetTenantBudget(ctx, meter.SetTenantBudgetRequest{TenantID: tenant, Ceiling: limes.BudgetCeiling{Unlimited: true, UnitCode: limes.AbstractUnitCode}})
		return err
	}
	insert := func() error {
		return u.records.Insert(ctx, limes.InsertRequest{Capability: mkCap(uuid.New(), uuid.Nil(), tenant, "agent"), IssuedBy: limes.Principal{Subject: "op"}})
	}
	if _, err := u.PurgeOrphans(ctx, meter.PurgeOrphansRequest{}); err != nil {
		t.Fatal(err)
	}
	if err := setBudget(); !errors.Is(err, limes.ErrTenantErased) {
		t.Fatalf("SetTenantBudget with a record of the tenant left: err = %v, want ErrTenantErased", err)
	}
	if err := insert(); !errors.Is(err, limes.ErrTenantErased) {
		t.Fatalf("Insert with a record of the tenant left: err = %v, want ErrTenantErased", err)
	}
	dropRecord(u.records, capID)
	if _, err := u.PurgeOrphans(ctx, meter.PurgeOrphansRequest{}); err != nil {
		t.Fatal(err)
	}
	if err := setBudget(); err != nil {
		t.Fatalf("SetTenantBudget once no record of the tenant is left: %v", err)
	}
	if err := insert(); err != nil {
		t.Fatalf("Insert once no record of the tenant is left: %v", err)
	}
}

// exchangeExpiredAgo is how long before now the subject tokens of the
// erase check expired: past MinPurgeRetention, the retention an erase
// applies.
const exchangeExpiredAgo = 2 * limes.MinPurgeRetention

// An erase forgets its tenant's exchanges of tokens expired past the
// retention, whether or not their capabilities stay on record, and leaves
// another tenant's to Purge. memstore's own: pgstore keeps every exchange
// until Purge, which the ExchangeStore contract allows as well.
func TestEraseForgetsOnlyItsTenantsSpentExchanges(t *testing.T) {
	ctx := context.Background()
	s := newTestStore()
	mine, theirs := uuid.New(), uuid.New()
	key := func(tenant uuid.UUID, token string) limes.ExchangeKey {
		return limes.ExchangeKey{TenantID: tenant, Issuer: "https://idp.example", TokenID: token}
	}
	claims := map[limes.ExchangeKey]uuid.UUID{}
	for _, tenant := range []uuid.UUID{mine, theirs} {
		c := mkCap(uuid.New(), uuid.Nil(), tenant, "agent")
		if err := s.Insert(ctx, limes.InsertRequest{Capability: c, IssuedBy: limes.Principal{Subject: "op"}}); err != nil {
			t.Fatal(err)
		}
		k := key(tenant, "jti-"+tenant.String())
		if _, claimed, err := s.ClaimExchange(ctx, limes.ClaimExchangeRequest{Key: k, TokenExpiresAt: time.Now().Add(-exchangeExpiredAgo), CapabilityID: c.ID}); err != nil || !claimed {
			t.Fatalf("claim for tenant %s: %v, %v", tenant, claimed, err)
		}
		claims[k] = c.ID
	}
	if _, err := s.EraseTenantRecords(ctx, limes.EraseTenantRequest{TenantID: mine}); err != nil {
		t.Fatal(err)
	}
	for k, id := range claims {
		got, err := s.ExchangedFor(ctx, k)
		switch k.TenantID {
		case mine:
			if !errors.Is(err, limes.ErrNotFound) {
				t.Errorf("the erased tenant's exchange of an expired token = %s, %v; want ErrNotFound", got, err)
			}
		default:
			if err != nil || got != id {
				t.Errorf("another tenant's exchange of an expired token after the erase = %s, %v; want %s kept", got, err, id)
			}
		}
	}
}
