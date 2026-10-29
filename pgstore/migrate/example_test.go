package migrate_test

// The examples need a PostgreSQL server, so they are compiled but not run:
// none has an Output comment. The package's own tests run every call they
// show against a database in a container.

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/oleg-tkachuk/limes/pgstore/migrate"
)

// ownerDSNEnv is the environment variable the examples read the connection
// string of the role that owns the schema from.
const ownerDSNEnv = "LIMES_OWNER_DSN"

// runtimeRole is the role the stores serve as, which the examples grant.
const runtimeRole = "app"

func ExampleRun() {
	ctx := context.Background()
	// The schema is created by the role that owns it, never by the role
	// the stores serve as: that one must own nothing, or row-level
	// security would not bind it.
	owner, err := pgxpool.New(ctx, os.Getenv(ownerDSNEnv))
	if err != nil {
		log.Fatal(err)
	}
	err = owner.AcquireFunc(ctx, func(c *pgxpool.Conn) error {
		return migrate.Run(ctx, c.Conn(), migrate.Options{RuntimeRoles: []string{runtimeRole}})
	})
	owner.Close()
	if err != nil {
		log.Fatal(err)
	}
}

// A consumer with a migration tool of its own gives it the steps rather
// than run tern beside it: Setup once, then each step's SQL in a
// transaction of its own, or a statement at a time outside one where
// InTransaction is false, then Grants.
func ExampleSteps() {
	steps, err := migrate.Steps()
	if err != nil {
		log.Fatal(err)
	}
	for _, s := range steps[concurrentFrom-1 : concurrentFrom+1] {
		fmt.Println(s.Version, s.Name, s.InTransaction)
	}
	// Output:
	// 14 014_record_listing.sql false
	// 15 015_revoked_flag.sql true
}

// concurrentFrom is the first step that builds its indexes CONCURRENTLY.
const concurrentFrom = 14
