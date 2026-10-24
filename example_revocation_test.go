package limes_test

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/memstore"
)

func ExampleNewRevocationCache() {
	ctx := context.Background()
	s := newExampleStack()
	// One cache answers for capabilities and Biscuit copies; memstore's
	// Store is both upstreams.
	cache, err := limes.NewRevocationCache(limes.RevocationCacheConfig{
		Capabilities: s.records,
		Biscuits:     s.records,
		TTL:          time.Second,
	})
	if err != nil {
		log.Fatal(err)
	}

	c, _ := s.issue()
	id := c.ID
	for range 3 {
		if _, err := cache.IsRevoked(ctx, id); err != nil {
			log.Fatal(err)
		}
	}
	stats := cache.Stats().Capabilities
	fmt.Println(stats.Misses, stats.Hits, stats.Lookups, stats.Entries)

	// A revocation notification clears every answer at once.
	cache.Clear()
	fmt.Println(cache.Stats().Clears, cache.Stats().Capabilities.Entries)
	// Output:
	// 1 2 1 1
	// 1 0
}

func ExampleRevocationCache_Sweep() {
	ctx := context.Background()
	s := newExampleStack()
	now := time.Date(2026, time.March, 2, 9, 0, 0, 0, time.UTC)
	cache, err := limes.NewRevocationCache(limes.RevocationCacheConfig{
		Capabilities: s.records, TTL: time.Second, Now: func() time.Time { return now },
	})
	if err != nil {
		log.Fatal(err)
	}
	for range 2 {
		c, _ := s.issue()
		if _, err := cache.IsRevoked(ctx, c.ID); err != nil {
			log.Fatal(err)
		}
	}
	now = now.Add(2 * time.Second)
	fmt.Println(cache.Sweep(), cache.Stats().Capabilities.Entries)
	// Output:
	// 2 0
}

func ExampleRevocationCache_IsBiscuitRevoked() {
	ctx := context.Background()
	s := newExampleStack()
	_, token := s.issue()
	biscuit, err := s.issuer.Biscuit(ctx, limes.BiscuitRequest{Capability: s.verified(token)})
	if err != nil {
		log.Fatal(err)
	}
	copyOf, err := s.verifier.BiscuitCopy(ctx, biscuit.Token)
	if err != nil {
		log.Fatal(err)
	}
	cache, err := limes.NewRevocationCache(limes.RevocationCacheConfig{Capabilities: s.records, Biscuits: s.records})
	if err != nil {
		log.Fatal(err)
	}
	revoked, err := cache.IsBiscuitRevoked(ctx, [][]byte{copyOf.RevocationID()})
	fmt.Println(revoked, err)

	// A cache built without a Biscuit upstream refuses the question.
	capsOnly, err := limes.NewRevocationCache(limes.RevocationCacheConfig{Capabilities: s.records})
	if err != nil {
		log.Fatal(err)
	}
	_, err = capsOnly.IsBiscuitRevoked(ctx, [][]byte{copyOf.RevocationID()})
	fmt.Println(err != nil)
	// Output:
	// false <nil>
	// true
}

func ExampleIssuer_Biscuit() {
	ctx := context.Background()
	s := newExampleStack()
	c, token := s.issue()

	// The Biscuit form is the same capability: same id, revoked with it.
	biscuit, err := s.issuer.Biscuit(ctx, limes.BiscuitRequest{Capability: s.verified(token)})
	if err != nil {
		log.Fatal(err)
	}
	got := s.verified(biscuit.Token)
	fmt.Println(got.FromBiscuit(), got.Capability().ID == c.ID)

	// A copy is not wrapped again: its holder attenuates it instead.
	_, err = s.issuer.Biscuit(ctx, limes.BiscuitRequest{Capability: got})
	fmt.Println(errors.Is(err, limes.ErrInvalidRequest))
	// Output:
	// true true
	// true
}

func ExampleVerifier_BiscuitCopy() {
	ctx := context.Background()
	s := newExampleStack()
	c, token := s.issue()
	biscuit, err := s.issuer.Biscuit(ctx, limes.BiscuitRequest{Capability: s.verified(token)})
	if err != nil {
		log.Fatal(err)
	}
	// Two copies attenuated apart, one handed to each sub-agent.
	first, _ := limes.Attenuate(biscuit.Token, limes.Attenuation{ResourcePrefixes: []string{"corpus/public/a/"}})
	second, _ := limes.Attenuate(biscuit.Token, limes.Attenuation{ResourcePrefixes: []string{"corpus/public/b/"}})

	// Revoke the first copy alone, by what the verifier read from it.
	copyOf, err := s.verifier.BiscuitCopy(ctx, first)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(copyOf.CapabilityID() == c.ID)
	if err := s.records.RevokeBiscuit(ctx, limes.RevokeBiscuitRequest{Copy: copyOf, Note: "leaked", Actor: "operator@example.com"}); err != nil {
		log.Fatal(err)
	}

	entry, err := s.records.GetBiscuitRevocation(ctx, copyOf)
	fmt.Println(err, entry.Note)

	_, err = s.verifier.Verify(ctx, limes.VerifyRequest{Token: first, Audience: exampleAudience})
	fmt.Println(errors.Is(err, limes.ErrRevoked))
	_, err = s.verifier.Verify(ctx, limes.VerifyRequest{Token: second, Audience: exampleAudience})
	fmt.Println(err)
	// Output:
	// true
	// <nil> leaked
	// true
	// <nil>
}

