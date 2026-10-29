// Package pgtest starts the PostgreSQL the tests of pgstore and its
// packages run against, one container per test binary, and gives each a
// database of its own where it needs one. Only tests import it.
package pgtest

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	tcexec "github.com/testcontainers/testcontainers-go/exec"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/oleg-tkachuk/limes/pgstore/internal/pgschema"
)

// The container's PostgreSQL, its database and its own role, a superuser,
// which row-level security never binds.
const (
	// Image is the PostgreSQL the store is tested on.
	Image         = "postgres:18.6-alpine"
	OwnerUser     = "limes_owner"
	OwnerPassword = "limes-owner-test"
	Database      = "limes"
)

// Postgres is a running container.
type Postgres struct {
	container *postgres.PostgresContainer
	// DSN reaches Database as OwnerUser.
	DSN string
}

// Start runs the container. Terminate it when the tests end, also when
// Start returns an error: the container may be up.
func Start(ctx context.Context) (*Postgres, error) {
	c, err := postgres.Run(ctx, Image,
		postgres.WithDatabase(Database),
		postgres.WithUsername(OwnerUser),
		postgres.WithPassword(OwnerPassword),
		postgres.BasicWaitStrategies(),
	)
	p := &Postgres{container: c}
	if err != nil {
		return p, err
	}
	p.DSN, err = c.ConnectionString(ctx, "sslmode=disable")
	return p, err
}

// Terminate stops and removes the container.
func (p *Postgres) Terminate() { _ = testcontainers.TerminateContainer(p.container) }

// FreshConn is a connection, as the owner, to a database named name with no
// schema in it, made for the test and dropped when it ends.
func (p *Postgres) FreshConn(t *testing.T, name string) *pgx.Conn {
	t.Helper()
	ctx := t.Context()
	owner, err := pgx.Connect(ctx, p.DSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Close(context.Background()) })
	db := pgx.Identifier{name}.Sanitize()
	for _, stmt := range []string{"DROP DATABASE IF EXISTS " + db, "CREATE DATABASE " + db} {
		if _, err := owner.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := pgx.ParseConfig(p.DSN)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Database = name
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = conn.Close(context.Background())
		_, _ = owner.Exec(context.Background(), "DROP DATABASE IF EXISTS "+db)
	})
	return conn
}

// pgDumpRestrictPrefix starts the lines pg_dump writes around a dump with a
// random key in them (\restrict and \unrestrict), which two dumps of one
// schema never share.
const pgDumpRestrictPrefix = `\restrict`

// pgDumpUnrestrictPrefix is the closing one.
const pgDumpUnrestrictPrefix = `\unrestrict`

// Dump is the store's schema in database as pg_dump writes it, with its
// privileges, without its owners or the random keys of the dump itself.
func (p *Postgres) Dump(ctx context.Context, database string) (string, error) {
	code, out, err := p.container.Exec(ctx, []string{
		"pg_dump", "--schema-only", "--no-owner",
		"--schema=" + pgschema.Name, "--username=" + OwnerUser, "--dbname=" + database,
	}, tcexec.Multiplexed())
	if err != nil {
		return "", err
	}
	raw, err := io.ReadAll(out)
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", fmt.Errorf("pg_dump exited %d: %s", code, raw)
	}
	var b strings.Builder
	for line := range strings.Lines(string(raw)) {
		if strings.HasPrefix(line, pgDumpRestrictPrefix) || strings.HasPrefix(line, pgDumpUnrestrictPrefix) {
			continue
		}
		b.WriteString(line)
	}
	return b.String(), nil
}
