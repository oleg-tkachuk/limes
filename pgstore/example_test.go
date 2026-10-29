package pgstore_test

// The examples need a PostgreSQL server, so they are compiled but not run:
// none has an Output comment. The package's own tests run every call they
// show against a database in a container.

import (
	"context"
	"errors"
	"log"
	"os"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/meter"
	"github.com/oleg-tkachuk/limes/pgstore"
)

// runtimeDSNEnv is the environment variable the examples read the
// connection string of the role the stores serve as from.
const runtimeDSNEnv = "LIMES_DSN"

// runtimePool is a pool of the role the stores serve as. A service keeps it
// for as long as it runs, so the examples do not close it.
func runtimePool(ctx context.Context) *pgxpool.Pool {
	pool, err := pgxpool.New(ctx, os.Getenv(runtimeDSNEnv))
	if err != nil {
		log.Fatal(err)
	}
	return pool
}

func ExampleCheckSchema() {
	ctx := context.Background()
	// A readiness check, from the runtime role, once pgstore/migrate has
	// created the schema.
	pool := runtimePool(ctx)
	if err := pgstore.CheckSchema(ctx, pool); err != nil {
		version, _ := pgstore.ReadSchemaVersion(ctx, pool)
		log.Fatalf("schema %d, want %d: %v", version.Applied, version.Expected, err)
	}
}

func ExampleNewStore() {
	ctx := context.Background()
	pool := runtimePool(ctx)
	records, err := pgstore.NewStore(pgstore.Config{DB: pool})
	if err != nil {
		log.Fatal(err)
	}

	// Every call for one tenant is scoped to it; row-level security keeps
	// every other tenant's rows out of reach.
	tenant := uuid.New()
	now := time.Now()
	err = records.Insert(pgstore.WithTenant(ctx, tenant), limes.InsertRequest{
		Capability: limes.Capability{
			ID: uuid.NewV7(), IssuedAt: now, ExpiresAt: now.Add(15 * time.Minute),
			Subject: limes.Principal{Type: limes.PrincipalTypeService, TenantID: tenant, Subject: "svc:reports"},
			Caveats: limes.Caveats{Ops: []limes.Op{limes.OpGet}},
		},
		IssuedBy: limes.Principal{Subject: "operator@example.com"},
	})
	if err != nil {
		log.Fatal(err)
	}

	// A call scoped to another tenant than its request names is wiring
	// gone wrong, refused rather than answered as "nothing there".
	_, err = records.ListByPrincipal(pgstore.WithTenant(ctx, uuid.New()), limes.ListByPrincipalRequest{
		TenantID: tenant, PrincipalType: limes.PrincipalTypeService, Subject: "svc:reports",
	})
	if !errors.Is(err, limes.ErrScopeMismatch) {
		log.Fatal(err)
	}
}

// Housekeeping runs across tenants: WithAllTenants for the batches that
// write — expired records, stale holds, orphaned counters and the charges
// past their retention — WithAllTenantsReadOnly for a report that only reads.
func ExampleWithAllTenants() {
	ctx := context.Background()
	pool := runtimePool(ctx)
	records, err := pgstore.NewStore(pgstore.Config{DB: pool})
	if err != nil {
		log.Fatal(err)
	}
	usage, err := pgstore.NewUsageStore(pgstore.Config{DB: pool})
	if err != nil {
		log.Fatal(err)
	}

	all := pgstore.WithAllTenants(ctx)
	// Each batch until one comes back short of the limit.
	for {
		purged, err := records.Purge(all, limes.PurgeRequest{})
		if err != nil {
			log.Fatal(err)
		}
		if purged.Purged < limes.DefaultHousekeepingLimit {
			break
		}
	}
	if _, err := usage.ReleaseExpired(all, meter.ReleaseExpiredRequest{}); err != nil {
		log.Fatal(err)
	}
	if _, err := usage.PurgeOrphans(all, meter.PurgeOrphansRequest{}); err != nil {
		log.Fatal(err)
	}
	// The ledger keeps every charge until it is past its retention
	// (meter.MinChargeRetention unless OlderThan says longer).
	ledger, err := pgstore.NewLedger(pgstore.Config{DB: pool})
	if err != nil {
		log.Fatal(err)
	}
	for {
		purged, err := ledger.PurgeCharges(all, meter.PurgeChargesRequest{})
		if err != nil {
			log.Fatal(err)
		}
		if purged.Purged < limes.DefaultHousekeepingLimit {
			break
		}
	}
}

