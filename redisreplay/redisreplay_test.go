package redisreplay

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
	"uuid"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/oleg-tkachuk/limes/dpop"
	"github.com/oleg-tkachuk/limes/dpop/dpoptest"
)

// redisAddrEnv names a real Redis to run the contract against as well, as
// host:port. Unset, that run is skipped: miniredis runs its scripts on
// another Lua, which a real server is the only check of.
const redisAddrEnv = "LIMES_REDIS_ADDR"

// redisRequiredEnv, set to any value, makes an unset redisAddrEnv a failure
// rather than a skip: the CI job that brings a Redis up sets it, so a job
// that lost the address cannot pass by checking nothing.
const redisRequiredEnv = "LIMES_REDIS_REQUIRED"

// testEpoch is where the clock of these tests starts.
var testEpoch = time.Unix(1_800_000_000, 0)

// entriesPerHolder is small, so the check of a holder's share fills it fast.
const entriesPerHolder = 4

// proofLifetime is how long the ids these tests record can be accepted.
const proofLifetime = time.Minute

// testHolder and testJKT are the holder and key thumbprint of these tests'
// ids; their form does not matter to the cache.
const (
	testHolder = "capability-a"
	testJKT    = "key-a"
)

// testClock is a clock a test moves by hand.
type testClock struct{ now time.Time }

func (c *testClock) Now() time.Time          { return c.now }
func (c *testClock) Advance(d time.Duration) { c.now = c.now.Add(d) }

