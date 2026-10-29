package biscuit_test

// The Biscuit form beside a JWT capability: the same capability, which its
// holder narrows offline and the issuer can revoke a copy of.

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"log"
	"time"
	"uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/biscuit"
	"github.com/oleg-tkachuk/limes/dpop"
	"github.com/oleg-tkachuk/limes/jwks"
	"github.com/oleg-tkachuk/limes/memstore"
)

// The examples' issuer, client and audience.
const (
	exampleIssuerName = "example-issuer"
	exampleAudience   = "my-service"
	exampleClientID   = "example-agent"
	exampleTTL        = 15 * time.Minute
	exampleRequests   = 500
)

// exampleStack is an issuer and a verifier over one in-memory store, with
// the Biscuit form on both.
type exampleStack struct {
	issuer   *limes.Issuer
	verifier *limes.Verifier
	records  *memstore.Store
	tenantID uuid.UUID
}

func newExampleStack() exampleStack {
	key, err := limes.GenerateKey()
	if err != nil {
		log.Fatal(err)
	}
	records, err := memstore.NewStore(memstore.StoreConfig{})
	if err != nil {
		log.Fatal(err)
	}
	signer, err := limes.NewSigner(limes.SignerConfig{KeyID: key.KeyID, Key: key.Private})
	if err != nil {
		log.Fatal(err)
	}
	issuer, err := limes.NewIssuer(limes.IssuerConfig{Signer: signer, Store: records, IssuerName: exampleIssuerName, Biscuit: biscuit.Form()})
	if err != nil {
		log.Fatal(err)
	}
	replay, err := dpop.NewMemoryReplayCache(dpop.MemoryReplayCacheConfig{})
	if err != nil {
		log.Fatal(err)
	}
	proofs, err := dpop.NewVerifier(dpop.VerifierConfig{Replay: replay})
	if err != nil {
		log.Fatal(err)
	}
	keys, err := jwks.NewStatic(jwks.StaticConfig{Keys: map[string]ed25519.PublicKey{key.KeyID: key.Public()}})
	if err != nil {
		log.Fatal(err)
	}
	verifier, err := limes.NewVerifier(limes.VerifierConfig{
		Keys:           keys,
		Revocations:    records,
		TrustedIssuers: []string{exampleIssuerName},
		Biscuit:        &limes.BiscuitConfig{Revocations: records, Form: biscuit.Form()},
		DPoP:           proofs,
	})
	if err != nil {
		log.Fatal(err)
	}
	return exampleStack{issuer: issuer, verifier: verifier, records: records, tenantID: uuid.New()}
}

// verified is the capability token grants, as the verifier accepts it.
func (s exampleStack) verified(token string) limes.VerifiedCapability {
	vc, err := s.verifier.Verify(context.Background(), limes.VerifyRequest{Token: token, Audience: exampleAudience})
	if err != nil {
		log.Fatal(err)
	}
	return vc
}

