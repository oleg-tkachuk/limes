package pgstore

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/jwks"
	"github.com/oleg-tkachuk/limes/memstore"
	"github.com/oleg-tkachuk/limes/meter"
)

// warnedUnavailable is how a WARN line naming store_unavailable starts its
// attributes in the JSON log.
const warnedUnavailable = `"level":"WARN","msg":"pgstore: the database could not serve a call; answered with a code to retry","code":"store_unavailable"`

// deadDialTimeout bounds a dial to the closed port: the refusal comes at
// once, so a slow answer is the test's fault.
const deadDialTimeout = 2 * time.Second

// deadDSN is a database URL at a port of 127.0.0.1 nothing listens on.
func deadDSN(t *testing.T) string {
	t.Helper()
	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("postgres://limes@%s/limes?sslmode=disable&connect_timeout=%d", addr, int(deadDialTimeout/time.Second))
}

// deadPool is a pool of a database nothing answers for.
func deadPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), deadDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// connectError is the error pgx returns for a connection never made: a
// dial to the closed port.
func connectError(t *testing.T) error {
	t.Helper()
	_, err := pgconn.Connect(context.Background(), deadDSN(t))
	var connectErr *pgconn.ConnectError
	if !errors.As(err, &connectErr) {
		t.Fatalf("a dial to a closed port: %v, want a *pgconn.ConnectError", err)
	}
	return err
}

// pgError is the database's answer with SQLSTATE code.
func pgError(code string) error {
	return fmt.Errorf("a statement: %w", &pgconn.PgError{Code: code, Message: "the database's message"})
}

// timedOutNet is a network operation that timed out.
type timedOutNet struct{}

func (timedOutNet) Error() string   { return "i/o timeout" }
func (timedOutNet) Timeout() bool   { return true }
func (timedOutNet) Temporary() bool { return true }

