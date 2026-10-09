// Command example is the quickstart walkthrough as a runnable program:
// issue → verify → delegate → revoke, against the in-memory reference store.
//
// It deliberately mentions no object storage and no database. If this program
// needs either to run, the module has broken its central promise.
//
//	go run ./example
package main

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/memstore"
)

func main() {
	ctx := context.Background()
	tenantID := uuid.New()

	// 1. Mint a signing key. `kid` identifies it so tokens stay verifiable
	//    across rotation.
	kid, pub, priv, err := limes.GenerateEd25519Keypair()
	must(err, "generate keypair")

	// 2. Wire an issuer and a verifier. Storage is yours — here, in memory.
	//    UsageStore is generic in the transaction handle; with no transactions
	//    of our own we instantiate it over struct{} and pass nil callbacks.
	records := memstore.New[struct{}]()
	usage := memstore.NewUsage[struct{}](records)

	signer, err := limes.NewEd25519Signer(kid, priv)
	must(err, "signer")

	issuer, err := limes.NewIssuer(limes.IssuerConfig{
		Signer: signer, Store: records, IssuerName: "example-issuer",
	})
	must(err, "issuer")

	verifier, err := limes.NewStandardVerifier(limes.VerifierConfig{
		Keys:           limes.NewStaticKeyResolver(map[string]ed25519.PublicKey{kid: pub}),
		Revocations:    records,
		TrustedIssuers: []string{"example-issuer"},
	})
	must(err, "verifier")

	// 3. Issue to an orchestrator agent. Everything the authority permits is
	//    described by the token itself — no role, no long-lived secret.
	parent, parentToken, err := issuer.Issue(ctx, limes.IssueRequest{
		// Who asked for it — distinct from who it authorises, and required.
		IssuedBy: limes.Principal{Type: limes.PrincipalUser, TenantID: tenantID, Subject: "operator@example.com"},
		Subject: limes.Principal{
			Type:     limes.PrincipalAgent,
			TenantID: tenantID,
			Subject:  "research-orchestrator",
			Agent: &limes.AgentPrincipal{
				AgentType: "example-agent", Model: "demo", RunID: uuid.New(),
			},
		},
		Audience: []string{"my-service"},
		TTL:      15 * time.Minute,
		Caveats: limes.Caveats{
			Ops:              []limes.Op{limes.OpGet, limes.OpShare},
			ResourcePrefixes: []string{"corpus/public/"},
			MaxRequests:      500,
			MaxBudgetAmount:  limes.MustParseAmount("25.00"),
			UnitCode:         "USD",
		},
	})
	must(err, "issue")
	fmt.Printf("issued   %s  (%d bytes)\n", parent.ID, len(parentToken))

	// 4. Verify on every call. Local — no round trip to the issuer.
	if _, err := verifier.Verify(ctx, parentToken, "my-service"); err != nil {
		log.Fatalf("verify: %v", err)
	}
	fmt.Println("verified ok")

	// A token minted for one audience must not verify for another.
	if _, err := verifier.Verify(ctx, parentToken, "another-service"); err == nil {
		log.Fatal("audience gate did not hold")
	}
	fmt.Println("verified rejected for the wrong audience")

	// 5. Delegate to a sub-agent — strictly narrower, without calling back to
	//    any admin API. An orchestrator can attenuate, never escalate.
	child, _, err := issuer.Delegate(ctx, limes.DelegateRequest{
		Parent: parent,
		Subject: limes.Principal{
			Type: limes.PrincipalAgent, TenantID: tenantID, Subject: "sub-worker",
			Agent: &limes.AgentPrincipal{
				AgentType: "example-agent", ParentAgentID: parent.ID,
			},
		},
		Audience: []string{"my-service"},
		TTL:      2 * time.Minute,
		Caveats: limes.Caveats{
			Ops:              []limes.Op{limes.OpGet},         // dropped OpShare
			ResourcePrefixes: []string{"corpus/public/2026/"}, // narrowed
			MaxRequests:      50,
			MaxBudgetAmount:  limes.MustParseAmount("2.00"), // 25.00 → 2.00
			UnitCode:         "USD",
		},
	})
	must(err, "delegate")
	fmt.Printf("delegated %s (child of %s)\n", child.ID, parent.ID)

	// Widening is refused — this is the property that makes the model safe to
	// hand to an agent.
	_, _, err = issuer.Delegate(ctx, limes.DelegateRequest{
		Parent: parent,
		Subject: limes.Principal{
			Type: limes.PrincipalAgent, TenantID: tenantID, Subject: "greedy-worker",
		},
		Audience: []string{"my-service"},
		TTL:      time.Minute,
		Caveats: limes.Caveats{
			Ops:             []limes.Op{limes.OpGet},
			MaxBudgetAmount: limes.MustParseAmount("999"), // wider than the parent's 25.00
			UnitCode:        "USD",
		},
	})
	if !errors.Is(err, limes.ErrDelegationTooWide) {
		log.Fatalf("widening delegation should have been refused, got %v", err)
	}
	fmt.Println("widening refused with ErrDelegationTooWide")

	// 6. Charge against the budget. The capability's ceiling, each ancestor's
	//    and the tenant's all apply; a rejection by any leaves every counter
	//    untouched, so a retry is always safe.
	receipt, err := usage.Charge(ctx, limes.ChargeRequest{CapabilityID: parent.ID, TenantID: tenantID, Amount: limes.MustParseAmount("0.35"), MaxBudget: parent.Caveats.MaxBudgetAmount, UnitCode: "USD", Op: "search", Actor: "research-orchestrator"}, nil)
	must(err, "charge")
	fmt.Printf("charged  0.35 USD, spent now %s\n", receipt.Spent)

	_, err = usage.Charge(ctx, limes.ChargeRequest{CapabilityID: parent.ID, TenantID: tenantID, Amount: limes.MustParseAmount("999"), MaxBudget: parent.Caveats.MaxBudgetAmount, UnitCode: "USD", Op: "search", Actor: "research-orchestrator"}, nil)
	if !errors.Is(err, limes.ErrBudgetExceeded) {
		log.Fatalf("over-budget charge should have been refused, got %v", err)
	}
	fmt.Println("over-budget charge refused with ErrBudgetExceeded")

	// 7. Revoke, cascading to everything the orchestrator delegated.
	must(records.Revoke(ctx, limes.RevokeRequest{
		ID: parent.ID, Reason: "example finished", Actor: "operator", CascadeChildren: true,
	}), "revoke")

	childRevoked, err := records.IsRevoked(ctx, child.ID)
	must(err, "is-revoked")
	if !childRevoked {
		log.Fatal("cascade did not reach the delegated child")
	}
	fmt.Println("revoked  parent + cascaded to the child")

	if _, err := verifier.Verify(ctx, parentToken, "my-service"); !errors.Is(err, limes.ErrRevoked) {
		log.Fatalf("revoked token should fail with ErrRevoked, got %v", err)
	}
	fmt.Println("verified now rejects the revoked token")
}

func must(err error, what string) {
	if err != nil {
		log.Fatalf("%s: %v", what, err)
	}
}
