// Package redisreplay is a dpop.ReplayCache over Redis, for a verifier that
// runs as several replicas: a proof replayed against any of them is refused,
// which an in-process dpop.MemoryReplayCache cannot promise.
//
// It is a module of its own so that limes itself never depends on a Redis
// client.
//
// # Layout
//
// Each holder has one sorted set, KeyPrefix followed by the holder, whose
// members are the proof ids recorded against it and whose scores are when
// they lapse. One call touches that one key, in one script, so a call is
// atomic and the cache works unchanged on Redis Cluster.
//
// Ids are remembered per holder, not across holders. That loses nothing: a
// proof names the access token it was made for (its `ath`), and
// dpop.Verifier.Verify refuses it with any other token before the cache is
// asked, so a captured proof cannot reach the cache under another holder.
//
// # Clocks
//
// What is live is decided by Config.Now, the verifier's clock, which every
// call sends to the script. Redis's own clock only runs the key's TTL, a
// duration that collects a holder gone quiet; a server clock running ahead
// cannot drop a live id.
//
// The verifiers' clocks must agree to within dpop.ReplayClockSkew. An id is
// kept until ReplayClockSkew past the last instant a verifier accepts its
// proof, and each call drops the ids that have lapsed by its own clock: a
// replica running further ahead than that drops an id while a replica behind
// it still accepts the proof, which can then be replayed there. Keep the
// replicas on NTP.
//
// # Memory
//
// EntriesPerHolder bounds each holder; nothing bounds the holders. Bound the
// whole cache with Redis's maxmemory and the noeviction policy: past it a
// write fails, Seen returns the error and the proof is refused. An eviction
// policy would forget live ids, and a forgotten id can be replayed.
//
// # Failover
//
// Redis replicates asynchronously, so ids a primary acknowledged and lost
// before a replica had them are forgotten when the replica takes over: a
// proof recorded there can be replayed once, inside its window. Keep
// dpop.VerifierConfig.Window short, or require server nonces
// (dpop.VerifierConfig.Nonces), where that matters.
package redisreplay

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/oleg-tkachuk/limes/dpop"
)

// DefaultKeyPrefix starts every key the cache writes when Config names no
// KeyPrefix.
const DefaultKeyPrefix = "limes:dpop:replay:"

// Config configures a Cache.
type Config struct {
	// Client runs the cache's script: a *redis.Client, *redis.ClusterClient,
	// *redis.Ring or any redis.UniversalClient. Required.
	Client redis.Scripter
	// KeyPrefix starts every key the cache writes. Zero: DefaultKeyPrefix.
	KeyPrefix string
	// EntriesPerHolder bounds the live proof ids of one holder. Zero:
	// dpop.DefaultReplayEntriesPerHolder.
	EntriesPerHolder int
	// Now is the clock. Nil: time.Now.
	Now func() time.Time
}

// Cache is a dpop.ReplayCache over Redis. It is safe for concurrent use.
type Cache struct {
	client redis.Scripter
	prefix string
	share  int
	now    func() time.Time
}

var _ dpop.ReplayCache = (*Cache)(nil)

// errNoClient is a Config with no Client.
var errNoClient = errors.New("redisreplay: Config.Client required")

// New builds a Cache, refusing a Config with no Client or a negative share.
func New(cfg Config) (*Cache, error) {
	if cfg.Client == nil {
		return nil, errNoClient
	}
	if cfg.EntriesPerHolder < 0 {
		return nil, fmt.Errorf("redisreplay: replay cache share of one holder %d is negative", cfg.EntriesPerHolder)
	}
	if cfg.KeyPrefix == "" {
		cfg.KeyPrefix = DefaultKeyPrefix
	}
	if cfg.EntriesPerHolder == 0 {
		cfg.EntriesPerHolder = dpop.DefaultReplayEntriesPerHolder
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Cache{client: cfg.Client, prefix: cfg.KeyPrefix, share: cfg.EntriesPerHolder, now: cfg.Now}, nil
}

//go:embed seen.lua
var seenSource string

// seenScript is seen.lua, run by its digest and loaded on the first miss.
var seenScript = redis.NewScript(seenSource)

// seenReplay is what seen.lua returns for an id already recorded, or one
// refused because its holder's share is full.
const seenReplay = 1

// Seen implements dpop.ReplayCache. A holder past its share is refused as a
// replay rather than accepted unrecorded; a Redis that cannot answer returns
// the error, and dpop.Verifier.Verify fails with it: an outage, not a refused
// proof.
func (c *Cache) Seen(ctx context.Context, e dpop.ReplayEntry) (bool, error) {
	now := c.now()
	got, err := seenScript.Run(ctx, c.client,
		[]string{c.key(e.Holder)},
		member(e.JKT, e.JTI), now.UnixMilli(), ceilMilli(e.ExpiresAt), c.share,
	).Int64()
	if err != nil {
		return false, fmt.Errorf("redisreplay: record proof id: %w", err)
	}
	return got == seenReplay, nil
}

// key is the sorted set holding holder's proof ids.
func (c *Cache) key(holder string) string { return c.prefix + holder }

// memberLengthSep ends the length of the thumbprint that starts a member.
const memberLengthSep = ":"

// member is the one string that names id jti of key jkt. The thumbprint's
// length comes first, so no pair of a thumbprint and an id spells another:
// a separator alone could appear in either.
func member(jkt, jti string) string {
	return strconv.Itoa(len(jkt)) + memberLengthSep + jkt + jti
}

// ceilMilli is t in Unix milliseconds, rounded up: an id is remembered for
// at least as long as it was asked to be, never a fraction less.
func ceilMilli(t time.Time) int64 {
	ms := t.UnixMilli()
	if t.Sub(time.UnixMilli(ms)) > 0 {
		ms++
	}
	return ms
}