func ExampleVerifiedCapability_Copies() {
	ctx := context.Background()
	s := newExampleStack()
	_, token := s.issue()
	biscuit, err := s.issuer.Biscuit(ctx, limes.BiscuitRequest{Capability: s.verified(token)})
	if err != nil {
		log.Fatal(err)
	}
	// A block that sets no limit of its own makes no copy a meter counts.
	narrow, _ := limes.Attenuate(biscuit.Token, limes.Attenuation{Ops: []limes.Op{limes.OpGet}})
	fmt.Println(len(s.verified(narrow).Copies()))

	// One that does needs a verifier whose meter counts copies
	// (BiscuitConfig.MeterCopies); this one's does not.
	limited, _ := limes.Attenuate(biscuit.Token, limes.Attenuation{Requests: &limes.RequestCeiling{Max: 5}})
	_, err = s.verifier.Verify(ctx, limes.VerifyRequest{Token: limited, Audience: exampleAudience})
	fmt.Println(errors.Is(err, limes.ErrCopyCountersNotMetered))
	// Output:
	// 0
	// true
}

func ExampleVerifiedCapability_MeterSubject() {
	s := newExampleStack()
	c, token := s.issue()
	subject := s.verified(token).MeterSubject()
	fmt.Println(subject.CapabilityID() == c.ID, subject.TenantID() == s.tenantID)
	fmt.Println(subject.Requests(), subject.Budget(), subject.UnitCode(), subject.CopyQuota(), len(subject.Copies()))
	fmt.Println(subject.Validate(), subject.ValidateAmounts())
	// The zero subject names nothing, and a meter refuses it.
	fmt.Println(errors.Is(limes.MeterSubject{}.Validate(), limes.ErrInvalidRequest))
	// Output:
	// true true
	// 500 25 USD USD 0 0
	// <nil> <nil>
	// true
}

func ExampleStoredMeterSubject() {
	ctx := context.Background()
	s := newExampleStack()
	c, _ := s.issue()

	// A cost reported with no token in hand counts against the ceilings on record.
	subject, err := limes.StoredMeterSubject(ctx, s.records, c.ID)
	fmt.Println(err, subject.Budget())

	_, err = limes.StoredMeterSubject(ctx, s.records, uuid.New())
	fmt.Println(errors.Is(err, limes.ErrNotFound))
	// Output:
	// <nil> 25 USD
	// true
}

func ExampleAllPages() {
	ctx := context.Background()
	pages := map[string]limes.Page[string]{
		"":   {Items: []string{"a", "b"}, Next: "p2"},
		"p2": {Items: []string{"c"}},
	}
	page := func(_ context.Context, cursor string) (limes.Page[string], error) {
		return pages[cursor], nil
	}
	var items []string
	for item, err := range limes.AllPages(ctx, "", page) {
		if err != nil {
			log.Fatal(err)
		}
		items = append(items, item)
	}
	fmt.Println(items)

	// A page pointing back at one already fetched ends the walk.
	pages["p2"] = limes.Page[string]{Items: []string{"c"}, Next: "p2"}
	for _, err := range limes.AllPages(ctx, "", page) {
		if err != nil {
			fmt.Println(errors.Is(err, limes.ErrCursorRepeated))
		}
	}
	// Output:
	// [a b c]
	// true
}

func ExamplePageLimit() {
	fmt.Println(limes.PageLimit(0), limes.PageLimit(20), limes.PageLimit(10_000))
	// Output:
	// 50 20 500
}

func ExampleNewIssuer() {
	// Wiring is checked once, at construction, rather than on the first request.
	_, err := limes.NewIssuer(limes.IssuerConfig{IssuerName: exampleIssuerName})
	fmt.Println(err != nil)
	// Output:
	// true
}

func ExampleNewVerifier() {
	records, err := memstore.NewStore(memstore.StoreConfig{})
	if err != nil {
		log.Fatal(err)
	}
	key, err := limes.GenerateKey()
	if err != nil {
		log.Fatal(err)
	}
	verifier, err := limes.NewVerifier(limes.VerifierConfig{
		Keys:           exampleKeys(key.KeyID, key.Public()),
		Revocations:    records,
		TrustedIssuers: []string{exampleIssuerName},
	})
	fmt.Println(verifier != nil, err)

	// Keys, Revocations and TrustedIssuers are required.
	_, err = limes.NewVerifier(limes.VerifierConfig{Revocations: records, TrustedIssuers: []string{exampleIssuerName}})
	fmt.Println(err != nil)
	// Output:
	// true <nil>
	// true
}

func ExampleVerifier_Verify() {
	ctx := context.Background()
	s := newExampleStack()
	_, token := s.issue()

	got, err := s.verifier.Verify(ctx, limes.VerifyRequest{
		Token: token, Audience: exampleAudience, Scheme: limes.AuthSchemeBearer,
	})
	fmt.Println(err, got.Capability().Subject.Subject, got.FromBiscuit())

	// An unbound token presented under DPoP is a downgrade the other way.
	_, err = s.verifier.Verify(ctx, limes.VerifyRequest{
		Token: token, Audience: exampleAudience, Scheme: limes.AuthSchemeDPoP,
	})
	fmt.Println(errors.Is(err, limes.ErrAuthSchemeMismatch), errors.Is(err, limes.ErrUnauthenticated))
	// Output:
	// <nil> research-orchestrator false
	// true true
}
