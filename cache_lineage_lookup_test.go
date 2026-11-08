package limes_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
	"uuid"

	"github.com/oleg-tkachuk/limes"
)

// The cache is a RevocationLineageLookup: it answers a capability's lineage
// as its upstream does — the ancestors with a live answer, served again
// from the cache — and an id not on record as ErrNotFound, asked of the
// upstream once for its TTL.
func TestRevocationCacheAnswersRevocationLineage(t *testing.T) {
	ctx := context.Background()
	s := newExampleStack()
	_, token := s.issue()
	child, err := s.issuer.Delegate(ctx, limes.DelegateRequest{
		Parent:         s.verified(token),
		Subject:        limes.Principal{Type: limes.PrincipalTypeAgent, TenantID: s.tenantID, Subject: "worker"},
		TTL:            time.Minute,
		InheritCaveats: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	want, err := s.records.RevocationLineage(ctx, child.Capability.ID)
	if err != nil || len(want.Ancestors) == 0 {
		t.Fatalf("the store's answer = %+v, %v; want the parent among its ancestors", want, err)
	}
	upstream := &countingLineage{Store: s.records}
	var lookup limes.RevocationLineageLookup = newRevocationCache(t, limes.RevocationCacheConfig{Capabilities: upstream})

	for round := range 2 {
		got, err := lookup.RevocationLineage(ctx, child.Capability.ID)
		if err != nil || got.Revoked || !slices.Equal(got.Ancestors, want.Ancestors) {
			t.Errorf("round %d: RevocationLineage = %+v, %v; want %v, not revoked", round, got, err, want.Ancestors)
		}
	}
	if n := upstream.asked.Load(); n != 1 {
		t.Errorf("the upstream was asked %d times, want once: the second answer is the cache's", n)
	}

	unknown := uuid.New()
	for round := range 2 {
		if _, err := lookup.RevocationLineage(ctx, unknown); !errors.Is(err, limes.ErrNotFound) {
			t.Errorf("round %d: an id not on record: %v, want ErrNotFound", round, err)
		}
	}
	if n := upstream.asked.Load(); n != 2 {
		t.Errorf("the upstream was asked %d times, want twice: an id not on record is asked once", n)
	}
}

// A cache over an upstream that names no lineage cannot tell an id not on
// record from one not revoked, so its RevocationLineage is an error, never
// an answer that would read as not revoked.
func TestRevocationCacheOverNoLineageNamesNone(t *testing.T) {
	f := newRecordFixture(t)
	cache := newRevocationCache(t, limes.RevocationCacheConfig{Capabilities: isRevokedOnly{f.records}})
	for name, id := range map[string]uuid.UUID{"on record": f.id, "not on record": uuid.New()} {
		if a, err := cache.RevocationLineage(context.Background(), id); !errors.Is(err, limes.ErrNoRevocationLineage) {
			t.Errorf("%s: RevocationLineage = %+v, %v; want ErrNoRevocationLineage", name, a, err)
		}
	}
}

// A lookup of the consumer's own that wraps a cache keeps the record check:
// over a cache whose upstream names lineage it refuses a capability not on
// record; over one whose upstream does not, it fails every verification
// rather than accept one.
func TestRequireRecordThroughALookupOverACache(t *testing.T) {
	f := newRecordFixture(t)
	blind := lookupOverCache{newRevocationCache(t, limes.RevocationCacheConfig{Capabilities: f.blind})}
	if err := f.verify(f.verifier(t, blind, true), f.token); !errors.Is(err, limes.ErrUnrecorded) {
		t.Errorf("over a cache blind to the record: %v, want ErrUnrecorded", err)
	}
	recorded := lookupOverCache{newRevocationCache(t, limes.RevocationCacheConfig{Capabilities: f.records})}
	if err := f.verify(f.verifier(t, recorded, true), f.token); err != nil {
		t.Errorf("over a cache that holds the record: %v, want accepted", err)
	}
	noLineage := lookupOverCache{newRevocationCache(t, limes.RevocationCacheConfig{Capabilities: isRevokedOnly{f.records}})}
	if err := f.verify(f.verifier(t, noLineage, true), f.token); !errors.Is(err, limes.ErrNoRevocationLineage) {
		t.Errorf("over a cache whose upstream names no lineage: %v, want ErrNoRevocationLineage", err)
	}
}