// fixture is a cache over a fresh miniredis, and what reads the server back.
type fixture struct {
	ctx    context.Context
	cache  *Cache
	server *miniredis.Miniredis
	clock  *testClock
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	clock := &testClock{now: testEpoch}
	cache, err := New(Config{Client: client, EntriesPerHolder: entriesPerHolder, Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	return fixture{ctx: context.Background(), cache: cache, server: server, clock: clock}
}

// entry is a fresh id of testJKT, counted against testHolder and accepted for
// proofLifetime from now.
func (f fixture) entry() dpop.ReplayEntry {
	return dpop.ReplayEntry{JKT: testJKT, JTI: uuid.New().String(), Holder: testHolder, ExpiresAt: f.clock.now.Add(proofLifetime)}
}

func (f fixture) seen(t *testing.T, e dpop.ReplayEntry) bool {
	t.Helper()
	replay, err := f.cache.Seen(f.ctx, e)
	if err != nil {
		t.Fatalf("Seen(%+v): %v", e, err)
	}
	return replay
}

func TestContractOnMiniredis(t *testing.T) {
	dpoptest.RunReplayCache(t, func(t *testing.T) dpoptest.ReplayEnv {
		t.Helper()
		f := newFixture(t)
		return dpoptest.ReplayEnv{
			Ctx: f.ctx, Cache: f.cache,
			Now: f.clock.Now, Advance: f.clock.Advance, EntriesPerHolder: entriesPerHolder,
		}
	})
}

func TestContractOnRedis(t *testing.T) {
	addr := os.Getenv(redisAddrEnv)
	if addr == "" {
		if os.Getenv(redisRequiredEnv) != "" {
			t.Fatalf("%s is set but %s is not", redisRequiredEnv, redisAddrEnv)
		}
		t.Skipf("%s not set", redisAddrEnv)
	}
	client := redis.NewClient(&redis.Options{Addr: addr})
	t.Cleanup(func() { _ = client.Close() })
	dpoptest.RunReplayCache(t, func(t *testing.T) dpoptest.ReplayEnv {
		t.Helper()
		clock := &testClock{now: testEpoch}
		// A prefix of its own makes each check's cache fresh on a shared server.
		cache, err := New(Config{
			Client: client, KeyPrefix: DefaultKeyPrefix + uuid.New().String() + ":",
			EntriesPerHolder: entriesPerHolder, Now: clock.Now,
		})
		if err != nil {
			t.Fatal(err)
		}
		return dpoptest.ReplayEnv{
			Ctx: context.Background(), Cache: cache,
			Now: clock.Now, Advance: clock.Advance, EntriesPerHolder: entriesPerHolder,
		}
	})
}

func TestNew(t *testing.T) {
	client := redis.NewClient(&redis.Options{Addr: miniredis.RunT(t).Addr()})
	t.Cleanup(func() { _ = client.Close() })
	const (
		ownPrefix = "own:"
		ownShare  = 7
	)
	tests := []struct {
		name       string
		cfg        Config
		wantErr    bool
		wantPrefix string
		wantShare  int
	}{
		{name: "no client", cfg: Config{}, wantErr: true},
		{name: "negative share", cfg: Config{Client: client, EntriesPerHolder: -1}, wantErr: true},
		{name: "defaults", cfg: Config{Client: client}, wantPrefix: DefaultKeyPrefix, wantShare: dpop.DefaultReplayEntriesPerHolder},
		{name: "own values", cfg: Config{Client: client, KeyPrefix: ownPrefix, EntriesPerHolder: ownShare}, wantPrefix: ownPrefix, wantShare: ownShare},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := New(tt.cfg)
			if tt.wantErr {
				if err == nil {
					t.Fatal("New accepted the config")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if c.prefix != tt.wantPrefix || c.share != tt.wantShare || c.now == nil {
				t.Errorf("New = prefix %q, share %d, clock set %t; want %q, %d, true", c.prefix, c.share, c.now != nil, tt.wantPrefix, tt.wantShare)
			}
		})
	}
}

func TestSeenReportsAnOutage(t *testing.T) {
	f := newFixture(t)
	f.server.Close()
	replay, err := f.cache.Seen(f.ctx, f.entry())
	if err == nil {
		t.Fatalf("Seen with Redis down = %t, nil; want an error", replay)
	}
}

func TestSeenStopsOnACancelledContext(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithCancel(f.ctx)
	cancel()
	if _, err := f.cache.Seen(ctx, f.entry()); !errors.Is(err, context.Canceled) {
		t.Fatalf("Seen on a cancelled context: %v, want context.Canceled", err)
	}
}

func TestSeenKeepsOneKeyPerHolderUntilItsLastIDLapses(t *testing.T) {
	f := newFixture(t)
	long := f.entry()
	long.ExpiresAt = f.clock.now.Add(2 * proofLifetime)
	f.seen(t, long)
	// A shorter id after it must not shorten the key's life under the first.
	f.seen(t, f.entry())

	key := DefaultKeyPrefix + testHolder
	if keys := f.server.Keys(); len(keys) != 1 || keys[0] != key {
		t.Fatalf("keys = %q, want only %q", keys, key)
	}
	if ttl := f.server.TTL(key); ttl != 2*proofLifetime {
		t.Errorf("TTL of the holder's key = %v, want %v", ttl, 2*proofLifetime)
	}
}

func TestSeenRecordsNothingAlreadyLapsed(t *testing.T) {
	f := newFixture(t)
	lapsed := f.entry()
	lapsed.ExpiresAt = f.clock.now
	if f.seen(t, lapsed) {
		t.Fatal("a lapsed id was reported as a replay")
	}
	if keys := f.server.Keys(); len(keys) != 0 {
		t.Errorf("a lapsed id left keys %q", keys)
	}
}

func TestMembersOfDifferentPairsDiffer(t *testing.T) {
	f := newFixture(t)
	// The same characters split differently between thumbprint and id.
	first := dpop.ReplayEntry{JKT: "ab", JTI: "c", Holder: testHolder, ExpiresAt: f.clock.now.Add(proofLifetime)}
	second := first
	second.JKT, second.JTI = "a", "bc"
	f.seen(t, first)
	if f.seen(t, second) {
		t.Error("an id of another key was a replay because the two spell the same")
	}
}

func TestCeilMilli(t *testing.T) {
	base := time.UnixMilli(testEpoch.UnixMilli())
	tests := []struct {
		name string
		t    time.Time
		want int64
	}{
		{"whole millisecond", base, base.UnixMilli()},
		{"a nanosecond past", base.Add(time.Nanosecond), base.UnixMilli() + 1},
		{"a nanosecond short of the next", base.Add(time.Millisecond - time.Nanosecond), base.UnixMilli() + 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ceilMilli(tt.t); got != tt.want {
				t.Errorf("ceilMilli = %d, want %d", got, tt.want)
			}
		})
	}
}
