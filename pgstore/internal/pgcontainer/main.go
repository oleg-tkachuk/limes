// Command pgcontainer runs the PostgreSQL that pgstore's tests run against,
// in a container, for as long as its standard input stays open. pgtest
// starts it with `go run`, so testcontainers, and docker through it, are
// required by this module alone and never by pgstore's go.mod.
//
// It writes one line once the database is up, "dsn <connection string>",
// reaching it as its owner, a superuser. Then it answers one request a line
// on its standard input:
//
//	dump <database>
//
// with "ok <n>" and the n bytes pg_dump writes of the store's schema in that
// database, or "err <message>". It ends the container when its input ends.
package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/testcontainers/testcontainers-go"
	tcexec "github.com/testcontainers/testcontainers-go/exec"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

// The container's PostgreSQL, its database and its own role.
const (
	// image is the PostgreSQL the store is tested on.
	image         = "postgres:18.6-alpine"
	ownerUser     = "limes_owner"
	ownerPassword = "limes-owner-test"
	database      = "limes"
	// schema is the schema a dump takes in, the store's.
	schema = "limes"
)

// The lines of the protocol.
const (
	replyDSN     = "dsn"
	requestDump  = "dump"
	replyOK      = "ok"
	replyErr     = "err"
	exitFailure  = 1
	sslmodeParam = "sslmode=disable"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "pgcontainer:", err)
		os.Exit(exitFailure)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	c, err := postgres.Run(ctx, image,
		postgres.WithDatabase(database),
		postgres.WithUsername(ownerUser),
		postgres.WithPassword(ownerPassword),
		postgres.BasicWaitStrategies(),
	)
	defer func() { _ = testcontainers.TerminateContainer(c) }()
	if err != nil {
		return err
	}
	dsn, err := c.ConnectionString(ctx, sslmodeParam)
	if err != nil {
		return err
	}
	out := bufio.NewWriter(os.Stdout)
	fmt.Fprintln(out, replyDSN, dsn)
	if err := out.Flush(); err != nil {
		return err
	}
	return serve(ctx, c, bufio.NewScanner(os.Stdin), out)
}

// serve answers the requests on in until it ends or ctx is done.
func serve(ctx context.Context, c *postgres.PostgresContainer, in *bufio.Scanner, out *bufio.Writer) error {
	for in.Scan() {
		verb, arg, _ := strings.Cut(in.Text(), " ")
		if verb != requestDump || arg == "" {
			fmt.Fprintln(out, replyErr, "unknown request "+strconv.Quote(in.Text()))
		} else if dump, err := pgDump(ctx, c, arg); err != nil {
			fmt.Fprintln(out, replyErr, strings.ReplaceAll(err.Error(), "\n", " "))
		} else {
			fmt.Fprintln(out, replyOK, len(dump))
			_, _ = out.Write(dump)
		}
		if err := out.Flush(); err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return in.Err()
}

// pgDump is the store's schema in db as pg_dump writes it, without owners.
func pgDump(ctx context.Context, c *postgres.PostgresContainer, db string) ([]byte, error) {
	code, r, err := c.Exec(ctx, []string{
		"pg_dump", "--schema-only", "--no-owner",
		"--schema=" + schema, "--username=" + ownerUser, "--dbname=" + db,
	}, tcexec.Multiplexed())
	if err != nil {
		return nil, err
	}
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, fmt.Errorf("pg_dump exited %d: %s", code, raw)
	}
	return raw, nil
}
