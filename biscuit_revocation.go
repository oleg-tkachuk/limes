package limes

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
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
	IsBiscuitRevoked(ctx context.Context, revocationIDs [][]byte) (bool, error)
}

// BiscuitRevocationStore persists revoked Biscuit copies. It is separate from
// Store so that a consumer without Biscuits implements nothing more.
type BiscuitRevocationStore interface {
	BiscuitRevocationLookup

	// RevokeBiscuit lists one revocation id of a capability's Biscuit.
	// Idempotent. Returns ErrNotFound when the capability is not on record
	// — or not visible to the caller — so a caller cannot revoke a copy of a
	// capability it cannot see, and an ErrInvalidRequest for an empty
	// revocation id.
	RevokeBiscuit(ctx context.Context, req RevokeBiscuitRequest) error

	// GetBiscuitRevocation reads back what RevokeBiscuit wrote for one
	// revocation id. ErrNotFound when the id is not revoked, or belongs to a
	// capability the caller cannot see.
	GetBiscuitRevocation(ctx context.Context, revocationID []byte) (BiscuitRevocation, error)
}

// BiscuitRevocation is one revoked Biscuit copy, as RevokeBiscuit wrote it.
type BiscuitRevocation struct {
	CapabilityID uuid.UUID
	RevocationID []byte
	RevokedAt    time.Time
	Reason       string
	Actor        string
}

// RevokeBiscuitRequest is the input shape for BiscuitRevocationStore.RevokeBiscuit.
type RevokeBiscuitRequest struct {
	// CapabilityID is the capability the Biscuit seals.
	CapabilityID uuid.UUID
	// RevocationID is the id to list: BiscuitCopy.RevocationID.
	RevocationID []byte
	// Reason and Actor are stored for audit, as on RevokeRequest.
	Reason string
	Actor  string
}

// BiscuitCopy identifies one copy of a capability's Biscuit.
type BiscuitCopy struct {
	// CapabilityID is the capability the Biscuit seals.
	CapabilityID uuid.UUID
	// RevocationID is the id of the copy's last block: listing it revokes
	// this copy and every copy attenuated from it.
	RevocationID []byte
	// Limits are the request and budget limits in force on the copy — its
	// own and those of the copies it was attenuated from — innermost first;
	// CopyUsageReader reads their counters.
	Limits []CopyCeiling
}

// BiscuitCopy reads which copy token is, after checking it as Verify does —
// the sealed token's signature, issuer and tenant, then the Biscuit's
// signature chain — so that a revocation is only ever recorded against a
// capability the token genuinely carries. Expiry, audience and revocation are
// not checked: a copy can be revoked whatever its state. Nor is MeterCopies:
// a copy carrying limits is named, and can be revoked, on any verifier.
func (v *StandardVerifier) BiscuitCopy(ctx context.Context, token string) (BiscuitCopy, error) {
	if !IsBiscuit(token) {
		return BiscuitCopy{}, fmt.Errorf("%w: not a Biscuit token", ErrInvalidSignature)
	}
	if len(token) > v.cfg.MaxTokenBytes {
		return BiscuitCopy{}, fmt.Errorf("%w: token is %d bytes (limit %d)",
			ErrInvalidSignature, len(token), v.cfg.MaxTokenBytes)
	}
	c, ids, err := v.verifyBiscuit(ctx, token, true)
	if err != nil {
		return BiscuitCopy{}, err
	}
	return BiscuitCopy{CapabilityID: c.ID, RevocationID: ids[len(ids)-1], Limits: c.Copies}, nil
}

// CachedBiscuitRevocationChecker caches IsBiscuitRevoked answers per token —
// keyed by its whole list of revocation ids — under the same TTL, bound and
// sharing of concurrent misses as CachedRevocationChecker, and is cleared the
// same way. It has no Invalidate: a revoked id is part of the key of every
// token carrying it, so only Clear reaches them all.
type CachedBiscuitRevocationChecker struct {
	upstream BiscuitRevocationLookup
	cache    *ttlCache[[sha256.Size]byte]
}

// NewCachedBiscuitRevocationChecker wires a cache over an upstream lookup.
// ttl and the options apply as they do to NewCachedRevocationChecker.
func NewCachedBiscuitRevocationChecker(upstream BiscuitRevocationLookup, ttl time.Duration, opts ...CacheOption) *CachedBiscuitRevocationChecker {
	return &CachedBiscuitRevocationChecker{
		upstream: upstream,
		cache:    newTTLCache[[sha256.Size]byte](newCacheConfig(ttl, opts)),
	}
}

// IsBiscuitRevoked serves a fresh cached answer, or asks upstream once on
// behalf of every concurrent caller asking about the same token. An upstream
// error is returned and never cached.
func (c *CachedBiscuitRevocationChecker) IsBiscuitRevoked(ctx context.Context, revocationIDs [][]byte) (bool, error) {
	return c.cache.get(ctx, biscuitCacheKey(revocationIDs), func(ctx context.Context) (bool, error) {
		return c.upstream.IsBiscuitRevoked(ctx, revocationIDs)
	})
}

// Clear drops every entry; wire it to the same revocation notification as
// CachedRevocationChecker.Clear.
func (c *CachedBiscuitRevocationChecker) Clear() { c.cache.clear() }

// Sweep drops every expired entry and returns how many it dropped.
func (c *CachedBiscuitRevocationChecker) Sweep() int { return c.cache.sweep() }

// biscuitCacheKey hashes the ids length-prefixed, so no two lists share a key.
func biscuitCacheKey(ids [][]byte) [sha256.Size]byte {
	h := sha256.New()
	for _, id := range ids {
		h.Write(binary.BigEndian.AppendUint64(nil, uint64(len(id))))
		h.Write(id)
	}
	var out [sha256.Size]byte
	copy(out[:], h.Sum(nil))
	return out
}

// errNoBiscuitRevocations is returned by NewStandardVerifier when Biscuits
// are accepted with nothing to check their copies against.
var errNoBiscuitRevocations = errors.New("limes: VerifierConfig.BiscuitRevocations required when AcceptBiscuit is set")
