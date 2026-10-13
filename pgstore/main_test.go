package pgstore

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

// The database every test of the package runs against: one PostgreSQL
// container, migrated once, its tables emptied before each check.
const (
	// postgresImage is the PostgreSQL the store is tested on.
	postgresImage = "postgres:18.6-alpine"
	// The container's owner role, which migrates and empties the tables.
	ownerUser     = "limes_owner"
	ownerPassword = "limes-owner-test"
	databaseName  = "limes"
	// appUser is the role the store under test connects as: it owns no
	// table, so row-level security binds it as it binds a deployed service.
	appUser     = "limes_app"
	appPassword = "limes-app-test"
	// appPoolConns is the pool the store under test gets: small, so a call
	// that takes a second connection while it holds one deadlocks here
	// (limes.Meter's contract).
	appPoolConns = 2
)

// grantApp lets appUser use the tables Migrate created, and no more.
const grantApp = `
CREATE ROLE ` + appUser + ` LOGIN PASSWORD '` + appPassword + `';
GRANT USAGE ON SCHEMA ` + schema + ` TO ` + appUser + `;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA ` + schema + ` TO ` + appUser + `;`

// emptyTables empties every table of the store, as the owner.
const emptyTables = `TRUNCATE ` + schema + `.capabilities CASCADE`

var (
	// owner is a pool as the owner role; app as appUser.
	owner, app *pgxpool.Pool
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
	container, err := postgres.Run(ctx, postgresImage,
		postgres.WithDatabase(databaseName),
		postgres.WithUsername(ownerUser),
		postgres.WithPassword(ownerPassword),
		postgres.BasicWaitStrategies(),
	)
	defer func() { _ = testcontainers.TerminateContainer(container) }()
	if err != nil {
		return 0, err
	}
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		return 0, err
	}
	if owner, err = pgxpool.New(ctx, dsn); err != nil {
		return 0, err
	}
	defer owner.Close()
	if err := Migrate(ctx, owner); err != nil {
		return 0, err
	}
	if _, err := owner.Exec(ctx, grantApp); err != nil {
		return 0, err
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return 0, err
	}
	cfg.ConnConfig.User, cfg.ConnConfig.Password = appUser, appPassword
	cfg.MaxConns = appPoolConns
	if app, err = pgxpool.NewWithConfig(ctx, cfg); err != nil {
		return 0, err
	}
	defer app.Close()
	return m.Run(), nil
}

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

// scopeNothing is a Scope that runs a statement, so a test can tell the
// store called it in the transaction it opened.
func scopeNothing(calls *atomic.Int64) func(context.Context, pgx.Tx) error {
	return func(ctx context.Context, tx pgx.Tx) error {
		calls.Add(1)
		_, err := tx.Exec(ctx, "SELECT 1")
		return err
	}
}
