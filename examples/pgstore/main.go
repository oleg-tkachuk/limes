// Command pgstore runs limes over PostgreSQL: it starts a database in a
// container, creates the schema, issues and verifies a capability, charges
// it in the same transaction as a write of its own, shows that another
// tenant sees none of it, and revokes it. It needs a running Docker.
//
//	go run ./pgstore
package main

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tclog "github.com/testcontainers/testcontainers-go/log"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/examples/internal/demo"
	"github.com/oleg-tkachuk/limes/jwks"
	"github.com/oleg-tkachuk/limes/pgstore"
)

const (
	// postgresImage is the PostgreSQL the example runs on.
	postgresImage = "postgres:18.6-alpine"
	databaseName  = "limes"
	// The owner role creates the schema; the app role is what a service
	// connects as. It owns no table, so row-level security binds it.
	ownerUser     = "limes_owner"
	ownerPassword = "limes-owner-example" //nolint:gosec // G101: the password of a container the example starts and removes
	appUser       = "limes_app"
	appPassword   = "limes-app-example" //nolint:gosec // G101: as above

	issuerName = "example-issuer"
	audience   = "search-service"
	unit       = "USD"
)

// money is an amount or a budget in the examples' currency.
func money(v fmt.Stringer) string { return v.String() + " " + unit }

// The example's own table, written in the same transaction as a charge: the
// call is recorded if and only if it is charged.
const (
	createApp   = `CREATE ROLE ` + appUser + ` LOGIN PASSWORD '` + appPassword + `'`
	createCalls = `CREATE TABLE public.calls (charge_id uuid PRIMARY KEY, query text NOT NULL);
GRANT INSERT, SELECT ON public.calls TO ` + appUser
	insertCall  = `INSERT INTO public.calls (charge_id, query) VALUES ($1, $2)`
	countCalls  = `SELECT count(*) FROM public.calls`
	countCaps   = `SELECT count(*) FROM limes.capabilities`
	countCharge = `SELECT count(*) FROM limes.charges`
)

