package limes_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/memstore"
)

// The issuer's name, and the audience every example's tokens are minted for.
const (
	exampleIssuerName = "example-issuer"
	exampleAudience   = "my-service"
)

// exampleStack is an issuer and a verifier over one in-memory store — the
// setup ExampleIssuer_Issue shows in full.
type exampleStack struct {
	issuer   *limes.Issuer
	verifier *limes.StandardVerifier
	records  *memstore.Store[struct{}]
	usage    *memstore.UsageStore[struct{}]
	tenantID uuid.UUID
}

func newExampleStack() exampleStack {
	kid, pub, priv, err := limes.GenerateEd25519Keypair()
	if err != nil {
		log.Fatal(err)
	}
	records := memstore.New[struct{}]()
	signer, err := limes.NewEd25519Signer(kid, priv)
	if err != nil {
		log.Fatal(err)
	}
	issuer, err := limes.NewIssuer(limes.IssuerConfig{Signer: signer, Store: records, IssuerName: exampleIssuerName})
	if err != nil {
		log.Fatal(err)
	}
	verifier, err := limes.NewStandardVerifier(limes.VerifierConfig{
		Keys:               limes.NewStaticKeyResolver(map[string]ed25519.PublicKey{kid: pub}),
		Revocations:        records,
		TrustedIssuers:     []string{exampleIssuerName},
		AcceptBiscuit:      true,
		BiscuitRevocations: records,
	})
	if err != nil {
		log.Fatal(err)
	}
	return exampleStack{
		issuer: issuer, verifier: verifier, records: records,
		usage: memstore.NewUsage[struct{}](records), tenantID: uuid.New(),
	}
}

