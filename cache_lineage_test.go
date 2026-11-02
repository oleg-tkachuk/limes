package limes_test

import (
	"context"
	"testing"
	"time"
	"uuid"

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

	cache := newRevocationCache(t, limes.RevocationCacheConfig{Capabilities: s.records, TTL: cacheLongerThanTheTest})
	if revoked, err := cache.IsRevoked(ctx, child.ID); err != nil || revoked {
		t.Fatalf("child before the revocation = %v, %v; want live", revoked, err)
	}
	// Not cascading: the child has no entry of its own, only its parent's.
	if _, err := s.records.Revoke(ctx, limes.RevokeRequest{CapabilityID: parent.Capability().ID}); err != nil {
		t.Fatal(err)
	}
	cache.Clear()
	if revoked, err := cache.IsRevoked(ctx, child.ID); err != nil || !revoked {
		t.Errorf("child after its parent's revocation and Clear = %v, %v; want revoked", revoked, err)
	}
}

// newRevocationCache is limes.NewRevocationCache, failing the test on
// a refused configuration.
func newRevocationCache(t testing.TB, cfg limes.RevocationCacheConfig) *limes.RevocationCache {
	t.Helper()
	c, err := limes.NewRevocationCache(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// Below names an ancestor on the answer's list, and nothing else: not the
// capability itself, not an id the answer never read, and nothing under a
// revoked answer, which names no ancestors.
func TestRevocationAnswerBelow(t *testing.T) {
	parent, grandparent, other := uuid.New(), uuid.New(), uuid.New()
	live := limes.RevocationAnswer{Ancestors: []uuid.UUID{parent, grandparent}}
	cases := []struct {
		name     string
		answer   limes.RevocationAnswer
		ancestor uuid.UUID
		want     bool
	}{
		{name: "the nearest ancestor", answer: live, ancestor: parent, want: true},
		{name: "a farther ancestor", answer: live, ancestor: grandparent, want: true},
		{name: "an id the answer never read", answer: live, ancestor: other},
		{name: "a root", answer: limes.RevocationAnswer{Ancestors: []uuid.UUID{}}, ancestor: parent},
		{name: "a revoked answer", answer: limes.RevocationAnswer{Revoked: true}, ancestor: parent},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.answer.Below(tc.ancestor); got != tc.want {
				t.Fatalf("Below(%v) = %v, want %v", tc.ancestor, got, tc.want)
			}
		})
	}
}
