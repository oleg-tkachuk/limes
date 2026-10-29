package migrate

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/oleg-tkachuk/limes/pgstore/internal/pgtest"
)

// freshDatabase is the database each migration check starts from nothing
// in, and runtimeRole the role it grants.
const (
	freshDatabase = "limes_fresh"
	runtimeRole   = "limes_runtime"
	// laterTable stands in for a table a later migration creates.
	laterTable = schema + ".later"
)

var (
	// pg is the container every check runs against.
	pg *pgtest.Postgres
	// owner is a pool as its owner, a superuser; ownerDSN reaches it.
	owner    *pgxpool.Pool
	ownerDSN string
)

func TestMain(m *testing.M) { pgtest.Exit(run(m)) }

func run(m *testing.M) (int, error) {
	ctx := context.Background()
	var err error
	pg, err = pgtest.Start(ctx)
	defer pg.Terminate()
	if err != nil {
		return 0, err
	}
	ownerDSN = pg.DSN
	if owner, err = pgxpool.New(ctx, ownerDSN); err != nil {
		return 0, err
	}
	defer owner.Close()
	return m.Run(), nil
}