// issue mints a capability for an agent: get and share under corpus/public/,
// 25.00 USD and 500 requests, for 15 minutes.
func (s exampleStack) issue() (limes.Capability, string) {
	c, token, err := s.issuer.Issue(context.Background(), limes.IssueRequest{
		IssuedBy: limes.Principal{Subject: "operator@example.com"},
		Subject: limes.Principal{
			Type: limes.PrincipalAgent, TenantID: s.tenantID, Subject: "research-orchestrator",
		},
		Audience: []string{exampleAudience},
		TTL:      15 * time.Minute,
		Caveats: limes.Caveats{
			Ops:              []limes.Op{limes.OpGet, limes.OpShare},
			ResourcePrefixes: []string{"corpus/public/"},
			MaxRequests:      500,
			MaxBudgetAmount:  limes.MustParseAmount("25.00"),
			UnitCode:         "USD",
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	return c, token
}

func ExampleIssuer_Issue() {
	ctx := context.Background()
	kid, pub, priv, _ := limes.GenerateEd25519Keypair()

	records := memstore.New[struct{}]() // your Store
	signer, _ := limes.NewEd25519Signer(kid, priv)
	issuer, _ := limes.NewIssuer(limes.IssuerConfig{
		Signer: signer, Store: records, IssuerName: exampleIssuerName,
	})
	verifier, _ := limes.NewStandardVerifier(limes.VerifierConfig{
		Keys:           limes.NewStaticKeyResolver(map[string]ed25519.PublicKey{kid: pub}),
		Revocations:    records,
		TrustedIssuers: []string{exampleIssuerName},
	})

	c, token, err := issuer.Issue(ctx, limes.IssueRequest{
		IssuedBy: limes.Principal{Subject: "operator@example.com"},
		Subject: limes.Principal{
			Type: limes.PrincipalAgent, TenantID: uuid.New(), Subject: "research-orchestrator",
		},
		Audience: []string{exampleAudience},
		TTL:      15 * time.Minute,
		Caveats: limes.Caveats{
			Ops:              []limes.Op{limes.OpGet},
			ResourcePrefixes: []string{"corpus/public/"},
			MaxBudgetAmount:  limes.MustParseAmount("25.00"),
			UnitCode:         "USD",
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	got, err := verifier.Verify(ctx, token, exampleAudience)
	fmt.Println(err == nil, got.ID == c.ID)
	_, err = verifier.Verify(ctx, token, "another-service")
	fmt.Println(errors.Is(err, limes.ErrAudienceMismatch))
	// Output:
	// true true
	// true
}

func ExampleCaveats_Check() {
	caveats := limes.Caveats{
		Ops:              []limes.Op{limes.OpGet},
		ResourcePrefixes: []string{"corpus/public/"},
	}

	fmt.Println(caveats.Check(limes.CheckRequest{Op: limes.OpGet, Resource: "corpus/public/report.pdf"}))

	// A prefix matches at a "/" boundary only.
	err := caveats.Check(limes.CheckRequest{Op: limes.OpGet, Resource: "corpus/public-secret/x"})
	fmt.Println(errors.Is(err, limes.ErrResourceNotAllowed), errors.Is(err, limes.ErrCaveatViolation))

	err = caveats.Check(limes.CheckRequest{Op: limes.OpPut, Resource: "corpus/public/report.pdf"})
	fmt.Println(errors.Is(err, limes.ErrOpNotAllowed))
	// Output:
	// <nil>
	// true true
	// true
}

func ExampleIssuer_Delegate() {
	ctx := context.Background()
	s := newExampleStack()
	parent, _ := s.issue()

	worker := limes.Principal{Type: limes.PrincipalAgent, TenantID: s.tenantID, Subject: "sub-worker"}
	child, _, err := s.issuer.Delegate(ctx, limes.DelegateRequest{
		Parent:  parent,
		Subject: worker,
		TTL:     2 * time.Minute,
		Caveats: limes.Caveats{
			Ops:              []limes.Op{limes.OpGet},
			ResourcePrefixes: []string{"corpus/public/2026/"},
			MaxRequests:      50,
			MaxBudgetAmount:  limes.MustParseAmount("2.00"),
			UnitCode:         "USD",
		},
	})
	fmt.Println(err, child.ParentID == parent.ID)

	// Asking for more than the parent holds is refused.
	_, _, err = s.issuer.Delegate(ctx, limes.DelegateRequest{
		Parent:  parent,
		Subject: worker,
		TTL:     time.Minute,
		Caveats: limes.Caveats{
			Ops:             []limes.Op{limes.OpGet},
			MaxBudgetAmount: limes.MustParseAmount("999"),
			UnitCode:        "USD",
		},
	})
	fmt.Println(errors.Is(err, limes.ErrDelegationTooWide))
	// Output:
	// <nil> true
	// true
}

func ExampleAttenuate() {
	ctx := context.Background()
	s := newExampleStack()
	c, _ := s.issue()

	full, err := s.issuer.Biscuit(c)
	if err != nil {
		log.Fatal(err)
	}
	// The holder narrows it offline: no key, no network.
	narrow, err := limes.Attenuate(full, limes.Attenuation{
		Ops:              []limes.Op{limes.OpGet},
		ResourcePrefixes: []string{"corpus/public/2026/"},
	})
	if err != nil {
		log.Fatal(err)
	}

	got, err := s.verifier.Verify(ctx, narrow, exampleAudience)
	fmt.Println(err, got.ID == c.ID, got.Caveats.Ops, got.Caveats.ResourcePrefixes)
	// Output:
	// <nil> true [get] [corpus/public/2026/]
}

func ExampleNewDPoPProof() {
	ctx := context.Background()
	const method, url = "POST", "https://data.example.com/objects/get"

	_, agentKey, _ := ed25519.GenerateKey(rand.Reader)
	jkt, _ := limes.KeyThumbprint(agentKey.Public())
	bound := limes.Capability{ConfirmationJKT: jkt}
	token := "the-capability-token"

	// The client signs a fresh proof per request.
	proof, _ := limes.NewDPoPProof(agentKey, method, url, token, time.Now())

	// The server checks it after Verify.
	dpop := &limes.DPoPVerifier{Replay: limes.NewMemoryReplayCache(0)}
	req := limes.DPoPRequest{Proof: proof, Method: method, URL: url, Token: token}
	fmt.Println(dpop.Check(ctx, bound, req))

	// The same proof a second time is a replay.
	fmt.Println(errors.Is(dpop.Check(ctx, bound, req), limes.ErrInvalidSignature))
	// Output:
	// <nil>
	// true
}

func ExampleParseAmount() {
	a, _ := limes.ParseAmount("0.1")
	b, _ := limes.ParseAmount("0.2")
	fmt.Println(a+b, int64(a+b))

	// Finer than a nano is refused rather than rounded.
	_, err := limes.ParseAmount("0.0000000001")
	fmt.Println(errors.Is(err, limes.ErrInvalidAmount))
	// Output:
	// 0.3 300000000
	// true
}

func ExampleMatchResource() {
	fmt.Println(limes.MatchResource("corpus/public", "corpus/public/x"))
	fmt.Println(limes.MatchResource("corpus/public", "corpus/public-secret"))
	// Output:
	// true
	// false
}

func Example_charge() {
	ctx := context.Background()
	s := newExampleStack()
	c, _ := s.issue()

	charge := func(amount string) (limes.ChargeReceipt, error) {
		return s.usage.Charge(ctx, limes.ChargeRequest{
			CapabilityID: c.ID, TenantID: s.tenantID, Amount: limes.MustParseAmount(amount),
			MaxBudget: c.Caveats.MaxBudgetAmount, UnitCode: "USD", Op: "search",
		}, nil)
	}

	receipt, err := charge("0.35")
	fmt.Println(err, receipt.Spent)

	// Over the ceiling: refused, and no counter moves.
	_, err = charge("999")
	fmt.Println(errors.Is(err, limes.ErrBudgetExceeded))

	refunded, err := s.usage.Refund(ctx, limes.RefundRequest{ChargeID: receipt.ChargeID})
	fmt.Println(err, refunded)
	// Output:
	// <nil> 0.35
	// true
	// <nil> 0.35
}

func Example_revoke() {
	ctx := context.Background()
	s := newExampleStack()
	c, token := s.issue()

	err := s.records.Revoke(ctx, limes.RevokeRequest{
		ID: c.ID, Reason: "agent looping", Actor: "operator@example.com", CascadeChildren: true,
	})
	fmt.Println(err)

	_, err = s.verifier.Verify(ctx, token, exampleAudience)
	fmt.Println(errors.Is(err, limes.ErrRevoked))
	// Output:
	// <nil>
	// true
}