func main() {
	ctx := context.Background()
	log := demo.New("pgstore")

	log.Info("starting PostgreSQL in a container", "image", postgresImage)
	tclog.SetDefault(quiet{})
	container, err := postgres.Run(ctx, postgresImage,
		postgres.WithDatabase(databaseName),
		postgres.WithUsername(ownerUser),
		postgres.WithPassword(ownerPassword),
		postgres.BasicWaitStrategies(),
	)
	defer func() { _ = testcontainers.TerminateContainer(container) }()
	demo.Must(log, err, "start PostgreSQL")
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	demo.Must(log, err, "connection string")

	owner, err := pgxpool.New(ctx, dsn)
	demo.Must(log, err, "connect as the owner")
	defer owner.Close()
	_, err = owner.Exec(ctx, createApp)
	demo.Must(log, err, "create the app role")
	err = owner.AcquireFunc(ctx, func(c *pgxpool.Conn) error {
		return pgstore.Migrate(ctx, c.Conn(), pgstore.MigrateOptions{RuntimeRoles: []string{appUser}})
	})
	demo.Must(log, err, "migrate")
	_, err = owner.Exec(ctx, createCalls)
	demo.Must(log, err, "create the example's table")
	log.Info("created the limes schema as its owner, and granted it to the role the service connects as",
		"owner", ownerUser, "service role", appUser)

	cfg, err := pgxpool.ParseConfig(dsn)
	demo.Must(log, err, "parse the connection string")
	cfg.ConnConfig.User, cfg.ConnConfig.Password = appUser, appPassword
	app, err := pgxpool.NewWithConfig(ctx, cfg)
	demo.Must(log, err, "connect as the app role")
	defer app.Close()

	records, err := pgstore.NewStore(pgstore.Config{DB: app})
	demo.Must(log, err, "records store")
	usage, err := pgstore.NewUsageStore(pgstore.Config{DB: app})
	demo.Must(log, err, "usage store")
	log.Info("built the records and usage stores; the service role owns no table, so row-level security binds it",
		"connected as", appUser)

	key, err := limes.GenerateKey()
	demo.Must(log, err, "generate a signing key")
	signer, err := limes.NewSigner(limes.SignerConfig{KeyID: key.KeyID, Key: key.Private})
	demo.Must(log, err, "signer")
	issuer, err := limes.NewIssuer(limes.IssuerConfig{Signer: signer, Store: records, IssuerName: issuerName})
	demo.Must(log, err, "issuer")
	keys, err := jwks.NewStatic(jwks.StaticConfig{Keys: map[string]ed25519.PublicKey{key.KeyID: key.Public()}})
	demo.Must(log, err, "key set")
	verifier, err := limes.NewVerifier(limes.VerifierConfig{Keys: keys, Revocations: records, TrustedIssuers: []string{issuerName}})
	demo.Must(log, err, "verifier")

	// Every call for a tenant runs scoped to it; row-level security keeps
	// the app role inside that tenant's rows.
	tenant := uuid.New()
	inTenant := pgstore.WithTenant(ctx, tenant)

	_, err = usage.SetTenantBudget(inTenant, limes.SetTenantBudgetRequest{
		TenantID: tenant, Ceiling: limes.BudgetCeiling{Max: limes.MustParseAmount("100.00")}, UnitCode: unit,
	})
	demo.Must(log, err, "set the tenant's budget")

	issued, err := issuer.Issue(inTenant, limes.IssueRequest{
		IssuedBy: limes.Principal{Type: limes.PrincipalTypeUser, TenantID: tenant, Subject: "operator@example.com"},
		Subject:  limes.Principal{Type: limes.PrincipalTypeAgent, TenantID: tenant, Subject: "research-agent"},
		Audience: []string{audience},
		TTL:      15 * time.Minute,
		Caveats: limes.Caveats{
			Ops:      []limes.Op{limes.OpGet},
			Requests: limes.RequestCeiling{Max: 100},
			Budget:   limes.BudgetCeiling{Max: limes.MustParseAmount("5.00")},
			UnitCode: unit,
		},
	})
	demo.Must(log, err, "issue")
	c := issued.Capability
	log.Info("issued a capability; its record is now a row in limes.capabilities",
		"capability", c.ID, "tenant", tenant, "budget", money(c.Caveats.Budget))

	// The verifier reads revocations without a tenant scope: it does so
	// before it knows the token's tenant.
	verified, err := verifier.Verify(ctx, limes.VerifyRequest{Token: issued.Token, Audience: audience})
	demo.Must(log, err, "verify")
	log.Info("verified the token: the signature locally, the revocation status in PostgreSQL",
		"capability", verified.Capability().ID)

	const query = "limes capability tokens"
	receipt, err := usage.Charge(inTenant, limes.ChargeRequest{
		Subject: verified.MeterSubject(), Amount: limes.MustParseAmount("0.35"), Op: "search", Actor: "research-agent",
	}, func(ctx context.Context, charged limes.Charged[pgx.Tx]) error {
		// Inside the charge's transaction: this row commits with the charge,
		// or neither does, and names the charge it records.
		log.Info("inside the charge's transaction: recording the call in the service's own table",
			"query", query, "charge", charged.Receipt.ChargeID)
		return insertChargedCall(ctx, charged.Tx, charged.Receipt.ChargeID, query)
	})
	demo.Must(log, err, "charge")
	log.Info("charged the search; the charge and the call's row committed together",
		"charge", receipt.ChargeID, "spent so far", money(receipt.SpentAmount))

	u, err := usage.GetUsage(inTenant, c.ID)
	demo.Must(log, err, "read usage")
	log.Info("read the capability's usage back, scoped to its tenant", "spent", money(u.SpentAmount))

	elsewhere := pgstore.WithTenant(ctx, uuid.New())
	_, err = records.GetRecord(elsewhere, c.ID)
	demo.Refused(log, err, limes.ErrNotFound, "read the capability scoped to another tenant")
	_, err = usage.GetUsage(elsewhere, c.ID)
	demo.Refused(log, err, limes.ErrUsageNotFound, "read its usage scoped to another tenant")

	demo.Must(log, records.Revoke(inTenant, limes.RevokeRequest{CapabilityID: c.ID, Note: "agent finished", Actor: "operator"}), "revoke")
	log.Info("revoked the capability", "capability", c.ID)
	_, err = verifier.Verify(ctx, limes.VerifyRequest{Token: issued.Token, Audience: audience})
	demo.Refused(log, err, limes.ErrRevoked, "verify the revoked token")

	// As the owner, which row-level security does not bind: what the
	// example left in the database.
	var rows []any
	for _, t := range []struct{ table, sql string }{
		{"limes.capabilities", countCaps}, {"limes.charges", countCharge}, {"public.calls", countCalls},
	} {
		var n int
		demo.Must(log, owner.QueryRow(ctx, t.sql).Scan(&n), "count "+t.table)
		rows = append(rows, t.table, fmt.Sprintf("%d row(s)", n))
	}
	log.Info("what the example left in the database, read as the owner", rows...)
	log.Info("done: every step behaved as described")
}

// quiet drops the container library's own log lines, which would break up
// the example's steps.
type quiet struct{}

func (quiet) Printf(string, ...any) {}

// insertChargedCall records the charged call in the example's own table,
// under the charge's id.
func insertChargedCall(ctx context.Context, tx pgx.Tx, chargeID uuid.UUID, query string) error {
	_, err := tx.Exec(ctx, insertCall, chargeID, query)
	return err
}
