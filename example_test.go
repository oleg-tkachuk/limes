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
	"github.com/oleg-tkachuk/limes/dpop"
	"github.com/oleg-tkachuk/limes/jwks"
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
	verifier *limes.Verifier
	records  *memstore.Store
	usage    *memstore.UsageStore[struct{}]
	tenantID uuid.UUID
}

func newExampleStack() exampleStack {
	key, err := limes.GenerateKey()
	kid, pub, priv := key.KeyID, key.Public(), key.Private
	if err != nil {
		log.Fatal(err)
	}
	records, err := memstore.NewStore(memstore.StoreConfig{})
	if err != nil {
		log.Fatal(err)
	}
	usage, err := memstore.NewUsageStore(memstore.UsageStoreConfig[struct{}]{Records: records})
	if err != nil {
		log.Fatal(err)
	}
	signer, err := limes.NewSigner(limes.SignerConfig{KeyID: kid, Key: priv})
	if err != nil {
		log.Fatal(err)
	}
	issuer, err := limes.NewIssuer(limes.IssuerConfig{Signer: signer, Store: records, IssuerName: exampleIssuerName})
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
	verifier, err := limes.NewVerifier(limes.VerifierConfig{
		Keys:               exampleKeys(kid, pub),
		Revocations:        records,
		TrustedIssuers:     []string{exampleIssuerName},
		AcceptBiscuit:      true,
		BiscuitRevocations: records,
		DPoP:               proofs,
	})
	if err != nil {
		log.Fatal(err)
	}
	return exampleStack{
		issuer: issuer, verifier: verifier, records: records,
		usage: usage, tenantID: uuid.New(),
	}
}

// verified is the capability token grants, as the verifier accepts it.
func (s exampleStack) verified(token string) limes.VerifiedCapability {
	vc, err := s.verifier.Verify(context.Background(), limes.VerifyRequest{Token: token, Audience: exampleAudience})
	if err != nil {
		log.Fatal(err)
	}
	return vc
}

// issue mints a capability for an agent: get and share under corpus/public/,
// 25.00 USD and 500 requests, for 15 minutes.
func (s exampleStack) issue() (limes.Capability, string) {
	cResult, err := s.issuer.Issue(context.Background(), limes.IssueRequest{
		IssuedBy: limes.Principal{Subject: "operator@example.com"},
		Subject: limes.Principal{
			Type: limes.PrincipalTypeAgent, TenantID: s.tenantID, Subject: "research-orchestrator",
		},
		Audience: []string{exampleAudience},
		TTL:      15 * time.Minute,
		Caveats: limes.Caveats{
			Ops:              []limes.Op{limes.OpGet, limes.OpShare},
			ResourcePrefixes: []string{"corpus/public/"},
			Requests:         limes.RequestCeiling{Max: 500},
			Budget:           limes.BudgetCeiling{Max: limes.MustParseAmount("25.00"), UnitCode: "USD"},
		},
	})
	c, token := cResult.Capability, cResult.Token
	if err != nil {
		log.Fatal(err)
	}
	return c, token
}

