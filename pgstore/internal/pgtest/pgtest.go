// Package pgtest gives the tests of pgstore and its packages the PostgreSQL
// they run against, one container per test binary, and a database of its
// own to each test that needs one. Only tests import it.
//
// The container is pgcontainer's, a module of its own beside this package
// that pgtest runs with `go run`: testcontainers is required there, never
// in pgstore's go.mod. It needs a running Docker and the go command.
package pgtest

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
)

// launcherDir is pgcontainer's directory, beside this package's.
const launcherDir = "pgcontainer"

// The lines of pgcontainer's protocol (its package doc).
const (
	replyDSN    = "dsn"
	requestDump = "dump"
	replyOK     = "ok"
	replyErr    = "err"
)

// Postgres is a running container.
type Postgres struct {
	cmd *exec.Cmd
	in  io.WriteCloser
	out *bufio.Reader
	// mu keeps one request at a time on the protocol.
	mu sync.Mutex
	// DSN reaches Database as the container's owner, a superuser.
	DSN string
	// Database is the database DSN names.
	Database string
}

// Start runs the container. Terminate it when the tests end, also when
// Start returns an error: the launcher may be up.
func Start(ctx context.Context) (*Postgres, error) {
	_, self, _, ok := runtime.Caller(0)
	if !ok {
		return &Postgres{}, errors.New("pgtest: cannot find its own source to run pgcontainer from")
	}
	cmd := exec.CommandContext(context.WithoutCancel(ctx), "go", "run", ".")
	cmd.Dir = filepath.Join(filepath.Dir(self), "..", launcherDir)
	cmd.Stderr = os.Stderr
	p := &Postgres{cmd: cmd}
	in, err := cmd.StdinPipe()
	if err != nil {
		return p, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return p, err
	}
	p.in, p.out = in, bufio.NewReader(out)
	if err := cmd.Start(); err != nil {
		return p, fmt.Errorf("pgtest: run pgcontainer: %w", err)
	}
	line, err := p.out.ReadString('\n')
	if err != nil {
		return p, fmt.Errorf("pgtest: pgcontainer gave no connection string: %w", err)
	}
	dsn, ok := strings.CutPrefix(strings.TrimSpace(line), replyDSN+" ")
	if !ok {
		return p, fmt.Errorf("pgtest: pgcontainer said %q, want a connection string", line)
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return p, err
	}
	p.DSN, p.Database = dsn, cfg.Database
	return p, nil
}

// Exit ends a test binary whose TestMain ran its tests over a container:
// with code, or, when err says the container could not be had, with that
// and a failure.
func Exit(code int, err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "the tests need a running Docker for their PostgreSQL:", err)
		os.Exit(exitNoContainer)
	}
	os.Exit(code)
}

// exitNoContainer is a test binary's status when it had no container.
const exitNoContainer = 1

// Terminate ends the launcher, which ends the container.
func (p *Postgres) Terminate() {
	if p.in != nil {
		_ = p.in.Close()
	}
	if p.cmd != nil && p.cmd.Process != nil {
		_ = p.cmd.Wait()
	}
}

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

// pgDumpRestrictPrefix and pgDumpUnrestrictPrefix start the lines pg_dump
// writes around a dump with a random key in them, which two dumps of one
// schema never share.
const (
	pgDumpRestrictPrefix   = `\restrict`
	pgDumpUnrestrictPrefix = `\unrestrict`
)

// Dump is the store's schema in database as pg_dump writes it, with its
// privileges, without its owners or the random keys of the dump itself.
func (p *Postgres) Dump(database string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, err := fmt.Fprintln(p.in, requestDump, database); err != nil {
		return "", err
	}
	raw, err := p.readReply()
	if err != nil {
		return "", fmt.Errorf("pgtest: dump %s: %w", database, err)
	}
	return withoutDumpKeys(raw), nil
}

// readReply reads pgcontainer's answer to a request: "ok <n>" and n bytes,
// or "err <message>".
func (p *Postgres) readReply() ([]byte, error) {
	line, err := p.out.ReadString('\n')
	if err != nil {
		return nil, err
	}
	verb, arg, _ := strings.Cut(strings.TrimSpace(line), " ")
	if verb != replyOK {
		return nil, errors.New(strings.TrimPrefix(strings.TrimSpace(line), replyErr+" "))
	}
	n, err := strconv.Atoi(arg)
	if err != nil {
		return nil, err
	}
	raw := make([]byte, n)
	_, err = io.ReadFull(p.out, raw)
	return raw, err
}

// withoutDumpKeys is a dump without the lines that carry its random key.
func withoutDumpKeys(raw []byte) string {
	var b strings.Builder
	for line := range strings.Lines(string(raw)) {
		if strings.HasPrefix(line, pgDumpRestrictPrefix) || strings.HasPrefix(line, pgDumpUnrestrictPrefix) {
			continue
		}
		b.WriteString(line)
	}
	return b.String()
}
