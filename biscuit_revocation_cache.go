package limes

import (
	"context"
	"errors"
	"strconv"
	"strings"
)

// errNoBiscuitLookup is the answer of a RevocationCache built without
// RevocationCacheConfig.Biscuits to a Biscuit question: no answer, so the
// copy is refused rather than taken as live.
var errNoBiscuitLookup = errors.New("limes: RevocationCacheConfig.Biscuits is not set; the cache answers no Biscuit question")

// answersBiscuits reports whether the cache has an upstream for Biscuit
// questions.
func (c *RevocationCache) answersBiscuits() bool { return c.biscuits != nil }

// IsBiscuitRevoked answers from the cache for each id that has a fresh
// answer, and asks upstream about all the others in one lookup, shared by
// every concurrent caller asking about the same ones. A token is revoked
// when any of its ids is: the ids a token shares with the copies it was
// attenuated from stay cached, so a fresh attenuation — which anyone holding
// a copy can make offline — costs one lookup for its new block, and a token
// of blocks the cache has never seen one lookup in all, not one per block. A "live" answer is then
// cached for each of them; a "revoked" one for that set alone, as upstream
// does not say which id it is. An upstream error is returned and never
// cached.
func (c *RevocationCache) IsBiscuitRevoked(ctx context.Context, revocationIDs [][]byte) (bool, error) {
	if !c.answersBiscuits() {
		return false, errNoBiscuitLookup
	}
	keys := make([]string, len(revocationIDs))
	for i, id := range revocationIDs {
		keys[i] = idKey(id)
	}
	revoked, missing, epoch := c.copies.Cached(keys)
	if revoked || len(missing) == 0 {
		return revoked, nil
	}
	return c.lookupBiscuitMisses(ctx, missing, epoch)
}

// lookupBiscuitMisses asks the upstream about the revocation ids the cache
// does not hold, keyed missing, as one question: a single id under its own
// key, several under the key of their set. A set found live seeds each id
// live as of epoch.
func (c *RevocationCache) lookupBiscuitMisses(ctx context.Context, missing []string, epoch uint64) (bool, error) {
	ids := make([][]byte, len(missing))
	for i, k := range missing {
		ids[i] = []byte(k[len(idKeyPrefix):])
	}
	key := missing[0]
	if len(missing) > 1 {
		key = setKey(ids)
	}
	revoked, at, err := c.copies.Get(ctx, key, func(ctx context.Context) (bool, error) {
		return c.biscuits.IsBiscuitRevoked(ctx, ids)
	})
	if err == nil && !revoked && len(missing) > 1 {
		c.copies.SeedLive(missing, epoch, at)
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