// issue issues an agent of the stack's tenant a capability to get and share
// under corpus/public/.
func (s exampleStack) issue() (limes.Capability, string) {
	res, err := s.issuer.Issue(context.Background(), limes.IssueRequest{
		ClientID: exampleClientID,
		IssuedBy: limes.Principal{Subject: "operator@example.com"},
		Subject:  limes.Principal{Type: limes.PrincipalTypeAgent, TenantID: s.tenantID, Subject: "research-orchestrator"},
		Audience: []string{exampleAudience},
		TTL:      exampleTTL,
		Caveats: limes.Caveats{
			Ops:              []limes.Op{limes.OpGet, limes.OpShare},
			ResourcePrefixes: []string{"corpus/public/"},
			Requests:         limes.RequestCeiling{Max: exampleRequests},
			Budget:           limes.BudgetCeiling{Unlimited: true, UnitCode: "USD"},
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	return res.Capability, res.Token
}

func Example_revocationCache() {
	ctx := context.Background()
	s := newExampleStack()
	_, token := s.issue()
	sealed, err := s.issuer.Biscuit(ctx, limes.BiscuitRequest{Capability: s.verified(token)})
	if err != nil {
		log.Fatal(err)
	}
	copyOf, err := s.verifier.BiscuitCopy(ctx, sealed.Token)
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

func Example_issuerBiscuit() {
	ctx := context.Background()
	s := newExampleStack()
	c, token := s.issue()

	// The Biscuit form is the same capability: same id, revoked with it.
	sealed, err := s.issuer.Biscuit(ctx, limes.BiscuitRequest{Capability: s.verified(token)})
	if err != nil {
		log.Fatal(err)
	}
	got := s.verified(sealed.Token)
	fmt.Println(got.FromBiscuit(), got.Capability().ID == c.ID)

	// A copy is not wrapped again: its holder attenuates it instead.
	_, err = s.issuer.Biscuit(ctx, limes.BiscuitRequest{Capability: got})
	fmt.Println(errors.Is(err, limes.ErrInvalidRequest))
	// Output:
	// true true
	// true
}

func Example_revokeACopy() {
	ctx := context.Background()
	s := newExampleStack()
	c, token := s.issue()
	sealed, err := s.issuer.Biscuit(ctx, limes.BiscuitRequest{Capability: s.verified(token)})
	if err != nil {
		log.Fatal(err)
	}
	// Two copies attenuated apart, one handed to each sub-agent.
	first, _ := biscuit.Attenuate(sealed.Token, limes.Attenuation{ResourcePrefixes: []string{"corpus/public/a/"}})
	second, _ := biscuit.Attenuate(sealed.Token, limes.Attenuation{ResourcePrefixes: []string{"corpus/public/b/"}})

	// Revoke the first copy alone, by what the verifier read from it.
	copyOf, err := s.verifier.BiscuitCopy(ctx, first)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(copyOf.CapabilityID() == c.ID)
	if _, err := s.records.RevokeBiscuit(ctx, limes.RevokeBiscuitRequest{Copy: copyOf, Note: "leaked", Actor: "operator@example.com"}); err != nil {
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

func Example_copies() {
	ctx := context.Background()
	s := newExampleStack()
	_, token := s.issue()
	sealed, err := s.issuer.Biscuit(ctx, limes.BiscuitRequest{Capability: s.verified(token)})
	if err != nil {
		log.Fatal(err)
	}
	// A block that sets no limit of its own makes no copy a meter counts.
	narrow, _ := biscuit.Attenuate(sealed.Token, limes.Attenuation{Ops: []limes.Op{limes.OpGet}})
	fmt.Println(len(s.verified(narrow).Copies()))

	// One that does needs a verifier whose meter counts copies
	// (BiscuitConfig.MeterCopies); this one's does not.
	limited, _ := biscuit.Attenuate(sealed.Token, limes.Attenuation{Requests: &limes.RequestCeiling{Max: 5}})
	_, err = s.verifier.Verify(ctx, limes.VerifyRequest{Token: limited, Audience: exampleAudience})
	fmt.Println(errors.Is(err, limes.ErrCopyCountersNotMetered))
	// Output:
	// 0
	// true
}

func ExampleAttenuate() {
	ctx := context.Background()
	s := newExampleStack()
	c, token := s.issue()

	fullResult, err := s.issuer.Biscuit(context.Background(), limes.BiscuitRequest{Capability: s.verified(token)})
	full := fullResult.Token
	if err != nil {
		log.Fatal(err)
	}
	// The holder narrows it offline: no key, no network.
	narrow, err := biscuit.Attenuate(full, limes.Attenuation{
		Ops:              []limes.Op{limes.OpGet},
		ResourcePrefixes: []string{"corpus/public/2026/"},
	})
	if err != nil {
		log.Fatal(err)
	}

	got, err := s.verifier.Verify(ctx, limes.VerifyRequest{Token: narrow, Audience: exampleAudience})
	fmt.Println(err, got.Capability().ID == c.ID, got.Capability().Caveats.Ops, got.Capability().Caveats.ResourcePrefixes)
	// Output:
	// <nil> true [get] [corpus/public/2026/]
}
