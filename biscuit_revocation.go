package limes

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"
	"uuid"

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
	// from the token, which no caller can assemble — and says whether the
	// copy had an entry already. Idempotent: a repeat writes nothing, keeps
	// the first entry and reports it with AlreadyRevoked. Returns
	// ErrNotFound when the capability is not on record — or not visible to
	// the caller — so a caller cannot revoke a copy of a capability it cannot
	// see, and ErrInvalidRequest for a request naming no copy.
	RevokeBiscuit(ctx context.Context, req RevokeBiscuitRequest) (RevokeBiscuitResult, error)

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

// RevokeBiscuitResult is what BiscuitRevocationStore.RevokeBiscuit did to
// the copy it names, as RevokeResult is Store.Revoke's.
type RevokeBiscuitResult struct {
	// AlreadyRevoked reports that the copy had an entry before the call, so
	// the call wrote none.
	AlreadyRevoked bool
	// Revocation is the copy's entry as it stands after the call — the one
	// the call wrote, or the earlier one it kept — as GetBiscuitRevocation
	// reads it back, at the store's precision.
	Revocation BiscuitRevocation
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
	if c.capabilityID == uuid.Nil() || len(c.revocationID) == 0 {
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
// meter.CopyUsageReader reads their counters.
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

// errNoBiscuitRevocations is returned by NewVerifier when Biscuits
// are accepted with nothing to check their copies against.
var errNoBiscuitRevocations = errors.New("limes: VerifierConfig.Biscuit.Revocations required")
