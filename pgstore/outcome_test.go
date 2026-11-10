package pgstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"uuid"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/oleg-tkachuk/limes/meter"
)

// safeToRetry is a pgconn error that sent nothing, as pgconn marks one.
type safeToRetry struct{ error }

func (safeToRetry) SafeToRetry() bool { return true }

func (e safeToRetry) Unwrap() error { return e.error }

// The outcome of a call is unknown only when the database may have
// committed and its answer was lost: not when it answered, nor when pgx
// sent nothing. A closed connection is taken as unknown: pgx reports a
// COMMIT whose answer was lost so.
func TestCommitUnanswered(t *testing.T) {
	lost := io.ErrUnexpectedEOF
	for name, tc := range map[string]struct {
		err  error
		want bool
	}{
		"no error":                   {nil, false},
		"the connection dropped":     {lost, true},
		"the context ended":          {fmt.Errorf("read: %w", context.DeadlineExceeded), true},
		"an error of PostgreSQL's":   {&pgconn.PgError{Code: uniqueViolation}, false},
		"PostgreSQL's beside a drop": {errors.Join(&pgconn.PgError{Code: pgerrcode.DeadlockDetected}, lost), false},
		"COMMIT rolled back":         {errNotCommitted, false},
		"the transaction ended":      {pgx.ErrTxClosed, false},
		"nothing sent":               {safeToRetry{lost}, false},
		"the connection closed":      {safeToRetry{pgconn.ErrConnClosed}, true},
	} {
		if got := commitUnanswered(tc.err); got != tc.want {
			t.Errorf("%s: commitUnanswered(%v) = %v, want %v", name, tc.err, got, tc.want)
		}
		got := outcomeOf(tc.err)
		if unknown := errors.Is(got, meter.ErrOutcomeUnknown); unknown != tc.want || !errors.Is(got, tc.err) {
			t.Errorf("%s: outcomeOf(%v) = %v; want ErrOutcomeUnknown %v, and the cause kept", name, tc.err, got, tc.want)
		}
	}
}

// commitTag is the CommandComplete message PostgreSQL answers a COMMIT that
// committed with: 'C', its length, the tag.
var commitTag = []byte("C\x00\x00\x00\x0bCOMMIT\x00")

// errCut is the read a cutter fails once the database has committed.
var errCut = errors.New("pgstore test: connection cut after COMMIT was answered")

// cutter cuts, while armed, the first connection that reads PostgreSQL's
// answer that a COMMIT committed, before the answer reaches pgx: the
// database has committed, and the client never learns it.
type cutter struct {
	armed atomic.Bool
	cut   atomic.Int64
}

// dial is a pgx DialFunc whose connections c cuts.
func (c *cutter) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	return &cutConn{Conn: conn, cutter: c}, nil
}

// cutConn is one connection a cutter watches.
type cutConn struct {
	net.Conn
	cutter *cutter
	mu     sync.Mutex
	// tail is the end of what was read last, so a tag split between two
	// reads is still found: a tag's length less one byte.
	tail []byte
	dead bool
}

func (c *cutConn) Read(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dead {
		return 0, errCut
	}
	n, err := c.Conn.Read(p)
	if c.cutter.armed.Load() && bytes.Contains(append(c.tail, p[:n]...), commitTag) && c.cutter.armed.CompareAndSwap(true, false) {
		c.dead = true
		c.cutter.cut.Add(1)
		return 0, errCut
	}
	// One byte short of a tag, so a tag read whole before is never found
	// again.
	c.tail = append(c.tail[:0], p[max(0, n-len(commitTag)+1):n]...)
	return n, err
}

// cutPool is a pool as appUser whose connections c cuts, in plain text so
// the cutter reads the protocol.
func cutPool(t *testing.T, c *cutter) *pgxpool.Pool {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(ownerDSN)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.User, cfg.ConnConfig.Password = appUser, appPassword
	cfg.ConnConfig.TLSConfig, cfg.ConnConfig.Fallbacks = nil, nil
	cfg.ConnConfig.DialFunc = c.dial
	cfg.MaxConns = appPoolConns
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// A keyed call whose COMMIT committed and whose answer was lost returns
// ErrOutcomeUnknown beside the cause; sent again, it replays what
// committed and moves nothing twice. Reserve sends COMMIT in its last
// batch, Refund on its own.
func TestALostCommitIsAnOutcomeUnknown(t *testing.T) {
	f := newTenantFixture(t)
	ctx := WithTenant(t.Context(), f.mine)
	c := &cutter{}
	cut, err := NewUsageStore(Config{DB: cutPool(t, c), Now: f.clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	subject := meterSubject(f.child.ID, f.mine)
	const amount = 3
	charged, err := f.usage.Charge(ctx, meter.ChargeRequest{Subject: subject, Amount: amount}, nil)
	if err != nil {
		t.Fatal(err)
	}
	reserve := meter.ReserveRequest{Subject: subject, Amount: amount, ReservationID: uuid.New()}
	refund := meter.RefundRequest{ChargeID: charged.ChargeID, Amount: 1, RefundID: uuid.New()}
	for name, tc := range map[string]struct {
		call     func(*UsageStore) (replayed bool, err error)
		recorded func() bool
	}{
		"Reserve": {
			call: func(s *UsageStore) (bool, error) {
				r, err := s.Reserve(ctx, reserve)
				return r.Replayed, err
			},
			recorded: func() bool {
				u, err := f.usage.GetUsage(ctx, f.child.ID)
				return err == nil && u.ReservedAmount == amount
			},
		},
		"Refund": {
			call: func(s *UsageStore) (bool, error) {
				r, err := s.Refund(ctx, refund, nil)
				return r.Replayed, err
			},
			recorded: func() bool {
				ch, err := f.usage.GetCharge(ctx, charged.ChargeID)
				return err == nil && ch.RefundedAmount == refund.Amount
			},
		},
	} {
		c.armed.Store(true)
		_, err := tc.call(cut)
		if c.cut.Load() == 0 {
			t.Fatalf("%s: no connection cut; err = %v", name, err)
		}
		c.cut.Store(0)
		// Reserve's batch keeps the cause; pgx reports Refund's COMMIT,
		// sent alone, as a closed connection.
		causeKept := errors.Is(err, errCut) || errors.Is(err, pgconn.ErrConnClosed)
		if !errors.Is(err, meter.ErrOutcomeUnknown) || !causeKept {
			t.Errorf("%s with its COMMIT's answer lost: err = %v, want ErrOutcomeUnknown and the cause", name, err)
		}
		if !tc.recorded() {
			t.Errorf("%s: the commit whose answer was lost is not on record", name)
		}
		replayed, err := tc.call(cut)
		if err != nil || !replayed {
			t.Errorf("%s sent again: replayed %v, %v; want a replay", name, replayed, err)
		}
		if !tc.recorded() {
			t.Errorf("%s sent again moved something twice", name)
		}
	}
}
