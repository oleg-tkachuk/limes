// Command quickstart is the README's walkthrough as a runnable program:
// issue → verify → delegate → charge → revoke, against the in-memory
// reference store.
//
// It deliberately mentions no object storage and no database. If this program
// needs either to run, the module has broken its central promise.
//
//	go run ./quickstart
package main

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/examples/internal/demo"
	"github.com/oleg-tkachuk/limes/jwks"
	"github.com/oleg-tkachuk/limes/memstore"
)

func main() {
	ctx := context.Background()
	log := demo.New("quickstart")
	must := func(err error, step string) { demo.Must(log, err, step) }
	tenantID := uuid.New()

	// 1. Mint a signing key. `kid` identifies it so tokens stay verifiable
	//    across rotation.
	key, err := limes.GenerateKey()
	kid, pub, priv := key.KeyID, key.Public, key.Private
	must(err, "generate keypair")

	// 2. Wire an issuer and a verifier. Storage is yours — here, in memory.
	//    UsageStore is generic in the transaction handle; with no transactions
	//    of our own we instantiate it over struct{} and pass nil callbacks.
	records, err := memstore.NewStore(memstore.StoreConfig{})
	must(err, "record store")
	usage, err := memstore.NewUsageStore(memstore.UsageStoreConfig[struct{}]{Records: records})
	must(err, "usage store")

	signer, err := limes.NewSigner(limes.SignerConfig{KeyID: kid, Key: priv})
	must(err, "signer")

	issuer, err := limes.NewIssuer(limes.IssuerConfig{
		Signer: signer, Store: records, IssuerName: "example-issuer",
	})
	must(err, "issuer")

	keys, err := jwks.NewStatic(jwks.StaticConfig{Keys: map[string]ed25519.PublicKey{kid: pub}})
	must(err, "key set")
	verifier, err := limes.NewVerifier(limes.VerifierConfig{
		Keys:           keys,
		Revocations:    records,
		TrustedIssuers: []string{"example-issuer"},
	})
	must(err, "verifier")

	// 3. Issue to an orchestrator agent. Everything the authority permits is
	//    described by the token itself — no role, no long-lived secret.
	parentResult, err := issuer.Issue(ctx, limes.IssueRequest{
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
			Requests:         limes.RequestCeiling{Max: 500},
			Budget:           limes.BudgetCeiling{Max: limes.MustParseAmount("25.00")},
			UnitCode:         "USD",
		},
	})
	parent, parentToken := parentResult.Capability, parentResult.Token
	must(err, "issue")
	log.Info("issued a capability to the orchestrator agent",
		"capability", parent.ID, "token size", fmt.Sprintf("%d bytes", len(parentToken)))

	// 4. On every call, verify and enforce the caveats in one step. Local — no
	//    round trip to the issuer.
	verified, err := verifier.Authorize(ctx, limes.AuthorizeRequest{
		Verify: limes.VerifyRequest{Token: parentToken, Audience: "my-service"},
		Check:  limes.CheckRequest{Op: limes.OpGet, Resource: "corpus/public/report.pdf"},
	})
	must(err, "authorize")
	log.Info("verified the token and admitted a call the caveats allow", "resource", "corpus/public/report.pdf")
	_, err = verifier.Authorize(ctx, limes.AuthorizeRequest{
		Verify: limes.VerifyRequest{Token: parentToken, Audience: "my-service"},
		Check:  limes.CheckRequest{Op: limes.OpGet, Resource: "corpus/private/report.pdf"},
	})
	demo.Refused(log, err, limes.ErrResourceNotAllowed, "a resource outside the caveats")

	// A token minted for one audience must not verify for another.
	_, err = verifier.Verify(ctx, limes.VerifyRequest{Token: parentToken, Audience: "another-service"})
	demo.Refused(log, err, limes.ErrAudienceMismatch, "the token presented to another service")

	// 5. Delegate to a sub-agent — strictly narrower, without calling back to
	//    any admin API. An orchestrator can attenuate, never escalate.
	childResult, err := issuer.Delegate(ctx, limes.DelegateRequest{
		Parent: verified, // what the verifier accepted, not the issuer's copy
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
			Requests:         limes.RequestCeiling{Max: 50},
			Budget:           limes.BudgetCeiling{Max: limes.MustParseAmount("2.00")}, // 25.00 → 2.00
			UnitCode:         "USD",
		},
	})
	must(err, "delegate")
	child := childResult.Capability
	log.Info("delegated a narrower capability to a sub-agent", "capability", child.ID, "parent", parent.ID)

	// Widening is refused — this is the property that makes the model safe to
	// hand to an agent.
	_, err = issuer.Delegate(ctx, limes.DelegateRequest{
		Parent: verified,
		Subject: limes.Principal{
			Type: limes.PrincipalAgent, TenantID: tenantID, Subject: "greedy-worker",
		},
		Audience: []string{"my-service"},
		TTL:      time.Minute,
		Caveats: limes.Caveats{
			Ops:              []limes.Op{limes.OpGet},
			ResourcePrefixes: []string{"corpus/public/"},
			Requests:         limes.RequestCeiling{Max: 50},
			Budget:           limes.BudgetCeiling{Max: limes.MustParseAmount("999")}, // wider than the parent's 25.00
			UnitCode:         "USD",
		},
	})
	demo.Refused(log, err, limes.ErrDelegationTooWide, "a delegation wider than its parent")

	// 6. Count the request and charge against the budget. The capability's ceiling, each ancestor's
	//    and the tenant's all apply; a rejection by any leaves every counter
	//    untouched, so a retry is always safe.
	bumped, err := usage.Bump(ctx, limes.BumpRequest{Subject: verified.MeterSubject()})
	must(err, "bump")
	log.Info("counted a request", "request", fmt.Sprintf("%d of %s", bumped.RequestCount, verified.Capability().Caveats.Requests))
	receipt, err := usage.Charge(ctx, limes.ChargeRequest{Subject: verified.MeterSubject(), Amount: limes.MustParseAmount("0.35"), Op: "search", Actor: "research-orchestrator"}, nil)
	must(err, "charge")
	log.Info("charged against the budget", "amount", "0.35 USD", "spent so far", receipt.SpentAmount.String()+" USD")

	_, err = usage.Charge(ctx, limes.ChargeRequest{Subject: verified.MeterSubject(), Amount: limes.MustParseAmount("999"), Op: "search", Actor: "research-orchestrator"}, nil)
	demo.Refused(log, err, limes.ErrBudgetExceeded, "a charge over the budget")

	// 7. Revoke, cascading to everything the orchestrator delegated.
	must(records.Revoke(ctx, limes.RevokeRequest{
		CapabilityID: parent.ID, Note: "example finished", Actor: "operator", CascadeChildren: true,
	}), "revoke")

	// The child stops either way — IsRevoked walks the chain — but only the
	// cascade writes a revocation entry of its own, naming it in the audit.
	rec, err := records.GetRecord(ctx, child.ID)
	must(err, "get-record")
	if rec.Revocation == nil {
		must(errCascadeMissed, "cascade the revocation")
	}
	log.Info("revoked the orchestrator's capability, cascading to the sub-agent's", "parent", parent.ID, "child", child.ID)

	_, err = verifier.Verify(ctx, limes.VerifyRequest{Token: parentToken, Audience: "my-service"})
	demo.Refused(log, err, limes.ErrRevoked, "the revoked token")
	log.Info("done: every step behaved as described")
}

// errCascadeMissed is a revocation that did not reach the delegated child.
var errCascadeMissed = errors.New("the delegated child carries no revocation of its own")
