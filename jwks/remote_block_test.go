package jwks

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

// blockFixture is a Remote over a server holding two keys, kid and other,
// with the set fetched once, and what it logs.
type blockFixture struct {
	srv    *jwksServer
	r      *Remote
	clock  *stepClock
	kid    string
	other  string
	pub    ed25519.PublicKey
	logged *bytes.Buffer
}

func newBlockFixture(t *testing.T) blockFixture {
	t.Helper()
	srv, r, clock, kid, pub := newRemoteFixture(t)
	other, otherPub, _, err := generateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	srv.set(func() { srv.keys[other] = otherPub })
	var logged bytes.Buffer
	r.cfg.Logger = slog.New(slog.NewJSONHandler(&logged, nil))
	if _, err := r.PublicKey(context.Background(), kid); err != nil {
		t.Fatal(err)
	}
	return blockFixture{srv: srv, r: r, clock: clock, kid: kid, other: other, pub: pub, logged: &logged}
}

// logLines are the JSON lines f logged.
func (f blockFixture) logLines(t *testing.T) []map[string]any {
	t.Helper()
	var lines []map[string]any
	for line := range strings.Lines(f.logged.String()) {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("log line %q: %v", line, err)
		}
		lines = append(lines, m)
	}
	return lines
}

// A blocked kid is refused as unknown while the held set is fresh and has
// it, without a fetch; the other kid is served as before, and Unblock gives
// the kid back.
func TestRemoteBlockRefusesACachedKid(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newBlockFixture(t)
		ctx := context.Background()
		f.r.Block(f.kid)
		if _, err := f.r.PublicKey(ctx, f.kid); !errors.Is(err, ErrUnknownKID) || errors.Is(err, ErrUnavailable) {
			t.Fatalf("a blocked kid: err = %v, want ErrUnknownKID", err)
		}
		if _, err := f.r.PublicKey(ctx, f.other); err != nil {
			t.Errorf("a kid not blocked: %v", err)
		}
		awaitFetches(t, f.srv.fetches.Load, 1)
		f.r.Unblock(f.kid)
		if got, err := f.r.PublicKey(ctx, f.kid); err != nil || !got.Equal(f.pub) {
			t.Errorf("an unblocked kid: %v, %v", got, err)
		}
		lines := f.logLines(t)
		if len(lines) != 3 {
			t.Fatalf("logged %d lines, want block, refusal, unblock: %v", len(lines), lines)
		}
		block, refusal := lines[0], lines[1]
		if block["level"] != slog.LevelWarn.String() {
			t.Errorf("Block logged %v, want a WARN", block)
		}
		if kids, _ := block[logKeyKIDs].([]any); len(kids) != 1 || kids[0] != f.kid {
			t.Errorf("Block logged kids %v, want [%s]", block[logKeyKIDs], f.kid)
		}
		if refusal["level"] != slog.LevelWarn.String() || refusal[logKeyKID] != f.kid {
			t.Errorf("the refusal logged %v, want a WARN naming %s", refusal, f.kid)
		}
	})
}

// With the endpoint down, a blocked kid is refused as unknown, not served
// from the stale set until MaxStale nor answered as unavailable; the other
// kid is still served from it.
func TestRemoteBlockRefusesAKidWhileTheEndpointIsDown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newBlockFixture(t)
		ctx := context.Background()
		f.srv.set(func() { f.srv.fail = true })
		f.clock.Advance(2 * time.Minute) // past RefreshInterval, inside MaxStale
		f.r.Block(f.kid)
		if _, err := f.r.PublicKey(ctx, f.kid); !errors.Is(err, ErrUnknownKID) || errors.Is(err, ErrUnavailable) {
			t.Fatalf("a blocked kid, endpoint down: err = %v, want ErrUnknownKID", err)
		}
		if _, err := f.r.PublicKey(ctx, f.other); err != nil {
			t.Errorf("a kid not blocked, endpoint down: %v", err)
		}
	})
}

// Refresh fetches at once, inside MinRefreshInterval, so a key the issuer
// withdrew stops verifying when it returns; an endpoint that fails is
// reported, wrapping ErrUnavailable, and logged at ERROR.
func TestRemoteRefreshPicksUpAWithdrawal(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newBlockFixture(t)
		ctx := context.Background()
		f.srv.set(func() { delete(f.srv.keys, f.kid) })
		if _, err := f.r.PublicKey(ctx, f.kid); err != nil {
			t.Fatalf("before Refresh, the held set still has the kid: %v", err)
		}
		if err := f.r.Refresh(ctx); err != nil {
			t.Fatalf("Refresh: %v", err)
		}
		awaitFetches(t, f.srv.fetches.Load, 2)
		if _, err := f.r.PublicKey(ctx, f.kid); !errors.Is(err, ErrUnknownKID) {
			t.Errorf("a withdrawn kid after Refresh: err = %v, want ErrUnknownKID", err)
		}
		if _, err := f.r.PublicKey(ctx, f.other); err != nil {
			t.Errorf("a kid still served after Refresh: %v", err)
		}

		f.srv.set(func() { f.srv.fail = true })
		if err := f.r.Refresh(ctx); !errors.Is(err, ErrUnavailable) {
			t.Errorf("Refresh with the endpoint down: err = %v, want ErrUnavailable", err)
		}
		lines := f.logLines(t)
		if len(lines) < 2 || lines[0]["level"] != slog.LevelInfo.String() || lines[len(lines)-1]["level"] != slog.LevelError.String() {
			t.Errorf("logged %v, want an INFO refresh, then an ERROR one", lines)
		}
	})
}

// refreshGiveUp is how long a caller of Refresh waits behind a fetch that
// never ends.
const refreshGiveUp = 50 * time.Millisecond

// Refresh gives up when its caller does, while another fetch holds the slot.
func TestRemoteRefreshStopsOnItsContext(t *testing.T) {
	f := newBlockFixture(t)
	f.r.fetching <- struct{}{} // a fetch in flight
	defer f.r.releaseFetch()
	ctx, cancel := context.WithTimeout(context.Background(), refreshGiveUp)
	defer cancel()
	if err := f.r.Refresh(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Refresh behind a fetch that never ends: err = %v, want context.DeadlineExceeded", err)
	}
}