func ExampleWithAllTenantsReadOnly() {
	ctx := context.Background()
	pool := runtimePool(ctx)
	usage, err := pgstore.NewUsageStore(pgstore.Config{DB: pool})
	if err != nil {
		log.Fatal(err)
	}
	// The tenants at or past 90% of their ceiling; the scope admits reads only.
	const alertBasisPoints = 9_000
	for budget, err := range meter.AllTenantBudgets(pgstore.WithAllTenantsReadOnly(ctx), usage, meter.ListTenantBudgetsRequest{
		Filter: meter.TenantBudgetFilterAtOrPast, ThresholdBasisPoints: alertBasisPoints,
	}) {
		if err != nil {
			log.Fatal(err)
		}
		log.Println(budget.TenantID, budget.SpentAmount, budget.Ceiling)
	}
}

// A charge and a write of the consumer's own commit together, or neither
// does: onCharged runs inside the charge's transaction, which TxFrom reads.
func ExampleTxFrom() {
	ctx := context.Background()
	pool := runtimePool(ctx)
	records, err := pgstore.NewStore(pgstore.Config{DB: pool})
	if err != nil {
		log.Fatal(err)
	}
	usage, err := pgstore.NewUsageStore(pgstore.Config{DB: pool})
	if err != nil {
		log.Fatal(err)
	}
	// The tenant, and a capability of it on record.
	tenant, capabilityID := uuid.New(), uuid.New()
	scoped := pgstore.WithTenant(ctx, tenant)
	subject, err := limes.StoredMeterSubject(scoped, records, capabilityID)
	if err != nil {
		log.Fatal(err)
	}

	_, err = usage.Charge(scoped, meter.ChargeRequest{
		Subject: subject, Amount: limes.MustParseAmount("0.35"), Op: "index:search",
	}, func(ctx context.Context, c meter.Charged) error {
		tx, ok := pgstore.TxFrom(ctx)
		if !ok {
			return errors.New("no charge transaction")
		}
		// The transaction is the store's to end: no Commit or Rollback here.
		_, err := tx.Exec(ctx, `INSERT INTO search_log (charge_id) VALUES ($1)`, c.Receipt.ChargeID)
		return err
	})
	if err != nil {
		log.Fatal(err)
	}
}

// A watcher beside each verifier clears its revocation cache on every
// revocation the schema notifies, so a revocation reaches every replica at
// once rather than within the cache's TTL.
func ExampleRevocationWatcher_Run() {
	ctx := context.Background()
	pool := runtimePool(ctx)
	records, err := pgstore.NewStore(pgstore.Config{DB: pool})
	if err != nil {
		log.Fatal(err)
	}
	cache, err := limes.NewRevocationCache(limes.RevocationCacheConfig{Capabilities: records, Biscuits: records})
	if err != nil {
		log.Fatal(err)
	}

	watcher := pgstore.RevocationWatcher{
		// A connection of its own: LISTEN holds it for as long as it runs.
		Connect: func(ctx context.Context) (*pgx.Conn, error) {
			return pgx.Connect(ctx, os.Getenv(runtimeDSNEnv))
		},
		Caches:  []pgstore.Clearer{cache},
		OnError: func(err error) { log.Println("revocation watcher:", err) },
	}
	go func() {
		if err := watcher.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Println(err)
		}
	}()
}

