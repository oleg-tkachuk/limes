package limes

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/oleg-tkachuk/limes/internal/ttlcache"
)

// BiscuitRevocationCache caches IsBiscuitRevoked answers per
// revocation id, under the same TTL, bound and sharing of concurrent misses
// as RevocationCache. A token is revoked when any of its ids is: the
// ids a token shares with the copies it was attenuated from stay cached, and
// the ones the cache does not hold are asked about together, in one upstream
// lookup. A fresh attenuation — which anyone holding a copy can make offline
// — so costs one lookup for its new block, and a token of blocks the cache
// has never seen one lookup in all, not one per block.
//
// A revoked id's cached "live" answer lasts until the TTL, as in
// RevocationCache; Clear drops every entry at once.
type BiscuitRevocationCache struct {
	upstream BiscuitRevocationLookup
	cache    *ttlcache.Cache[string]
}

// BiscuitRevocationCacheConfig configures a BiscuitRevocationCache;
// its fields read as RevocationCacheConfig's do, MaxEntries counting
// entries: one per revocation id, and one per set of ids asked about
// together, whose key holds every id of the set — up to MaxBiscuitBlocks+1
// block signatures. Size memory for MaxEntries times the largest such key,
// not one id.
type BiscuitRevocationCacheConfig struct {
	// Upstream answers what the cache does not hold. Required.
	Upstream   BiscuitRevocationLookup
	TTL        time.Duration
	MaxEntries int
	Now        func() time.Time
}

// NewBiscuitRevocationCache wires a cache over cfg.Upstream, as
// NewRevocationCache does.
func NewBiscuitRevocationCache(cfg BiscuitRevocationCacheConfig) (*BiscuitRevocationCache, error) {
	if cfg.Upstream == nil {
		return nil, errors.New("limes: BiscuitRevocationCacheConfig.Upstream required")
	}
	c, err := newCacheConfig(cfg.TTL, cfg.MaxEntries, cfg.Now)
	if err != nil {
		return nil, err
	}
	return &BiscuitRevocationCache{upstream: cfg.Upstream, cache: ttlcache.New[string](c)}, nil
}

// IsBiscuitRevoked answers from the cache for each id that has a fresh
// answer, and asks upstream about all the others in one lookup, shared by
// every concurrent caller asking about the same ones. A "live" answer is then
// cached for each of them; a "revoked" one for that set alone, as upstream
// does not say which id it is. An upstream error is returned and never
// cached.
func (c *BiscuitRevocationCache) IsBiscuitRevoked(ctx context.Context, revocationIDs [][]byte) (bool, error) {
	keys := make([]string, len(revocationIDs))
	for i, id := range revocationIDs {
		keys[i] = idKey(id)
	}
	revoked, missing, epoch := c.cache.Cached(keys)
	if revoked || len(missing) == 0 {
		return revoked, nil
	}
	ids := make([][]byte, len(missing))
	for i, k := range missing {
		ids[i] = []byte(k[len(idKeyPrefix):])
	}
	key := missing[0]
	if len(missing) > 1 {
		key = setKey(ids)
	}
	revoked, expires, err := c.cache.Get(ctx, key, func(ctx context.Context) (bool, error) {
		return c.upstream.IsBiscuitRevoked(ctx, ids)
	})
	if err == nil && !revoked && len(missing) > 1 {
		c.cache.SeedLive(missing, epoch, expires)
	}
	return revoked, err
}

// The cache holds one revocation id under idKeyPrefix and a set of them,
// asked about together, under setKeyPrefix, each id of a set preceded by its
// length: no id's key spells a set's, and no two sets spell one key.
const (
	idKeyPrefix  = "i"
	setKeyPrefix = "s"
)

func idKey(id []byte) string { return idKeyPrefix + string(id) }

func setKey(ids [][]byte) string {
	var b strings.Builder
	b.WriteString(setKeyPrefix)
	for _, id := range ids {
		b.WriteString(strconv.Itoa(len(id)))
		b.WriteByte(':')
		b.Write(id)
	}
	return b.String()
}

// Clear drops every entry; wire it to the same revocation notification as
// RevocationCache.Clear.
func (c *BiscuitRevocationCache) Clear() { c.cache.Clear() }

// Sweep drops every expired entry and returns how many it dropped.
func (c *BiscuitRevocationCache) Sweep() int { return c.cache.Sweep() }