// Each failure of the database is answered by the code that says what
// retrying would do, the driver's error kept in the chain; what says
// nothing of the database, or may have left a write applied, keeps no code
// of limes's.
func TestStoreFailure(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	outcome := fmt.Errorf("%w: %w", meter.ErrOutcomeUnknown, pgconn.ErrConnClosed)
	for name, tc := range map[string]struct {
		ctx    context.Context
		err    error
		writes bool
		want   error // nil: no code of limes's added
	}{
		"statement_timeout":                {err: pgError(pgerrcode.QueryCanceled), want: limes.ErrStoreTimeout},
		"lock_timeout":                     {err: pgError(pgerrcode.LockNotAvailable), want: limes.ErrStoreTimeout},
		"idle in transaction timeout":      {err: pgError(pgerrcode.IdleInTransactionSessionTimeout), want: limes.ErrStoreTimeout},
		"statement_timeout in a write":     {err: pgError(pgerrcode.QueryCanceled), writes: true, want: limes.ErrStoreTimeout},
		"admin shutdown":                   {err: pgError(pgerrcode.AdminShutdown), want: limes.ErrStoreUnavailable},
		"crash shutdown":                   {err: pgError(pgerrcode.CrashShutdown), want: limes.ErrStoreUnavailable},
		"cannot connect now":               {err: pgError(pgerrcode.CannotConnectNow), want: limes.ErrStoreUnavailable},
		"connection exception":             {err: pgError(pgerrcode.ConnectionException), want: limes.ErrStoreUnavailable},
		"connection failure":               {err: pgError(pgerrcode.ConnectionFailure), want: limes.ErrStoreUnavailable},
		"too many connections":             {err: pgError(pgerrcode.TooManyConnections), want: limes.ErrStoreUnavailable},
		"out of memory":                    {err: pgError(pgerrcode.OutOfMemory), want: limes.ErrStoreUnavailable},
		"disk full":                        {err: pgError(pgerrcode.DiskFull), want: limes.ErrStoreUnavailable},
		"serialization failure":            {err: pgError(pgerrcode.SerializationFailure), writes: true, want: limes.ErrStoreUnavailable},
		"a deadlock, retryDeadlock's":      {err: pgError(pgerrcode.DeadlockDetected), writes: true},
		"a connection never made":          {err: connectError(t), want: limes.ErrStoreUnavailable},
		"a connection never made, a write": {err: connectError(t), writes: true, want: limes.ErrStoreUnavailable},
		"a connection closed under a read": {err: pgconn.ErrConnClosed, want: limes.ErrStoreUnavailable},
		"a network timeout under a read":   {err: &net.OpError{Op: "read", Err: timedOutNet{}}, want: limes.ErrStoreUnavailable},
		// What may have left a write applied, or says nothing of the
		// database, keeps no code: sent again, it could apply twice.
		"a connection closed under a write":   {err: pgconn.ErrConnClosed, writes: true},
		"a network timeout under a write":     {err: &net.OpError{Op: "read", Err: timedOutNet{}}, writes: true},
		"statement completion unknown":        {err: pgError(pgerrcode.StatementCompletionUnknown), writes: true},
		"a unique violation":                  {err: pgError(pgerrcode.UniqueViolation)},
		"a syntax error":                      {err: pgError(pgerrcode.SyntaxError)},
		"the caller's own cancelling":         {ctx: cancelled, err: pgError(pgerrcode.QueryCanceled)},
		"the caller's own cancelling, a dial": {ctx: cancelled, err: connectError(t)},
		"an error of the caller's":            {err: errors.New("the consumer's own")},
	} {
		ctx := tc.ctx
		if ctx == nil {
			ctx = context.Background()
		}
		got, _ := storeFailure(ctx, tc.err, tc.writes)
		if !errors.Is(got, tc.err) {
			t.Errorf("%s: %v lost the driver's error", name, got)
		}
		code, coded := limes.CodeOf(got)
		switch {
		case tc.want == nil && coded:
			t.Errorf("%s: answered %s, want no code of limes's", name, code)
		case tc.want != nil && !errors.Is(got, tc.want):
			t.Errorf("%s: %v, want %v", name, got, tc.want)
		}
	}
	// A code of limes's already is kept: an unanswered commit outranks the
	// connection it was lost on.
	if got, _ := storeFailure(context.Background(), outcome, true); !errors.Is(got, meter.ErrOutcomeUnknown) || errors.Is(got, limes.ErrStoreUnavailable) {
		t.Errorf("an unknown outcome: %v, want it kept as it is", got)
	}
	var pgErr *pgconn.PgError
	if got, _ := storeFailure(context.Background(), pgError(pgerrcode.AdminShutdown), false); !errors.As(got, &pgErr) || pgErr.Code != pgerrcode.AdminShutdown {
		t.Errorf("errors.As to the PgError through %v: %v", got, pgErr)
	}
}

