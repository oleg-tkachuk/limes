// Command issue-verify issues a capability token to an agent, checks it the
// way a service checks every request, and revokes it.
//
//	go run ./issue-verify
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
)

func main() {
	ctx := context.Background()
	log := demo.New("issue-verify")

	key, err := limes.GenerateKey()
	demo.Must(log, err, "generate a signing key")
	log.Info("generated an Ed25519 signing key", "key id", key.KeyID)

	// The records store keeps every capability and its revocation; in
	// memory here, PostgreSQL in the pgstore example.
	records, err := memstore.NewStore(memstore.StoreConfig{})
	demo.Must(log, err, "records store")
	signer, err := limes.NewSigner(limes.SignerConfig{KeyID: key.KeyID, Key: key.Private})
	demo.Must(log, err, "signer")
	issuer, err := limes.NewIssuer(limes.IssuerConfig{Signer: signer, Store: records, IssuerName: issuerName})
	demo.Must(log, err, "issuer")

	// The verifier needs only the public key and the revocations: it checks
	// a token locally, with no call to the issuer.
	keys, err := jwks.NewStatic(jwks.StaticConfig{Keys: map[string]ed25519.PublicKey{key.KeyID: key.Public}})
	demo.Must(log, err, "key set")
	verifier, err := limes.NewVerifier(limes.VerifierConfig{Keys: keys, Revocations: records, TrustedIssuers: []string{issuerName}})
	demo.Must(log, err, "verifier")
	log.Info("issuer signs tokens; verifier checks them with the public key alone, no call back to the issuer",
		"issuer", issuerName, "audience", audience)

	tenant := uuid.New()
	issued, err := issuer.Issue(ctx, limes.IssueRequest{
		IssuedBy: limes.Principal{Type: limes.PrincipalUser, TenantID: tenant, Subject: "operator@example.com"},
		Subject:  limes.Principal{Type: limes.PrincipalAgent, TenantID: tenant, Subject: "research-agent"},
		Audience: []string{audience},
		TTL:      15 * time.Minute,
		Caveats: limes.Caveats{
			Ops:              []limes.Op{limes.OpGet},
			ResourcePrefixes: []string{"corpus/public/"},
			Requests:         limes.RequestCeiling{Unlimited: true},
			Budget:           limes.BudgetCeiling{Unlimited: true},
		},
	})
	demo.Must(log, err, "issue")
	c := issued.Capability
	log.Info("issued a capability token to the agent: what it may do travels in the token",
		"capability", c.ID, "agent", c.Subject.Subject, "operations", c.Caveats.Ops,
		"resources under", c.Caveats.ResourcePrefixes, "expires", c.ExpiresAt.Format(time.RFC3339),
		"token size", fmt.Sprintf("%d bytes", len(issued.Token)))

	verified, err := verifier.Authorize(ctx, limes.AuthorizeRequest{
		Verify: limes.VerifyRequest{Token: issued.Token, Audience: audience},
		Check:  limes.CheckRequest{Op: limes.OpGet, Resource: "corpus/public/report.pdf"},
	})
	demo.Must(log, err, "authorize a read inside the caveats")
	log.Info("verifier admitted a request the caveats allow",
		"operation", limes.OpGet, "resource", "corpus/public/report.pdf", "capability", verified.Capability().ID)

	_, err = verifier.Authorize(ctx, limes.AuthorizeRequest{
		Verify: limes.VerifyRequest{Token: issued.Token, Audience: audience},
		Check:  limes.CheckRequest{Op: limes.OpGet, Resource: "corpus/private/salaries.xlsx"},
	})
	demo.Refused(log, err, limes.ErrResourceNotAllowed, "read a resource outside the caveats")

	_, err = verifier.Authorize(ctx, limes.AuthorizeRequest{
		Verify: limes.VerifyRequest{Token: issued.Token, Audience: audience},
		Check:  limes.CheckRequest{Op: limes.OpShare, Resource: "corpus/public/report.pdf"},
	})
	demo.Refused(log, err, limes.ErrOpNotAllowed, "share, an op the caveats do not grant")

	_, err = verifier.Verify(ctx, limes.VerifyRequest{Token: issued.Token, Audience: "billing-service"})
	demo.Refused(log, err, limes.ErrAudienceMismatch, "present the token to another service")

	demo.Must(log, records.Revoke(ctx, limes.RevokeRequest{CapabilityID: c.ID, Note: "agent finished", Actor: "operator"}), "revoke")
	log.Info("revoked the capability in the store", "capability", c.ID)

	_, err = verifier.Verify(ctx, limes.VerifyRequest{Token: issued.Token, Audience: audience})
	demo.Refused(log, err, limes.ErrRevoked, "verify the revoked token")
	log.Info("done: every step behaved as described")
}
