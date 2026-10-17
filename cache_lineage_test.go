package limes_test

import (
	"context"
	"testing"
	"time"

	"github.com/oleg-tkachuk/limes"
)

// cacheLongerThanTheTest outlives the test, so only a drop can end an entry.
const cacheLongerThanTheTest = time.Hour

// A cached answer about a child is also an answer about its parent: after
// the parent is revoked, dropping the cache must reach the child at once,
// not when its entry expires.
func TestRevocationCacheDropReachesADescendant(t *testing.T) {
	ctx := context.Background()
	s := newExampleStack()
	_, token := s.issue()
	parent := s.verified(token)
	childResult, err := s.issuer.Delegate(ctx, limes.DelegateRequest{
		Parent:  parent,
		Subject: limes.Principal{Type: limes.PrincipalTypeAgent, TenantID: s.tenantID, Subject: "sub-worker"},
		TTL:     time.Minute,
		Caveats: limes.Caveats{
			Ops: []limes.Op{limes.OpGet}, ResourcePrefixes: []string{"corpus/public/"},
			Requests: limes.RequestCeiling{Max: 1}, Budget: limes.BudgetCeiling{Max: limes.MustParseAmount("1.00"), UnitCode: "USD"},
		},
	})
	child := childResult.Capability
	if err != nil {
		t.Fatal(err)
	}

	cache := newRevocationCache(t, limes.RevocationCacheConfig{Upstream: s.records, TTL: cacheLongerThanTheTest})
	if revoked, err := cache.IsRevoked(ctx, child.ID); err != nil || revoked {
		t.Fatalf("child before the revocation = %v, %v; want live", revoked, err)
	}
	// Not cascading: the child has no entry of its own, only its parent's.
	if err := s.records.Revoke(ctx, limes.RevokeRequest{CapabilityID: parent.Capability().ID}); err != nil {
		t.Fatal(err)
	}
	cache.Clear()
	if revoked, err := cache.IsRevoked(ctx, child.ID); err != nil || !revoked {
		t.Errorf("child after its parent's revocation and Clear = %v, %v; want revoked", revoked, err)
	}
}

// newRevocationCache is limes.NewCachedRevocationChecker, failing the test on
// a refused configuration.
func newRevocationCache(t testing.TB, cfg limes.RevocationCacheConfig) *limes.CachedRevocationChecker {
	t.Helper()
	c, err := limes.NewCachedRevocationChecker(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