func ExampleIssuer_Issue() {
	ctx := context.Background()
	key, _ := limes.GenerateKey()
	kid, pub, priv := key.KeyID, key.Public(), key.Private

	records, _ := memstore.NewStore(memstore.StoreConfig{}) // your Store
	signer, _ := limes.NewSigner(limes.SignerConfig{KeyID: kid, Key: priv})
	issuer, _ := limes.NewIssuer(limes.IssuerConfig{
		Signer: signer, Store: records, IssuerName: exampleIssuerName,
	})
	verifier, _ := limes.NewVerifier(limes.VerifierConfig{
		Keys:           exampleKeys(kid, pub),
		Revocations:    records,
		TrustedIssuers: []string{exampleIssuerName},
	})

	cResult, err := issuer.Issue(ctx, limes.IssueRequest{
		IssuedBy: limes.Principal{Subject: "operator@example.com"},
		Subject: limes.Principal{
			Type: limes.PrincipalTypeAgent, TenantID: uuid.New(), Subject: "research-orchestrator",
		},
		Audience: []string{exampleAudience},
		TTL:      15 * time.Minute,
		Caveats: limes.Caveats{
			Ops:              []limes.Op{limes.OpGet},
			ResourcePrefixes: []string{"corpus/public/"},
			Budget:           limes.BudgetCeiling{Max: limes.MustParseAmount("25.00"), UnitCode: "USD"},
		},
	})
	c, token := cResult.Capability, cResult.Token
	if err != nil {
		log.Fatal(err)
	}

	got, err := verifier.Verify(ctx, limes.VerifyRequest{Token: token, Audience: exampleAudience})
	fmt.Println(err == nil, got.Capability().ID == c.ID)
	_, err = verifier.Verify(ctx, limes.VerifyRequest{Token: token, Audience: "another-service"})
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
	_, token := s.issue()
	// The orchestrator presents its token; the parent is what the verifier
	// accepted.
	parent := s.verified(token)

	worker := limes.Principal{Type: limes.PrincipalTypeAgent, TenantID: s.tenantID, Subject: "sub-worker"}
	childResult, err := s.issuer.Delegate(ctx, limes.DelegateRequest{
		Parent:  parent,
		Subject: worker,
		TTL:     2 * time.Minute,
		Caveats: limes.Caveats{
			Ops:              []limes.Op{limes.OpGet},
			ResourcePrefixes: []string{"corpus/public/2026/"},
			Requests:         limes.RequestCeiling{Max: 50},
			Budget:           limes.BudgetCeiling{Max: limes.MustParseAmount("2.00"), UnitCode: "USD"},
		},
	})
	child := childResult.Capability
	fmt.Println(err, child.ParentID == parent.Capability().ID)

	// Asking for more than the parent holds is refused.
	_, err = s.issuer.Delegate(ctx, limes.DelegateRequest{
		Parent:  parent,
		Subject: worker,
		TTL:     time.Minute,
		Caveats: limes.Caveats{
			Ops:    []limes.Op{limes.OpGet},
			Budget: limes.BudgetCeiling{Max: limes.MustParseAmount("999"), UnitCode: "USD"},
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
	c, token := s.issue()

	fullResult, err := s.issuer.Biscuit(context.Background(), limes.BiscuitRequest{Capability: s.verified(token)})
	full := fullResult.Token
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

	got, err := s.verifier.Verify(ctx, limes.VerifyRequest{Token: narrow, Audience: exampleAudience})
	fmt.Println(err, got.Capability().ID == c.ID, got.Capability().Caveats.Ops, got.Capability().Caveats.ResourcePrefixes)
	// Output:
	// <nil> true [get] [corpus/public/2026/]
}

func Example_proofOfPossession() {
	ctx := context.Background()
	s := newExampleStack()
	const method, url = "POST", "https://data.example.com/objects/get"

	// The capability is bound to a key the agent keeps.
	_, agentKey, _ := ed25519.GenerateKey(rand.Reader)
	jkt, _ := dpop.Thumbprint(agentKey.Public())
	tokenResult, err := s.issuer.Issue(ctx, limes.IssueRequest{
		IssuedBy: limes.Principal{Subject: "operator@example.com"},
		Subject:  limes.Principal{Type: limes.PrincipalTypeAgent, TenantID: s.tenantID, Subject: "worker"},
		Audience: []string{exampleAudience}, TTL: 15 * time.Minute,
		Caveats:         limes.Caveats{Ops: []limes.Op{limes.OpGet}},
		ConfirmationJKT: jkt,
	})
	token := tokenResult.Token
	if err != nil {
		log.Fatal(err)
	}

	// Without a proof the token is refused: a copy of it is useless.
	_, err = s.verifier.Verify(ctx, limes.VerifyRequest{Token: token, Audience: exampleAudience})
	fmt.Println(errors.Is(err, dpop.ErrRequired))

	// The client signs a fresh proof per request.
	proof, _ := dpop.Sign(ctx, agentKey, dpop.SignRequest{Method: method, URL: url, Token: token, IssuedAt: time.Now()})
	req := limes.VerifyRequest{
		Token: token, Audience: exampleAudience,
		DPoP: dpop.Proof{JWT: proof, Method: method, URL: url},
	}
	_, err = s.verifier.Verify(ctx, req)
	fmt.Println(err)

	// The same proof a second time is a replay.
	_, err = s.verifier.Verify(ctx, req)
	fmt.Println(errors.Is(err, dpop.ErrReplayed))
	// Output:
	// true
	// <nil>
	// true
}

func ExampleVerifier_Authorize() {
	ctx := context.Background()
	s := newExampleStack()
	_, token := s.issue()

	// One call: the token, then every caveat that needs no state.
	authorize := func(op limes.Op, resource string) error {
		_, err := s.verifier.Authorize(ctx, limes.AuthorizeRequest{
			Verify: limes.VerifyRequest{Token: token, Audience: exampleAudience},
			Check:  limes.CheckRequest{Op: op, Resource: resource},
		})
		return err
	}
	fmt.Println(authorize(limes.OpGet, "corpus/public/report.pdf"))
	fmt.Println(errors.Is(authorize(limes.OpDelete, "corpus/public/report.pdf"), limes.ErrOpNotAllowed))
	fmt.Println(errors.Is(authorize(limes.OpGet, "corpus/private/x"), limes.ErrResourceNotAllowed))
	// Output:
	// <nil>
	// true
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
	_, token := s.issue()
	// The ceilings come from what the verifier accepted, never from the caller.
	subject := s.verified(token).MeterSubject()

	charge := func(amount string) (limes.ChargeReceipt, error) {
		return s.usage.Charge(ctx, limes.ChargeRequest{
			Subject: subject, Amount: limes.MustParseAmount(amount), Op: "search",
		}, nil)
	}

	receipt, err := charge("0.35")
	fmt.Println(err, receipt.SpentAmount)

	// Over the ceiling: refused, and no counter moves.
	_, err = charge("999")
	fmt.Println(errors.Is(err, limes.ErrBudgetExceeded))

	refund, err := s.usage.Refund(ctx, limes.RefundRequest{ChargeID: receipt.ChargeID, Remaining: true})
	fmt.Println(err, refund.Amount)
	// Output:
	// <nil> 0.35
	// true
	// <nil> 0.35
}

// A test of a report needs charges at chosen times. Build the stores on a
// clock the test sets, record a capability that outlives the period, and
// move the clock forward before each charge: a charge carries the store's
// time, so it lands where the clock says, and no test writes a store's
// tables.
func Example_backdatedCharges() {
	ctx := context.Background()
	start := time.Date(2026, time.March, 2, 9, 0, 0, 0, time.UTC) // a Monday
	at := start
	clock := func() time.Time { return at }
	records, err := memstore.NewStore(memstore.StoreConfig{Now: clock})
	if err != nil {
		log.Fatal(err)
	}
	usage, err := memstore.NewUsageStore(memstore.UsageStoreConfig[struct{}]{Records: records, Now: clock})
	if err != nil {
		log.Fatal(err)
	}
	ledger := memstore.NewLedger(usage)

	// The meter does not read expiry, but a capability that expired before
	// a charge's time would make a report the test cannot reproduce live.
	const period = 14 * 24 * time.Hour
	c := limes.Capability{
		ID: uuid.New(), IssuedAt: start, ExpiresAt: start.Add(period),
		Subject: limes.Principal{Type: limes.PrincipalTypeService, TenantID: uuid.New(), Subject: "svc:reporting"},
		Caveats: limes.Caveats{Requests: limes.RequestCeiling{Unlimited: true}, Budget: limes.BudgetCeiling{Unlimited: true, UnitCode: "USD"}},
	}
	if err := records.Insert(ctx, limes.InsertRequest{Capability: c}); err != nil {
		log.Fatal(err)
	}
	subject, err := limes.StoredMeterSubject(ctx, records, c.ID)
	if err != nil {
		log.Fatal(err)
	}
	for _, day := range []int{0, 1, 8} { // Monday, Tuesday, and Tuesday a week on
		at = start.AddDate(0, 0, day)
		if _, err := usage.Charge(ctx, limes.ChargeRequest{Subject: subject, Amount: limes.MustParseAmount("1.5")}, nil); err != nil {
			log.Fatal(err)
		}
	}

	page, err := ledger.SumCharges(ctx, limes.SumChargesRequest{
		TenantID: c.Subject.TenantID, From: start, To: start.Add(period), Bucket: limes.ChargeBucketWeek,
	})
	if err != nil {
		log.Fatal(err)
	}
	for _, sum := range page.Items {
		fmt.Println(sum.BucketStart.Format(time.DateOnly), sum.Charged, sum.Charges)
	}
	// Output:
	// 2026-03-02 3 2
	// 2026-03-09 1.5 1
}

func Example_revoke() {
	ctx := context.Background()
	s := newExampleStack()
	c, token := s.issue()

	err := s.records.Revoke(ctx, limes.RevokeRequest{
		CapabilityID: c.ID, Note: "agent looping", Actor: "operator@example.com", CascadeChildren: true,
	})
	fmt.Println(err)

	_, err = s.verifier.Verify(ctx, limes.VerifyRequest{Token: token, Audience: exampleAudience})
	fmt.Println(errors.Is(err, limes.ErrRevoked))
	// Output:
	// <nil>
	// true
}

func ExampleAllByPrincipal() {
	ctx := context.Background()
	s := newExampleStack()
	for range 3 {
		s.issue()
	}

	// One page per capability, to show the walk crossing pages.
	const pageSize = 1
	n := 0
	for _, err := range limes.AllByPrincipal(ctx, s.records, limes.ListByPrincipalRequest{
		TenantID: s.tenantID, PrincipalType: limes.PrincipalTypeAgent, Subject: "research-orchestrator",
		Limit: pageSize,
	}) {
		if err != nil {
			log.Fatal(err)
		}
		n++
	}
	fmt.Println(n)
	// Output:
	// 3
}

// exampleKeys is a jwks.Static holding pub under kid.
func exampleKeys(kid string, pub ed25519.PublicKey) *jwks.Static {
	keys, err := jwks.NewStatic(jwks.StaticConfig{Keys: map[string]ed25519.PublicKey{kid: pub}})
	if err != nil {
		log.Fatal(err)
	}
	return keys
}
