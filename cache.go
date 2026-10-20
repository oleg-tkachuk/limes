package limes

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes/internal/ttlcache"
)

// RevocationLookup is the seam the cache wraps. A persistent store
// implements it; tests substitute a stub. Like Store.IsRevoked, it answers
// for the capability's whole delegation chain.
type RevocationLookup interface {
	// IsRevoked reports whether id, or any ancestor of it, is revoked. An
	// error is no answer: the verifier refuses rather than assume not.
	IsRevoked(ctx context.Context, id uuid.UUID) (bool, error)
}

// DefaultRevocationCacheTTL bounds how long a cached answer is served. A
// revocation therefore takes effect within this window on every verifier
// that did not perform it.
const DefaultRevocationCacheTTL = 2 * time.Second

// DefaultRevocationCacheEntries bounds the cache. Past it, the oldest entry
// makes room for the new one: a flood of distinct ids costs extra lookups,
// never unbounded memory, and never the answers cached for everyone else.
const DefaultRevocationCacheEntries = 100_000

// RevocationCache is a small in-memory cache for revocation answers: of a
// capability, IsRevoked, and of a Biscuit copy, IsBiscuitRevoked. One cache
// serves both, so one Clear, wired to a revocation notification, reaches
// every answer a verifier holds.
//
// A cached answer about a capability is also an answer about its ancestors,
// so revoking a parent reaches a child's cached "live" entry only when that
// entry expires — within the TTL, like any other revocation seen from
// another process — or when Clear drops it. There is no per-id drop: the
// cache does not know which entries are a revoked id's descendants.
//
// Concurrent misses for the same question share one upstream call.
type RevocationCache struct {
	capabilities RevocationLookup
	// biscuits is nil for a cache that answers no Biscuit question.
	biscuits BiscuitRevocationLookup
	caps     *ttlcache.Cache[uuid.UUID]
	copies   *ttlcache.Cache[string]
}

// RevocationCacheConfig configures a RevocationCache.
type RevocationCacheConfig struct {
	// Capabilities answers IsRevoked for what the cache does not hold.
	// Required.
	Capabilities RevocationLookup
	// Biscuits answers IsBiscuitRevoked for what the cache does not hold.
	// Optional: without it the cache refuses every Biscuit question, and
	// NewVerifier refuses it as BiscuitConfig.Revocations. A store that
	// implements both, as pgstore's does, is passed as both.
	Biscuits BiscuitRevocationLookup
	// TTL is how long an answer is served. Zero: DefaultRevocationCacheTTL.
	TTL time.Duration
	// MaxEntries bounds each kind of answer on its own, so a flood of one
	// never evicts the other. Zero: DefaultRevocationCacheEntries. A Biscuit
	// answer is one entry per revocation id, and one per set of ids asked
	// about together, whose key holds every id of the set — up to
	// MaxBiscuitBlocks+1 block signatures: size memory for MaxEntries times
	// the largest such key, not one id.
	MaxEntries int
	// Now is the clock. Nil: time.Now.
	Now func() time.Time
}

// NewRevocationCache wires a cache over cfg's upstreams. A negative TTL or
// bound is refused; a deployment that wants no cache passes its store to the
// verifier directly.
func NewRevocationCache(cfg RevocationCacheConfig) (*RevocationCache, error) {
	if cfg.Capabilities == nil {
		return nil, errors.New("limes: RevocationCacheConfig.Capabilities required")
	}
	c, err := newCacheConfig(cfg.TTL, cfg.MaxEntries, cfg.Now)
	if err != nil {
		return nil, err
	}
	return &RevocationCache{
		capabilities: cfg.Capabilities,
		biscuits:     cfg.Biscuits,
		caps:         ttlcache.New[uuid.UUID](c),
		copies:       ttlcache.New[string](c),
	}, nil
}

// newCacheConfig resolves a configuration's zero values to the defaults and
// refuses the negative ones.
func newCacheConfig(ttl time.Duration, maxEntries int, now func() time.Time) (ttlcache.Config, error) {
	if ttl < 0 {
		return ttlcache.Config{}, fmt.Errorf("limes: cache TTL %s is negative", ttl)
	}
	if maxEntries < 0 {
		return ttlcache.Config{}, fmt.Errorf("limes: cache bound %d is negative", maxEntries)
	}
	if ttl == 0 {
		ttl = DefaultRevocationCacheTTL
	}
	if maxEntries == 0 {
		maxEntries = DefaultRevocationCacheEntries
	}
	if now == nil {
		now = time.Now
	}
	return ttlcache.Config{TTL: ttl, MaxEntries: maxEntries, Now: now}, nil
}

// IsRevoked serves a fresh cached answer, or asks upstream once on behalf of
// every concurrent caller asking about the same id. An upstream error is
// returned to all of them and is never cached.
func (c *RevocationCache) IsRevoked(ctx context.Context, id uuid.UUID) (bool, error) {
	revoked, _, err := c.caps.Get(ctx, id, func(ctx context.Context) (bool, error) { return c.capabilities.IsRevoked(ctx, id) })
	return revoked, err
}

// Clear drops every entry. A consumer that learns of a revocation from
// elsewhere — a database notification, a message bus — calls it so the next
// check goes upstream at once instead of waiting out the TTL. It drops
// everything, not only the revoked id, because an answer cached for a
// descendant is also an answer about the revoked ancestor.
func (c *RevocationCache) Clear() {
	c.caps.Clear()
	c.copies.Clear()
}

// Sweep drops every expired entry, of either kind, and returns how many it
// dropped. Optional housekeeping — lookups skip stale entries, and every new
// answer drops the expired ones before it.
func (c *RevocationCache) Sweep() int { return c.caps.Sweep() + c.copies.Sweep() }
