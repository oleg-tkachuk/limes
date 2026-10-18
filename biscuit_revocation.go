package limes

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes/internal/link"
)

// Revoking one copy of a Biscuit.
//
// Every block of a Biscuit carries a revocation id — its signature, which
// biscuit-go reports authority first. A token stops verifying when any of
// its ids is revoked, so revoking a block's id revokes the copy that ends
// with it and every copy attenuated from that one, and nothing above it:
// the copy it was attenuated from, its siblings, and the capability's JWT
// keep working. Revoking the authority block's id — the last id of an
// unattenuated Biscuit — revokes that Biscuit and every copy attenuated from
// it, but not the capability's JWT, nor another Biscuit minted for the same
// capability (each has its own root). Revoking the capability itself
// (Store.Revoke) revokes them all.

// BiscuitRevocationLookup answers whether any of a Biscuit's revocation ids
// is revoked. The ids it is asked about come from a Biscuit whose signature
// chain has already verified.
type BiscuitRevocationLookup interface {
	// IsBiscuitRevoked reports whether any of revocationIDs — the blocks
	// of one copy, authority first — has been revoked.
	IsBiscuitRevoked(ctx context.Context, revocationIDs [][]byte) (bool, error)
}

// BiscuitRevocationStore persists revoked Biscuit copies. It is separate from
// Store so that a consumer without Biscuits implements nothing more.
type BiscuitRevocationStore interface {
	BiscuitRevocationLookup

	// RevokeBiscuit lists the revocation id of one Biscuit copy, under the
	// capability the copy belongs to — the pair Verifier.BiscuitCopy read
	// from the token, which no caller can assemble. Idempotent. Returns
	// ErrNotFound when the capability is not on record — or not visible to
	// the caller — so a caller cannot revoke a copy of a capability it cannot
	// see, and ErrInvalidRequest for a request naming no copy.
	RevokeBiscuit(ctx context.Context, req RevokeBiscuitRequest) error

	// GetBiscuitRevocation reads back what RevokeBiscuit wrote for one copy,
	// under the capability it was revoked under. ErrNotFound when the copy is
	// not revoked under that capability, or the capability is one the caller
	// cannot see; ErrInvalidRequest for the zero BiscuitCopy.
	GetBiscuitRevocation(ctx context.Context, copy BiscuitCopy) (BiscuitRevocation, error)
}

// BiscuitRevocation is one revoked Biscuit copy, as RevokeBiscuit wrote it.
type BiscuitRevocation struct {
	CapabilityID uuid.UUID
	RevocationID []byte
	RevokedAt    time.Time
	Note         string
	Actor        string
}

// RevokeBiscuitRequest is the input shape for BiscuitRevocationStore.RevokeBiscuit.
type RevokeBiscuitRequest struct {
	// Copy is the copy to revoke, as Verifier.BiscuitCopy read it.
	Copy BiscuitCopy
	// Note and Actor are stored for audit, as on RevokeRequest.
	Note  string
	Actor string
}

// Validate refuses a request naming no copy as ErrInvalidRequest.
func (r RevokeBiscuitRequest) Validate() error {
	if err := r.Copy.Validate(); err != nil {
		return fmt.Errorf("revoke biscuit: %w", err)
	}
	return nil
}

// BiscuitCopy identifies one copy of a capability's Biscuit. Only
// Verifier.BiscuitCopy makes one, from a token whose signature chain it
// checked, so the revocation id and the capability it is revoked under always
// belong together: a caller cannot name another tenant's copy under its own
// capability. The zero BiscuitCopy names nothing.
type BiscuitCopy struct {
	capabilityID uuid.UUID
	revocationID []byte
	copies       []CopyCeiling
}

// Validate refuses the zero BiscuitCopy, which names no copy, as
// ErrInvalidRequest.
func (c BiscuitCopy) Validate() error {
	if c.capabilityID == uuid.Nil || len(c.revocationID) == 0 {
		return invalidRequest("no biscuit copy; read one with Verifier.BiscuitCopy")
	}
	return nil
}

// CapabilityID is the capability the Biscuit seals.
func (c BiscuitCopy) CapabilityID() uuid.UUID { return c.capabilityID }

// RevocationID is the id of the copy's last block: listing it revokes this
// copy and every copy attenuated from it.
func (c BiscuitCopy) RevocationID() []byte { return slices.Clone(c.revocationID) }

// Copies are the request and budget limits in force on the copy — its own
// and those of the copies it was attenuated from — innermost first;
// CopyUsageReader reads their counters.
func (c BiscuitCopy) Copies() []CopyCeiling { return cloneCopyCeilings(c.copies) }

// BiscuitCopy reads which copy token is, after checking it as Verify does —
// the sealed token's signature, issuer and tenant, then the Biscuit's
// signature chain — so that a revocation is only ever recorded against a
// capability the token genuinely carries. Expiry, audience and revocation are
// not checked: a copy can be revoked whatever its state. Nor is MeterCopies:
// a copy carrying limits is named, and can be revoked, on any verifier.
func (v *Verifier) BiscuitCopy(ctx context.Context, token string) (BiscuitCopy, error) {
	if !isBiscuit(token) {
		return BiscuitCopy{}, fmt.Errorf("%w: not a Biscuit token", ErrInvalidToken)
	}
	if len(token) > v.cfg.MaxTokenBytes {
		return BiscuitCopy{}, fmt.Errorf("%w: token is %d bytes (limit %d)",
			ErrInvalidToken, len(token), v.cfg.MaxTokenBytes)
	}
	c, ids, err := v.verifyBiscuit(ctx, token)
	if err != nil {
		return BiscuitCopy{}, err
	}
	return BiscuitCopy{capabilityID: c.ID, revocationID: slices.Clone(ids[len(ids)-1]), copies: cloneCopyCeilings(c.copies)}, nil
}

func init() {
	link.NewBiscuitCopy = func(capabilityID uuid.UUID, revocationID []byte) any {
		return BiscuitCopy{capabilityID: capabilityID, revocationID: slices.Clone(revocationID)}
	}
}

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
	cache    *ttlCache[string]
}

// BiscuitRevocationCacheConfig configures a BiscuitRevocationCache;
// its fields read as RevocationCacheConfig's do, MaxEntries counting
// revocation ids.
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
	return &BiscuitRevocationCache{upstream: cfg.Upstream, cache: newTTLCache[string](c)}, nil
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
	revoked, missing, epoch := c.cache.cached(keys)
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
	revoked, expires, err := c.cache.get(ctx, key, func(ctx context.Context) (bool, error) {
		return c.upstream.IsBiscuitRevoked(ctx, ids)
	})
	if err == nil && !revoked && len(missing) > 1 {
		c.cache.seedLive(missing, epoch, expires)
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
func (c *BiscuitRevocationCache) Clear() { c.cache.clear() }

// Sweep drops every expired entry and returns how many it dropped.
func (c *BiscuitRevocationCache) Sweep() int { return c.cache.sweep() }

// errNoBiscuitRevocations is returned by NewVerifier when Biscuits
// are accepted with nothing to check their copies against.
var errNoBiscuitRevocations = errors.New("limes: VerifierConfig.Biscuit.Revocations required")
