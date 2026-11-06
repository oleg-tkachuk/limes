package limes_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/oleg-tkachuk/limes"
)

// A parent revoked through a Revoker reaches its child's cached "not
// revoked" answer at once, over memstore's lineage, and drops no other
// answer: the cache counts one narrow clear and no Clear.
func TestRevokerReachesADescendant(t *testing.T) {
	ctx := context.Background()
	s := newExampleStack()
	_, token := s.issue()
	parent := s.verified(token)
	child, err := s.issuer.Delegate(ctx, limes.DelegateRequest{
		Parent:  parent,
		Subject: limes.Principal{Type: limes.PrincipalTypeAgent, TenantID: s.tenantID, Subject: "sub-worker"},
		TTL:     time.Minute,
		Caveats: limes.Caveats{
			Ops: []limes.Op{limes.OpGet}, ResourcePrefixes: []string{"corpus/public/"},
			Requests: limes.RequestCeiling{Max: 1}, Budget: limes.BudgetCeiling{Max: limes.MustParseAmount("1.00"), UnitCode: "USD"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	cache := newRevocationCache(t, limes.RevocationCacheConfig{Capabilities: s.records, TTL: cacheLongerThanTheTest})
	revoker, err := limes.NewRevoker(limes.RevokerConfig{Store: s.records, Cache: cache, Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	if revoked, err := cache.IsRevoked(ctx, child.Capability.ID); err != nil || revoked {
		t.Fatalf("child before the revocation = %v, %v; want live", revoked, err)
	}
	if _, err := revoker.Revoke(ctx, limes.RevokeRequest{CapabilityID: parent.Capability().ID, Actor: "operator"}); err != nil {
		t.Fatal(err)
	}
	if revoked, err := cache.IsRevoked(ctx, child.Capability.ID); err != nil || !revoked {
		t.Errorf("child right after its parent's revocation = %v, %v; want revoked", revoked, err)
	}
	if st := cache.Stats(); st.NarrowClears != 1 || st.Clears != 0 {
		t.Errorf("Stats counted %d ClearRevoked and %d Clear; want 1 and 0", st.NarrowClears, st.Clears)
	}
}
