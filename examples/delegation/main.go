// Command delegation has an orchestrator agent hand a narrower capability to
// a sub-agent, shows that a wider one is refused, and revokes the whole
// chain from its root.
//
//	go run ./delegation
package main

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/examples/internal/demo"
	"github.com/oleg-tkachuk/limes/jwks"
	"github.com/oleg-tkachuk/limes/memstore"
)

const (
	issuerName = "example-issuer"
	audience   = "search-service"
	unit       = "USD"
)

// money is an amount or a budget in the examples' currency.
func money(v fmt.Stringer) string { return v.String() + " " + unit }

func main() {
	ctx := context.Background()
	log := demo.New("delegation")

	key, err := limes.GenerateKey()
	demo.Must(log, err, "generate a signing key")
	records, err := memstore.NewStore(memstore.StoreConfig{})
	demo.Must(log, err, "records store")
	signer, err := limes.NewSigner(limes.SignerConfig{KeyID: key.KeyID, Key: key.Private})
	demo.Must(log, err, "signer")
	issuer, err := limes.NewIssuer(limes.IssuerConfig{Signer: signer, Store: records, IssuerName: issuerName})
	demo.Must(log, err, "issuer")
	keys, err := jwks.NewStatic(jwks.StaticConfig{Keys: map[string]ed25519.PublicKey{key.KeyID: key.Public}})
	demo.Must(log, err, "key set")
	verifier, err := limes.NewVerifier(limes.VerifierConfig{Keys: keys, Revocations: records, TrustedIssuers: []string{issuerName}})
	demo.Must(log, err, "verifier")

	tenant := uuid.New()
	root, err := issuer.Issue(ctx, limes.IssueRequest{
		IssuedBy: limes.Principal{Type: limes.PrincipalUser, TenantID: tenant, Subject: "operator@example.com"},
		Subject:  limes.Principal{Type: limes.PrincipalAgent, TenantID: tenant, Subject: "orchestrator"},
		Audience: []string{audience},
		TTL:      15 * time.Minute,
		Caveats: limes.Caveats{
			Ops:              []limes.Op{limes.OpGet, limes.OpShare},
			ResourcePrefixes: []string{"corpus/public/"},
			Requests:         limes.RequestCeiling{Max: 500},
			Budget:           limes.BudgetCeiling{Max: limes.MustParseAmount("25.00")},
			UnitCode:         unit,
		},
	})
	demo.Must(log, err, "issue to the orchestrator")
	log.Info("issued the orchestrator its capability",
		"capability", root.Capability.ID, "operations", root.Capability.Caveats.Ops,
		"resources under", root.Capability.Caveats.ResourcePrefixes,
		"requests", root.Capability.Caveats.Requests, "budget", money(root.Capability.Caveats.Budget))

	// A delegation starts from what the verifier accepted: the orchestrator
	// presents its own token, as it would to any service.
	parent, err := verifier.Verify(ctx, limes.VerifyRequest{Token: root.Token, Audience: audience})
	demo.Must(log, err, "verify the orchestrator's token")

	child, err := issuer.Delegate(ctx, limes.DelegateRequest{
		Parent:   parent,
		Subject:  limes.Principal{Type: limes.PrincipalAgent, TenantID: tenant, Subject: "sub-agent"},
		Audience: []string{audience},
		TTL:      2 * time.Minute,
		Caveats: limes.Caveats{
			Ops:              []limes.Op{limes.OpGet},
			ResourcePrefixes: []string{"corpus/public/2026/"},
			Requests:         limes.RequestCeiling{Max: 50},
			Budget:           limes.BudgetCeiling{Max: limes.MustParseAmount("2.00")},
			UnitCode:         unit,
		},
	})
	demo.Must(log, err, "delegate to the sub-agent")
	log.Info("the orchestrator delegated a narrower capability to a sub-agent, with no call to an admin",
		"capability", child.Capability.ID, "parent", child.Capability.ParentID,
		"operations", child.Capability.Caveats.Ops, "resources under", child.Capability.Caveats.ResourcePrefixes,
		"requests", child.Capability.Caveats.Requests, "budget", money(child.Capability.Caveats.Budget),
		"expires", child.Capability.ExpiresAt.Format(time.RFC3339))

	_, err = verifier.Authorize(ctx, limes.AuthorizeRequest{
		Verify: limes.VerifyRequest{Token: child.Token, Audience: audience},
		Check:  limes.CheckRequest{Op: limes.OpGet, Resource: "corpus/public/2026/q3.pdf"},
	})
	demo.Must(log, err, "the sub-agent reads inside its prefix")
	log.Info("verifier admitted the sub-agent's request inside its own prefix", "resource", "corpus/public/2026/q3.pdf")

	_, err = verifier.Authorize(ctx, limes.AuthorizeRequest{
		Verify: limes.VerifyRequest{Token: child.Token, Audience: audience},
		Check:  limes.CheckRequest{Op: limes.OpGet, Resource: "corpus/public/2025/q3.pdf"},
	})
	demo.Refused(log, err, limes.ErrResourceNotAllowed, "the sub-agent reads under its parent's prefix, not its own")

	_, err = issuer.Delegate(ctx, limes.DelegateRequest{
		Parent:   parent,
		Subject:  limes.Principal{Type: limes.PrincipalAgent, TenantID: tenant, Subject: "greedy-agent"},
		Audience: []string{audience},
		TTL:      time.Minute,
		Caveats: limes.Caveats{
			Ops:              []limes.Op{limes.OpGet},
			ResourcePrefixes: []string{"corpus/public/"},
			Requests:         limes.RequestCeiling{Max: 50},
			Budget:           limes.BudgetCeiling{Max: limes.MustParseAmount("999.00")},
			UnitCode:         unit,
		},
	})
	demo.Refused(log, err, limes.ErrDelegationTooWide, "delegate a budget wider than the parent's")

	demo.Must(log, records.Revoke(ctx, limes.RevokeRequest{
		CapabilityID: root.Capability.ID, Note: "run finished", Actor: "operator", CascadeChildren: true,
	}), "revoke the orchestrator")
	log.Info("revoked the orchestrator's capability, and with it everything it delegated", "capability", root.Capability.ID)

	_, err = verifier.Verify(ctx, limes.VerifyRequest{Token: child.Token, Audience: audience})
	demo.Refused(log, err, limes.ErrRevoked, "the sub-agent's token after its parent's revocation")
	log.Info("done: every step behaved as described")
}