// A store over a database nothing answers for answers every call with a
// code to retry: a read and a write alike, the connection never having
// been made. The problem is 503, in category unavailable.
func TestStoreUnreachable(t *testing.T) {
	pool := deadPool(t)
	ctx := WithTenant(context.Background(), uuid.New())
	var logged bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logged, nil))
	records, err := NewStore(Config{DB: pool, Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	usage, err := NewUsageStore(Config{DB: pool, Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	logged.Reset()
	tenant := uuid.New()
	for name, call := range map[string]func() error{
		"GetRecord": func() error { _, err := records.GetRecord(ctx, uuid.New()); return err },
		"IsRevoked": func() error { _, err := records.IsRevoked(ctx, uuid.New()); return err },
		"Revoke":    func() error { _, err := records.Revoke(ctx, limes.RevokeRequest{CapabilityID: uuid.New()}); return err },
		"GetUsage":  func() error { _, err := usage.GetUsage(ctx, uuid.New()); return err },
		"Charge": func() error {
			_, err := usage.Charge(WithTenant(ctx, tenant), meter.ChargeRequest{Subject: meterSubject(uuid.New(), tenant), Amount: 1}, nil)
			return err
		},
		"SetTenantBudget": func() error {
			_, err := usage.SetTenantBudget(WithTenant(ctx, tenant), meter.SetTenantBudgetRequest{TenantID: tenant, Ceiling: limes.BudgetCeiling{Unlimited: true, UnitCode: testUnit}})
			return err
		},
	} {
		err := call()
		p := limes.ProblemFor(err)
		if !errors.Is(err, limes.ErrStoreUnavailable) || p.Code != limes.CodeStoreUnavailable || p.Status != http.StatusServiceUnavailable || p.Category != limes.CategoryUnavailable {
			t.Errorf("%s: %v answered %s %d %s, want store_unavailable 503", name, err, p.Code, p.Status, p.Category)
		}
		// Each is logged where the store decided, with its code.
		if n := strings.Count(logged.String(), warnedUnavailable); n != 1 {
			t.Errorf("%s: logged %d WARN lines naming store_unavailable, want 1: %s", name, n, logged.String())
		}
		logged.Reset()
	}
}

// A verifier whose revocation store is unreachable answers
// revocation_unavailable, its own code for the lookup, not the store's.
func TestRevocationUnavailableOutranksTheStore(t *testing.T) {
	key, err := limes.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	signer, err := limes.NewSigner(limes.SignerConfig{KeyID: key.KeyID, Key: key.Private})
	if err != nil {
		t.Fatal(err)
	}
	records, err := memstore.NewStore(memstore.StoreConfig{})
	if err != nil {
		t.Fatal(err)
	}
	iss, err := limes.NewIssuer(limes.IssuerConfig{Signer: signer, Store: records, IssuerName: "issuer"})
	if err != nil {
		t.Fatal(err)
	}
	issued, err := iss.Issue(context.Background(), limes.IssueRequest{
		ClientID: "client", IssuedBy: limes.Principal{Subject: "operator"}, Audience: []string{"svc"}, TTL: time.Minute,
		Subject: limes.Principal{Type: limes.PrincipalTypeService, TenantID: uuid.New(), Subject: "svc"},
		Caveats: limes.Caveats{Requests: limes.RequestCeiling{Unlimited: true}, Ops: []limes.Op{limes.OpGet}},
	})
	if err != nil {
		t.Fatal(err)
	}
	keys, err := jwks.NewStatic(jwks.StaticConfig{Keys: map[string]ed25519.PublicKey{key.KeyID: key.Public()}})
	if err != nil {
		t.Fatal(err)
	}
	dead, err := NewStore(Config{DB: deadPool(t)})
	if err != nil {
		t.Fatal(err)
	}
	v, err := limes.NewVerifier(limes.VerifierConfig{Keys: keys, Revocations: dead, TrustedIssuers: []string{"issuer"}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = v.Verify(context.Background(), limes.VerifyRequest{Token: issued.Token, Audience: "svc"})
	if code, _ := limes.CodeOf(err); code != limes.CodeRevocationUnavailable || !errors.Is(err, limes.ErrStoreUnavailable) {
		t.Errorf("Verify over an unreachable store: %v answered %s, want revocation_unavailable over store_unavailable", err, code)
	}
}

// A meter call deadlocked once runs again; deadlocked a second time it is
// answered store_unavailable, the deadlock kept in the chain, and logged.
func TestRetryDeadlockAnswersTheSecond(t *testing.T) {
	var logged bytes.Buffer
	c := conn{log: slog.New(slog.NewJSONHandler(&logged, nil))}
	deadlock := pgError(pgerrcode.DeadlockDetected)
	calls := 0
	err := c.retryDeadlock(context.Background(), retriedCharge, func() error { calls++; return deadlock })
	if calls != 2 || !errors.Is(err, limes.ErrStoreUnavailable) || !isDeadlock(err) {
		t.Errorf("%d calls, %v; want two, store_unavailable over the deadlock", calls, err)
	}
	if !strings.Contains(logged.String(), `"code":"store_unavailable"`) {
		t.Errorf("logged %s, want the second deadlock's line", logged.String())
	}
	calls = 0
	if err := c.retryDeadlock(context.Background(), retriedCharge, func() error {
		calls++
		if calls == 1 {
			return deadlock
		}
		return nil
	}); err != nil || calls != 2 {
		t.Errorf("a deadlock then success: %d calls, %v", calls, err)
	}
}
