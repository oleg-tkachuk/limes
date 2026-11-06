package limes

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
)

// errNoBiscuitLookup is the answer of a RevocationCache built without
// RevocationCacheConfig.Biscuits to a Biscuit question: no answer, so the
// copy is refused rather than taken as live.
var errNoBiscuitLookup = invalidConfig("limes: RevocationCacheConfig.Biscuits is not set; the cache answers no Biscuit question")

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
// cached for each of them; a "revoked" one for that set and for the token's
// own ids, as upstream does not say which id it is, and it holds for every
// copy attenuated from the token. An upstream error is returned and never
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
	if !revoked && len(missing) > 0 {
		var err error
		if revoked, err = c.lookupBiscuitMisses(ctx, missing, epoch); err != nil {
			return revoked, err
		}
		// Revoked through the ids asked upstream, part of the copy's: the
		// copy is revoked as a whole, kept under its own key.
		if revoked && len(missing) < len(keys) && len(keys) > 1 {
			c.copies.SeedRevoked(setKey(revocationIDs), epoch)
		}
	}
	if !revoked && c.heldRevokedCopy(revocationIDs) {
		return true, nil
	}
	return revoked, nil
}

// heldRevokedCopy reports whether the cache holds a revoked answer about
// this copy, or a copy it was attenuated from: their ids, authority first,
// are a prefix of its. No revocation the cache learned is undone by a live
// answer about the ids it asked since, fresh or stale. Every prefix's key
// comes of one pass over the ids (prefixKeys).
func (c *RevocationCache) heldRevokedCopy(revocationIDs [][]byte) bool {
	return len(revocationIDs) > 1 && c.copies.HeldRevoked(prefixKeys(revocationIDs))
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
	// A set's answer covers its ids: tagged with them, a stale live answer
	// yields to one held revoked and fails while one let go to make room
	// would still be kept (ttlcache.staleOr), as a delegated capability's.
	var tags []string
	if len(missing) > 1 {
		key, tags = setKey(ids), missing
	}
	revoked, at, err := c.copies.GetTagged(ctx, key, func(ctx context.Context) (bool, []string, error) {
		revoked, err := c.biscuits.IsBiscuitRevoked(ctx, ids)
		return revoked, tags, err
	})
	// A live answer about the set, stale over a failure, does not undo the
	// revocation of one of its ids that the cache read since.
	if err == nil && !revoked && len(missing) > 1 && c.copies.HeldRevoked(missing) {
		return true, nil
	}
	if err == nil && !revoked && len(missing) > 1 {
		c.copies.SeedLive(missing, epoch, at)
	}
	return revoked, err
}

// The cache holds one revocation id under idKeyPrefix and a set of them,
// asked about together, under setKeyPrefix and the set's digest: no id's
// key spells a set's, and two sets spell one key only where SHA-256
// collides. The digest is a chain, each id hashed with its length onto the
// digest of the ids before it, so a key is the same size however many ids
// its set holds, and the keys of every prefix of a copy's ids come of one
// pass over them.
const (
	idKeyPrefix  = "i"
	setKeyPrefix = "s"
	// setKeySize is the length of a set's key: its prefix and digest.
	setKeySize = len(setKeyPrefix) + sha256.Size
	// lengthSize is how many bytes an id's length is hashed as.
	lengthSize = 8
)

func idKey(id []byte) string { return idKeyPrefix + string(id) }

// setKey is the key of the set ids.
func setKey(ids [][]byte) string {
	var digest [sha256.Size]byte
	for _, id := range ids {
		digest = chainDigest(digest, id)
	}
	return setKeyPrefix + string(digest[:])
}

// prefixKeys is the key of every prefix of ids two ids long or more, the
// shortest first, laid out in one string they share.
func prefixKeys(ids [][]byte) []string {
	if len(ids) < 2 {
		return nil
	}
	buf := make([]byte, 0, (len(ids)-1)*setKeySize)
	digest := chainDigest([sha256.Size]byte{}, ids[0])
	for _, id := range ids[1:] {
		digest = chainDigest(digest, id)
		buf = append(buf, setKeyPrefix...)
		buf = append(buf, digest[:]...)
	}
	all := string(buf)
	keys := make([]string, len(ids)-1)
	for i := range keys {
		keys[i] = all[i*setKeySize : (i+1)*setKeySize]
	}
	return keys
}

// chainDigest is the digest of a set of ids whose ids before id have the
// digest prev.
func chainDigest(prev [sha256.Size]byte, id []byte) [sha256.Size]byte {
	var length [lengthSize]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(id)))
	h := sha256.New()
	_, _ = h.Write(prev[:])
	_, _ = h.Write(length[:])
	_, _ = h.Write(id)
	var digest [sha256.Size]byte
	h.Sum(digest[:0])
	return digest
}
