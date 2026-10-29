package pgstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/pgstore/internal/pgtest"
	"github.com/oleg-tkachuk/limes/pgstore/migrate"
)

// The database every test of the package runs against: one PostgreSQL
// container (pgtest), migrated once, its tables emptied before each check.
// Its owner, a superuser which row-level security never binds, sets each
// check's rows up and empties the tables.
const (
	// migratorUser owns the schema, as a deployment's migration role does:
	// no superuser, so the policies bind it (010_force_row_security.sql).
	migratorUser     = "limes_migrator"
	migratorPassword = "limes-migrator-test"
	// appUser is the role the store under test connects as: it owns no
	// table, so row-level security binds it as it binds a deployed service.
	appUser     = "limes_app"
	appPassword = "limes-app-test"
	// appPoolConns is the pool the store under test gets: small, so a call
	// that takes a second connection while it holds one deadlocks here
	// (meter.Meter's contract).
	appPoolConns = 2
)

// createApp creates appUser, which migrate.Run grants as a runtime role.
const createApp = `CREATE ROLE ` + appUser + ` LOGIN PASSWORD '` + appPassword + `'`

// createMigrator creates migratorUser, free to create the store's schema in
// database db.
func createMigrator(db string) string {
	return `CREATE ROLE ` + migratorUser + ` LOGIN PASSWORD '` + migratorPassword + `';
GRANT CREATE ON DATABASE ` + pgx.Identifier{db}.Sanitize() + ` TO ` + migratorUser
}

// The tenants a consumer keeps, as the tests stand one in: a table of their
// own, read by Config.CheckTenant.
const (
	createTenants = `
CREATE TABLE public.tenants (id uuid PRIMARY KEY, deleted boolean NOT NULL DEFAULT false);
GRANT SELECT ON public.tenants TO ` + appUser
	sqlNewTenant    = `INSERT INTO public.tenants (id) VALUES ($1)`
	sqlDeleteTenant = `UPDATE public.tenants SET deleted = true WHERE id = $1`
	sqlTenantStatus = `SELECT deleted FROM public.tenants WHERE id = $1`
)

// newTenant records a tenant the tests' CheckTenant knows.
func newTenant(t *testing.T) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := owner.Exec(t.Context(), sqlNewTenant, id); err != nil {
		t.Fatalf("new tenant: %v", err)
	}
	return id
}

// checkTenant is the tests' Config.CheckTenant over public.tenants.
func checkTenant(ctx context.Context, tx pgx.Tx, tenant uuid.UUID) error {
	var deleted bool
	err := tx.QueryRow(ctx, sqlTenantStatus, tenant).Scan(&deleted)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return limes.ErrUnknownTenant
	case err != nil:
		return err
	case deleted:
		return limes.ErrTenantDeleted
	}
	return nil
}

// emptyTables empties every table of the store, as the owner.
const emptyTables = `TRUNCATE public.tenants, ` + schema + `.capabilities, ` + schema + `.usage, ` + schema + `.copy_usage, ` + schema + `.tenant_budgets, ` + schema + `.charges, ` + schema + `.reservations, ` + schema + `.erased_tenants CASCADE`

var (
	// pg is the container every test runs against.
	pg *pgtest.Postgres
	// owner is a pool as the owner role; app as appUser.
	owner, app *pgxpool.Pool
	// ownerDSN reaches the container's database as the owner.
	ownerDSN string
	// migrator is a pool as migratorUser, the schema's owner.
	migrator *pgxpool.Pool
)

func TestMain(m *testing.M) {
	code, err := run(m)
	if err != nil {
		fmt.Fprintln(os.Stderr, "pgstore tests need a running Docker for their PostgreSQL:", err)
		os.Exit(1)
	}
	os.Exit(code)
}

func run(m *testing.M) (int, error) {
	ctx := context.Background()
	var err error
	pg, err = pgtest.Start(ctx)
	defer pg.Terminate()
	if err != nil {
		return 0, err
	}
	dsn := pg.DSN
	ownerDSN = dsn
	if owner, err = pgxpool.New(ctx, dsn); err != nil {
		return 0, err
	}
	defer owner.Close()
	if _, err := owner.Exec(ctx, createApp); err != nil {
		return 0, err
	}
	if _, err := owner.Exec(ctx, createMigrator(pg.Database)); err != nil {
		return 0, err
	}
	if migrator, err = poolAs(ctx, dsn, migratorUser, migratorPassword); err != nil {
		return 0, err
	}
	defer migrator.Close()
	err = migrator.AcquireFunc(ctx, func(c *pgxpool.Conn) error {
		return migrate.Run(ctx, c.Conn(), migrate.Options{RuntimeRoles: []string{appUser}})
	})
	if err != nil {
		return 0, err
	}
	if _, err := owner.Exec(ctx, createTenants); err != nil {
		return 0, err
	}
	if app, err = poolAs(ctx, dsn, appUser, appPassword); err != nil {
		return 0, err
	}
	defer app.Close()
	return m.Run(), nil
}

// poolAs is a pool of appPoolConns connections to dsn's database as user.
func poolAs(ctx context.Context, dsn, user, password string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	cfg.ConnConfig.User, cfg.ConnConfig.Password = user, password
	cfg.MaxConns = appPoolConns
	return pgxpool.NewWithConfig(ctx, cfg)
}

// sqlSetSession sets a setting for the rest of the session.
const sqlSetSession = `SELECT set_config($1, $2, false)`

// fresh empties the tables for a check of its own.
func fresh(t *testing.T) {
	t.Helper()
	if _, err := owner.Exec(t.Context(), emptyTables); err != nil {
		t.Fatalf("empty the tables: %v", err)
	}
}

// fakeClock stands still but for Advance.
type fakeClock struct {
	start  time.Time
	offset atomic.Int64
}

func newFakeClock() *fakeClock {
	return &fakeClock{start: time.Now().UTC().Truncate(timeResolution)}
}

func (c *fakeClock) Now() time.Time { return c.start.Add(time.Duration(c.offset.Load())) }

func (c *fakeClock) Advance(d time.Duration) { c.offset.Add(int64(d)) }

// countedScope is ScopeFromContext, counting its calls, so a test can tell
// the store called its Scope in the transaction it opened.
func countedScope(calls *atomic.Int64) func(context.Context, pgx.Tx) error {
	return func(ctx context.Context, tx pgx.Tx) error {
		calls.Add(1)
		return ScopeFromContext(ctx, tx)
	}
}

// consumerReadOnlyKey is a consumer's own marker of a call that reads every
// tenant, which the store never sees: consumerScope maps it to the schema's
// read-only setting.
type consumerReadOnlyKey struct{}

// consumerReadOnly marks ctx as the consumer's read across every tenant.
func consumerReadOnly(ctx context.Context) context.Context {
	return context.WithValue(ctx, consumerReadOnlyKey{}, true)
}

// consumerScope is a consumer's Config.Scope: its own read-only marker sets
// the schema's read-only setting, and WithTenant and WithAllTenants set
// theirs as ScopeFromContext does.
func consumerScope(ctx context.Context, tx pgx.Tx) error {
	if ctx.Value(consumerReadOnlyKey{}) != nil {
		_, err := tx.Exec(ctx, sqlSetLocal, SettingAllTenants, SettingAllTenantsRead)
		return err
	}
	return ScopeFromContext(ctx, tx)
}
