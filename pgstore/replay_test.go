package pgstore

import (
	"bytes"
	"context"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/oleg-tkachuk/limes/dpop"
	"github.com/oleg-tkachuk/limes/dpop/dpoptest"
)

// The replay cache tests' share of one holder, its proof lifetime, and the
// holder and keys they use.
const (
	testReplayShare  = 3
	replayLifetime   = time.Minute
	replayHolder     = "capability-a"
	replayKey        = "key-a"
	racingProofs     = 20
	racingPoolConns  = 10
	smallPurgeBatch  = 2
	expiredReplayIDs = 5
)

// replayCache is a ReplayCache over app, on clock, with the tests' share,
// logging to log when it is set.
func replayCache(t *testing.T, db DB, clock *fakeClock, log *slog.Logger) *ReplayCache {
	t.Helper()
	c, err := NewReplayCache(ReplayConfig{DB: db, EntriesPerHolder: testReplayShare, Now: clock.Now, Logger: log})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// replayEntry is a proof id of holder, fresh, expiring replayLifetime after
// clock's now.
func replayEntry(clock *fakeClock, holder, jti string) dpop.ReplayEntry {
	return dpop.ReplayEntry{JKT: replayKey, JTI: jti, Holder: holder, ExpiresAt: clock.Now().Add(replayLifetime)}
}

func TestReplayCacheContract(t *testing.T) {
	dpoptest.RunReplayCache(t, func(t *testing.T) dpoptest.ReplayEnv {
		fresh(t)
		clock := newFakeClock()
		return dpoptest.ReplayEnv{
			Ctx:              t.Context(),
			Cache:            replayCache(t, app, clock, nil),
			Now:              clock.Now,
			Advance:          clock.Advance,
			EntriesPerHolder: testReplayShare,
		}
	})
}

func TestNewReplayCacheRefusesItsMisconfiguration(t *testing.T) {
	if _, err := NewReplayCache(ReplayConfig{}); err == nil {
		t.Error("a replay cache with no DB was built")
	}
	if _, err := NewReplayCache(ReplayConfig{DB: app, EntriesPerHolder: -1}); err == nil {
		t.Error("a replay cache with a negative share was built")
	}
	c, err := NewReplayCache(ReplayConfig{DB: app})
	if err != nil || c.share != dpop.DefaultReplayEntriesPerHolder {
		t.Errorf("the zero share = %v, %v; want dpop.DefaultReplayEntriesPerHolder", c, err)
	}
}

// Two replicas share one table: a proof id one accepted, the other refuses.
func TestReplayCacheAcrossReplicas(t *testing.T) {
	fresh(t)
	clock := newFakeClock()
	other, err := poolAs(t.Context(), ownerDSN, appUser, appPassword)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(other.Close)
	a, b := replayCache(t, app, clock, nil), replayCache(t, other, clock, nil)
	e := replayEntry(clock, replayHolder, "jti-1")
	if seen, err := a.Seen(t.Context(), e); seen || err != nil {
		t.Fatalf("the first replica's first sight = %v, %v; want new", seen, err)
	}
	if seen, err := b.Seen(t.Context(), e); !seen || err != nil {
		t.Errorf("the other replica's sight of the same id = %v, %v; want a replay", seen, err)
	}
}

// An id that has lapsed already is new and kept nowhere: nothing could
// replay it.
func TestReplayCacheKeepsNoLapsedID(t *testing.T) {
	fresh(t)
	clock := newFakeClock()
	c := replayCache(t, app, clock, nil)
	e := replayEntry(clock, replayHolder, "lapsed")
	e.ExpiresAt = clock.Now()
	for range 2 {
		if seen, err := c.Seen(t.Context(), e); seen || err != nil {
			t.Fatalf("a lapsed id = %v, %v; want new", seen, err)
		}
	}
	if n := replayRows(t); n != 0 {
		t.Errorf("%d rows kept for a lapsed id, want none", n)
	}
}

// replayRows counts the rows of the replay table.
func replayRows(t *testing.T) int {
	t.Helper()
	var n int
	if err := owner.QueryRow(t.Context(), `SELECT count(*) FROM `+replayTable).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// PurgeExpired deletes the expired ids a batch at a time and leaves the
// live ones; it returns 0 once none is left.
func TestReplayCachePurgesExpiredIDs(t *testing.T) {
	fresh(t)
	clock := newFakeClock()
	c := replayCache(t, app, clock, nil)
	c.purgeBatch = smallPurgeBatch
	for i := range expiredReplayIDs {
		if _, err := c.Seen(t.Context(), replayEntry(clock, "holder-"+strconv.Itoa(i), "old-"+strconv.Itoa(i))); err != nil {
			t.Fatal(err)
		}
	}
	clock.Advance(replayLifetime + time.Second)
	if _, err := c.Seen(t.Context(), replayEntry(clock, replayHolder, "live")); err != nil {
		t.Fatal(err)
	}
	var purged []int64
	for {
		n, err := c.PurgeExpired(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		purged = append(purged, n)
		if n == 0 {
			break
		}
	}
	want := []int64{smallPurgeBatch, smallPurgeBatch, expiredReplayIDs - 2*smallPurgeBatch, 0}
	if len(purged) != len(want) {
		t.Fatalf("purged %v, want %v", purged, want)
	}
	for i := range want {
		if purged[i] != want[i] {
			t.Fatalf("purged %v, want %v", purged, want)
		}
	}
	if n := replayRows(t); n != 1 {
		t.Errorf("%d rows left, want the one live id", n)
	}
}

// A holder refused for its share is logged at WARN with the holder and the
// share; a replay is not.
func TestReplayCacheWarnsAHolderPastItsShare(t *testing.T) {
	fresh(t)
	clock := newFakeClock()
	var buf bytes.Buffer
	c := replayCache(t, app, clock, slog.New(slog.NewTextHandler(&buf, nil)))
	first := replayEntry(clock, replayHolder, "jti-0")
	for i := range testReplayShare {
		e := replayEntry(clock, replayHolder, "jti-"+strconv.Itoa(i))
		if i == 0 {
			e = first
		}
		if seen, err := c.Seen(t.Context(), e); seen || err != nil {
			t.Fatalf("id %d within the share = %v, %v", i, seen, err)
		}
	}
	if seen, err := c.Seen(t.Context(), first); !seen || err != nil {
		t.Fatalf("a replay = %v, %v", seen, err)
	}
	if buf.Len() != 0 {
		t.Errorf("a replay was logged: %s", buf.String())
	}
	if seen, err := c.Seen(t.Context(), replayEntry(clock, replayHolder, "one-too-many")); !seen || err != nil {
		t.Fatalf("an id past the share = %v, %v; want refused", seen, err)
	}
	line := buf.String()
	for _, want := range []string{"level=WARN", logKeyHolder + "=" + replayHolder, logKeyShare + "=3"} {
		if !strings.Contains(line, want) {
			t.Errorf("the refusal's log %q lacks %q", line, want)
		}
	}
}

// A cache whose database cannot answer refuses the proof with the error:
// an outage, not a replay.
func TestReplayCacheFailsClosed(t *testing.T) {
	clock := newFakeClock()
	closed, err := poolAs(t.Context(), ownerDSN, appUser, appPassword)
	if err != nil {
		t.Fatal(err)
	}
	closed.Close()
	c := replayCache(t, closed, clock, nil)
	seen, err := c.Seen(t.Context(), replayEntry(clock, replayHolder, "jti"))
	if !seen || err == nil {
		t.Errorf("Seen on a closed pool = %v, %v; want refused with the error", seen, err)
	}
	if _, err := c.PurgeExpired(t.Context()); err == nil {
		t.Error("PurgeExpired on a closed pool: no error")
	}
}

// More proofs of one holder than its share, all at once, record the share
// and no more: the count and the insert are one decision per holder.
func TestReplayCacheShareHoldsUnderARace(t *testing.T) {
	fresh(t)
	clock := newFakeClock()
	cfg, err := pgxpool.ParseConfig(ownerDSN)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.User, cfg.ConnConfig.Password = appUser, appPassword
	cfg.MaxConns = racingPoolConns
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	c := replayCache(t, pool, clock, nil)
	var admitted atomic.Int64
	var wg sync.WaitGroup
	errs := make(chan error, racingProofs)
	for i := range racingProofs {
		wg.Go(func() {
			seen, err := c.Seen(context.WithoutCancel(t.Context()), replayEntry(clock, replayHolder, "race-"+strconv.Itoa(i)))
			if err != nil {
				errs <- err
				return
			}
			if !seen {
				admitted.Add(1)
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if got := admitted.Load(); got != testReplayShare {
		t.Errorf("%d of %d racing proofs admitted, want the share, %d", got, racingProofs, testReplayShare)
	}
	if n := replayRows(t); n != testReplayShare {
		t.Errorf("%d rows recorded, want the share, %d", n, testReplayShare)
	}
}
