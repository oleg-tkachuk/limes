// Command metering counts an agent's requests and charges its spend against
// the ceilings of its capability and its tenant: a direct charge, a hold
// settled at the actual cost, and the refusals once a ceiling is reached.
//
//	go run ./metering
package main

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/examples/internal/demo"
	"github.com/oleg-tkachuk/limes/jwks"
	"github.com/oleg-tkachuk/limes/memstore"
)

const (
	issuerName = "example-issuer"
	audience   = "llm-gateway"
	unit       = "USD"
	// requestLimit is how many requests the capability admits.
	requestLimit = 3
	// holdTTL is how long a reservation holds its amount unless settled.
	holdTTL = time.Minute
)

// The costs the example meters: a search charged directly, and a completion
// held at an estimate and settled at its actual cost.
var (
	searchCost         = limes.MustParseAmount("0.35")
	completionEstimate = limes.MustParseAmount("0.50")
	completionCost     = limes.MustParseAmount("0.42")
)

// money is an amount or a budget in the examples' currency.
func money(v fmt.Stringer) string { return v.String() + " " + unit }

func main() {
	ctx := context.Background()
	log := demo.New("metering")

	key, err := limes.GenerateKey()
	demo.Must(log, err, "generate a signing key")
	records, err := memstore.NewStore(memstore.StoreConfig{})
	demo.Must(log, err, "records store")
	// UsageStore is generic in the transaction a charge runs its callback
	// in; the in-memory one has none, so struct{} and nil callbacks.
	usage, err := memstore.NewUsageStore(memstore.UsageStoreConfig[struct{}]{Records: records})
	demo.Must(log, err, "usage store")
	signer, err := limes.NewSigner(limes.SignerConfig{KeyID: key.KeyID, Key: key.Private})
	demo.Must(log, err, "signer")
	issuer, err := limes.NewIssuer(limes.IssuerConfig{Signer: signer, Store: records, IssuerName: issuerName})
	demo.Must(log, err, "issuer")
	keys, err := jwks.NewStatic(jwks.StaticConfig{Keys: map[string]ed25519.PublicKey{key.KeyID: key.Public()}})
	demo.Must(log, err, "key set")
	verifier, err := limes.NewVerifier(limes.VerifierConfig{Keys: keys, Revocations: records, TrustedIssuers: []string{issuerName}})
	demo.Must(log, err, "verifier")

	tenant := uuid.New()
	tenantBudget, err := usage.SetTenantBudget(ctx, limes.SetTenantBudgetRequest{
		TenantID: tenant, Ceiling: limes.BudgetCeiling{Max: limes.MustParseAmount("100.00")}, UnitCode: unit,
	})
	demo.Must(log, err, "set the tenant's budget")
	log.Info("set the tenant's budget: every capability in the tenant spends from it too",
		"tenant", tenant, "ceiling", money(tenantBudget.Ceiling))

	issued, err := issuer.Issue(ctx, limes.IssueRequest{
		IssuedBy: limes.Principal{Type: limes.PrincipalTypeUser, TenantID: tenant, Subject: "operator@example.com"},
		Subject:  limes.Principal{Type: limes.PrincipalTypeAgent, TenantID: tenant, Subject: "research-agent"},
		Audience: []string{audience},
		TTL:      15 * time.Minute,
		Caveats: limes.Caveats{
			Ops:      []limes.Op{limes.OpGet},
			Requests: limes.RequestCeiling{Max: requestLimit},
			Budget:   limes.BudgetCeiling{Max: limes.MustParseAmount("1.00")},
			UnitCode: unit,
		},
	})
	demo.Must(log, err, "issue")
	c := issued.Capability
	log.Info("issued a capability with its own ceilings",
		"capability", c.ID, "requests", c.Caveats.Requests, "budget", money(c.Caveats.Budget))

	// The meter charges the subject the verifier hands back for the token:
	// the capability with its ceilings, and its tenant.
	verified, err := verifier.Verify(ctx, limes.VerifyRequest{Token: issued.Token, Audience: audience})
	demo.Must(log, err, "verify")
	subject := verified.MeterSubject()

	for range requestLimit {
		r, err := usage.Bump(ctx, limes.BumpRequest{Subject: subject})
		demo.Must(log, err, "count a request")
		log.Info("counted a request against the capability's limit", "request", fmt.Sprintf("%d of %d", r.RequestCount, requestLimit))
	}
	_, err = usage.Bump(ctx, limes.BumpRequest{Subject: subject})
	demo.Refused(log, err, limes.ErrRequestLimitExceeded, "a request past the limit")

	receipt, err := usage.Charge(ctx, limes.ChargeRequest{
		Subject: subject, Amount: searchCost, Op: "search", Actor: "research-agent",
	}, nil)
	demo.Must(log, err, "charge a search")
	log.Info("charged a search directly: its cost is known up front",
		"amount", money(searchCost), "charge", receipt.ChargeID, "spent so far", money(receipt.SpentAmount))

	// A call whose cost is known only afterwards holds an estimate first,
	// then settles at what it cost.
	hold, err := usage.Reserve(ctx, limes.ReserveRequest{
		Subject: subject, Amount: completionEstimate, TTL: holdTTL, Op: "completion", Actor: "research-agent",
	})
	demo.Must(log, err, "reserve an estimate")
	log.Info("held an estimate for a completion whose cost is known only afterwards",
		"reservation", hold.ID, "held", money(hold.Amount), "released unless settled by", hold.ExpiresAt.UTC().Format(time.RFC3339))
	logUsage(ctx, log, usage, c.ID)

	settled, err := usage.Settle(ctx, limes.SettleRequest{
		Subject: subject, ReservationID: hold.ID, Amount: completionCost,
	}, nil)
	demo.Must(log, err, "settle at the actual cost")
	log.Info("settled the hold at what the completion actually cost; the rest of the estimate is freed",
		"actual cost", money(completionCost), "spent so far", money(settled.SpentAmount))
	logUsage(ctx, log, usage, c.ID)

	_, err = usage.Charge(ctx, limes.ChargeRequest{
		Subject: subject, Amount: limes.MustParseAmount("0.50"), Op: "search", Actor: "research-agent",
	}, nil)
	demo.Refused(log, err, limes.ErrBudgetExceeded, "a charge past the capability's budget")
	logUsage(ctx, log, usage, c.ID)

	b, err := usage.GetTenantBudget(ctx, tenant)
	demo.Must(log, err, "read the tenant's budget")
	log.Info("the tenant's budget moved with every charge",
		"spent", money(b.SpentAmount), "held", money(b.ReservedAmount), "ceiling", money(b.Ceiling))
	log.Info("done: every step behaved as described")
}

// logUsage logs what the meter holds for capability id.
func logUsage(ctx context.Context, log *slog.Logger, usage limes.MeterReader, id uuid.UUID) {
	u, err := usage.GetUsage(ctx, id)
	if err != nil {
		log.Info("the meter holds nothing for the capability yet", "capability", id, "error", err)
		return
	}
	log.Info("what the meter holds for the capability now",
		"requests", u.RequestCount, "spent", money(u.SpentAmount), "held", money(u.ReservedAmount))
}
