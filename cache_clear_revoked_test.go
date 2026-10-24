package limes_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes"
)

// unrelatedRevocations is how many capabilities the hit-rate check revokes
// while it verifies another, one between two checks.
const unrelatedRevocations = 50

// clearHits verifies one capability, then revokes an unrelated one and
// tells the cache with clear, unrelatedRevocations times over, and returns
// the cache's hits for the capability verified.
func clearHits(t *testing.T, clear func(*limes.RevocationCache, uuid.UUID)) uint64 {
	t.Helper()
	ctx := context.Background()
	s := newExampleStack()
	verified, _ := s.issue()
	cache := newRevocationCache(t, limes.RevocationCacheConfig{Capabilities: s.records, TTL: cacheLongerThanTheTest})
	for range unrelatedRevocations {
		if revoked, err := cache.IsRevoked(ctx, verified.ID); err != nil || revoked {
			t.Fatalf("IsRevoked = %v, %v", revoked, err)
		}
		other, _ := s.issue()
		if _, err := s.records.Revoke(ctx, limes.RevokeRequest{CapabilityID: other.ID}); err != nil {
			t.Fatal(err)
		}
		clear(cache, other.ID)
	}
	return cache.Stats().Capabilities.Hits
}

// A consumer that revokes often keeps the answers its revocations do not
// touch: with ClearRevoked every check but the first of a capability
// verified between unrelated revocations is a hit; with Clear none is.
func TestClearRevokedKeepsTheHitRate(t *testing.T) {
	cleared := clearHits(t, func(c *limes.RevocationCache, _ uuid.UUID) { c.Clear() })
	narrow := clearHits(t, func(c *limes.RevocationCache, id uuid.UUID) { c.ClearRevoked(id) })
	t.Logf("hits of %d checks: %d after Clear, %d after ClearRevoked", unrelatedRevocations, cleared, narrow)
	if cleared != 0 || narrow != unrelatedRevocations-1 {
		t.Errorf("hits: %d after Clear, %d after ClearRevoked; want 0 and %d", cleared, narrow, unrelatedRevocations-1)
	}
}

// ClearRevoked of a parent reaches a child's cached answer at once: the
// answer named the parent among the child's ancestors.
func TestClearRevokedReachesADescendant(t *testing.T) {
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
	if revoked, err := cache.IsRevoked(ctx, child.Capability.ID); err != nil || revoked {
		t.Fatalf("child before the revocation = %v, %v; want live", revoked, err)
	}
	if _, err := s.records.Revoke(ctx, limes.RevokeRequest{CapabilityID: parent.Capability().ID}); err != nil {
		t.Fatal(err)
	}
	cache.ClearRevoked(parent.Capability().ID)
	if revoked, err := cache.IsRevoked(ctx, child.Capability.ID); err != nil || !revoked {
		t.Errorf("child after ClearRevoked of its parent = %v, %v; want revoked", revoked, err)
	}
	if st := cache.Stats(); st.ClearsRevoked != 1 || st.Clears != 0 {
		t.Errorf("Stats counted %d ClearRevoked and %d Clear; want 1 and 0", st.ClearsRevoked, st.Clears)
	}
}

// isRevokedOnly is a RevocationLookup that names no ancestors.
type isRevokedOnly struct{ limes.RevocationLookup }

// Over an upstream that names no ancestors the cache cannot tell a
// descendant from any other id, so ClearRevoked clears it whole.
func TestClearRevokedWithoutLineageClears(t *testing.T) {
	ctx := context.Background()
	s := newExampleStack()
	c, _ := s.issue()
	cache := newRevocationCache(t, limes.RevocationCacheConfig{Capabilities: isRevokedOnly{s.records}, TTL: cacheLongerThanTheTest})
	if _, err := cache.IsRevoked(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	cache.ClearRevoked(uuid.New())
	if st := cache.Stats(); st.Clears != 1 || st.ClearsRevoked != 0 || st.Capabilities.Entries != 0 {
		t.Errorf("Stats = %+v; want one Clear and no answer left", st)
	}
}

// An id not on record is live to the cache, as IsRevoked answers it, and
// kept by none: RevocationLineage's ErrNotFound is no refusal, and no
// ancestor's revocation could reach an answer kept for it. memstore names
// it not found.
func TestRevocationCacheKeepsNoAnswerForAnIDNotOnRecord(t *testing.T) {
	ctx := context.Background()
	s := newExampleStack()
	unknown := uuid.New()
	if _, _, err := s.records.RevocationLineage(ctx, unknown); !errors.Is(err, limes.ErrNotFound) {
		t.Fatalf("memstore RevocationLineage of an id not on record: err = %v, want ErrNotFound", err)
	}
	cache := newRevocationCache(t, limes.RevocationCacheConfig{Capabilities: s.records, TTL: cacheLongerThanTheTest})
	for range 2 {
		if revoked, err := cache.IsRevoked(ctx, unknown); err != nil || revoked {
			t.Fatalf("IsRevoked of an id not on record = %v, %v; want live", revoked, err)
		}
	}
	if st := cache.Stats().Capabilities; st.Entries != 0 || st.Hits != 0 || st.Lookups != 2 {
		t.Errorf("Stats = %+v; want both questions looked up, nothing kept", st)
	}
}