// EraseTenant removes a tenant's records and meter rows, and the consumer's
// own rows of it, in one transaction.
func ExampleStore_EraseTenant() {
	ctx := context.Background()
	pool := runtimePool(ctx)
	records, err := pgstore.NewStore(pgstore.Config{DB: pool})
	if err != nil {
		log.Fatal(err)
	}
	tenant := uuid.New()
	result, err := records.EraseTenant(pgstore.WithTenant(ctx, tenant), limes.EraseTenantRequest{
		TenantID: tenant, Note: "tenant closed", Actor: "operator@example.com",
	}, func(ctx context.Context, e pgstore.Erased) error {
		_, err := e.Tx.Exec(ctx, `DELETE FROM documents WHERE tenant_id = $1`, tenant)
		return err
	})
	if err != nil {
		log.Fatal(err)
	}
	// The records a verifier may still need stay, revoked, until Purge.
	log.Println(result.Revoked, result.Removed, result.Kept)
}

func ExampleNewLedger() {
	ctx := context.Background()
	pool := runtimePool(ctx)
	ledger, err := pgstore.NewLedger(pgstore.Config{DB: pool})
	if err != nil {
		log.Fatal(err)
	}
	tenant := uuid.New()
	monthStart := meter.ChargeBucketMonth.Start(time.Now())
	page, err := ledger.SumCharges(pgstore.WithTenant(ctx, tenant), meter.SumChargesRequest{
		TenantID: tenant, From: monthStart, To: monthStart.AddDate(0, 1, 0), Bucket: meter.ChargeBucketDay,
	})
	if err != nil {
		log.Fatal(err)
	}
	for _, sum := range page.Items {
		log.Println(sum.BucketStart.Format(time.DateOnly), sum.Charged, sum.UnitCode)
	}
}

func ExampleNewCensus() {
	ctx := context.Background()
	pool := runtimePool(ctx)
	census, err := pgstore.NewCensus(pgstore.Config{DB: pool})
	if err != nil {
		log.Fatal(err)
	}
	// Every tenant's records, by tenant, for a platform operator.
	page, err := census.CountRecords(pgstore.WithAllTenantsReadOnly(ctx), limes.CountRecordsRequest{
		AllTenants: true, ByTenant: true, ExpiringWithin: 24 * time.Hour,
	})
	if err != nil {
		log.Fatal(err)
	}
	for _, n := range page.Items {
		log.Println(n.TenantID, n.Active, n.Revoked, n.ExpiringSoon)
	}
}

// requestTenant is the context key a consumer's own middleware keeps the
// request's tenant under.
type requestTenant struct{}

// A consumer that scopes transactions itself — from a tenant its own
// middleware put in the context — sets Scope to write the settings the
// schema's policies read, and CheckScope to CheckSchemaScope, which checks a
// call's tenant against those settings.
func ExampleCheckSchemaScope() {
	ctx := context.Background()
	pool := runtimePool(ctx)
	records, err := pgstore.NewStore(pgstore.Config{
		DB: pool,
		Scope: func(ctx context.Context, tx pgx.Tx) error {
			tenant, ok := ctx.Value(requestTenant{}).(uuid.UUID)
			if !ok {
				return errors.New("no tenant on the request")
			}
			// Both settings, every time, local to the transaction: the one
			// not used set to nothing, so no default can widen the scope.
			_, err := tx.Exec(ctx, `SELECT set_config($1, $2, true), set_config($3, '', true)`,
				pgstore.SettingTenant, tenant.String(), pgstore.SettingAllTenants)
			return err
		},
		CheckScope: pgstore.CheckSchemaScope,
	})
	if err != nil {
		log.Fatal(err)
	}
	tenant := uuid.New()
	if _, err := records.ListByPrincipal(context.WithValue(ctx, requestTenant{}, tenant), limes.ListByPrincipalRequest{
		TenantID: tenant, PrincipalType: limes.PrincipalTypeService, Subject: "svc:reports",
	}); err != nil {
		log.Fatal(err)
	}
}
