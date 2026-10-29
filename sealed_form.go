package limes

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"uuid"

	"github.com/oleg-tkachuk/limes/internal/jose"
)

// TokenForm is a form of capability token that seals a JWT this issuer
// signed inside a token its holder can narrow offline: the Biscuit form
// (IssuerConfig.Biscuit, BiscuitConfig.Form). The form reads and writes its
// encoding and verifies its signatures; the core keeps what a narrowing may
// do. Every block a form hands back is checked as the holder's input
// (Attenuation, Narrows), whatever form decoded it.
//
// A form's error refuses the token as ErrBiscuitAttenuation when it wraps
// that sentinel, and as ErrInvalidToken otherwise. The form need not wrap
// either: an error that does keeps its text, one that does not gets the
// family's prefixed, and the family is named once either way.
type TokenForm interface {
	// Seal builds a token around the JWT sign returns for the root key the
	// form chose, whose private half signs the token's first block and is
	// then dropped.
	Seal(ctx context.Context, sign func(ctx context.Context, root ed25519.PublicKey) (string, error)) (string, error)

	// Open splits token into the JWT it seals and unseal, which, once the
	// caller has verified that JWT and hands it the root key the JWT names,
	// verifies the rest of token under it and returns what it read. Nothing
	// of token is to be trusted before unseal returns.
	Open(token string) (sealed string, unseal func(root ed25519.PublicKey) (Unsealed, error), err error)
}

// Unsealed is what a TokenForm read of a token past its sealed JWT, its
// signature chain verified.
type Unsealed struct {
	// Blocks are the token's attenuations, in the order they were appended.
	Blocks []Attenuation
	// RevocationIDs are the token's revocation ids: its first block's, then
	// one for each of Blocks.
	RevocationIDs [][]byte
}

// ErrCopyCountersNotMetered — a Biscuit sets a copy's own request or budget
// limit, and the verifier was not told that its Meter counts copies. The
// limit would go unenforced, so the token is refused instead. The token is
// not at fault — a verifier with MeterCopies accepts it — so this is the
// server's configuration, outside ErrInvalidToken: ErrorResponse answers it
// as an internal error, and it has no Reason.
var ErrCopyCountersNotMetered = errors.New("limes: copy limits need a verifier with MeterCopies")

// ErrBiscuitAttenuation — an attenuation block the verifier cannot honour:
// outside the vocabulary, malformed, or widening its parent. Matches
// ErrInvalidToken: the token is not one this verifier accepts.
var ErrBiscuitAttenuation error = &familyError{"limes: invalid Biscuit attenuation", ErrInvalidToken}

// isBiscuit reports whether token is in the Biscuit form rather than a JWT:
// a JWT has exactly two dots, base64url has none.
func isBiscuit(token string) bool {
	return token != "" && !strings.Contains(token, ".")
}

// Biscuit returns the Biscuit form of a capability this issuer issued: the
// same capability — same ID, caveats, expiry and binding, revoked together
// with it — in a token its holder can attenuate offline. One copy can also be
// revoked on its own; see BiscuitRevocationStore.
//
// Like Delegate it takes a VerifiedCapability, so what it signs is what this
// issuer signed and recorded before, never a value a caller assembled or
// widened. It refuses one another issuer named, one read from a Biscuit —
// wrapping a copy again would shed its attenuation and its revocation id;
// its holder attenuates it instead — and one that has expired or that the
// store reports revoked.
//
// It needs no OpShare: a copy attenuated from the form allows no more than
// the capability, which its bearer could hand on as it is, and a copy that
// sets limits of its own is counted against Caveats.Copies. Delegate, which
// has the issuer mint a new principal, still requires OpShare.
func (i *Issuer) Biscuit(ctx context.Context, req BiscuitRequest) (BiscuitResult, error) {
	token, err := i.biscuit(ctx, req.Capability)
	if err != nil {
		return BiscuitResult{}, err
	}
	return BiscuitResult{Token: token}, nil
}

// BiscuitRequest is the input shape for Issuer.Biscuit.
type BiscuitRequest struct {
	// Capability is the capability to give a Biscuit form, as the verifier
	// accepted it.
	Capability VerifiedCapability
}

// BiscuitResult is what Issuer.Biscuit mints.
type BiscuitResult struct {
	// Token is the Biscuit, base64url.
	Token string
	_     noCompare
}

// biscuit is Biscuit, returning the token alone.
func (i *Issuer) biscuit(ctx context.Context, vc VerifiedCapability) (string, error) {
	if i.form == nil {
		return "", errNoIssuerForm
	}
	if err := i.checkSealable(ctx, vc); err != nil {
		return "", err
	}
	out, err := i.seal(ctx, vc.c)
	if err != nil {
		return "", err
	}
	if err := i.fits(out); err != nil {
		return "", err
	}
	return out, nil
}

// checkSealable refuses a capability this issuer gives no Biscuit form: one
// the verifier did not accept, one already read from a Biscuit, another
// issuer's, or one expired or revoked.
func (i *Issuer) checkSealable(ctx context.Context, vc VerifiedCapability) error {
	c := vc.c
	switch {
	case c.ID == uuid.Nil():
		return invalidRequest("Biscuit requires a capability the verifier accepted")
	case vc.fromBiscuit:
		return invalidRequest("capability %s was read from a Biscuit; attenuate that instead", c.ID)
	case c.Issuer != i.issuerName:
		return invalidRequest("capability %s was issued by %q, not %q", c.ID, c.Issuer, i.issuerName)
	}
	return i.checkLive(ctx, c, i.clock(), "biscuit of")
}

// errNoIssuerForm is Issuer.Biscuit on an issuer given no form to seal in.
var errNoIssuerForm = invalidConfig("limes: IssuerConfig.Biscuit required: github.com/oleg-tkachuk/limes/biscuit's Form")

// seal is c's sealed form: the form chooses the root key its token's
// signature chain starts from, and the issuer signs c naming it.
func (i *Issuer) seal(ctx context.Context, c Capability) (string, error) {
	sign := func(ctx context.Context, root ed25519.PublicKey) (string, error) {
		if len(root) != ed25519.PublicKeySize {
			return "", broken("limes: a sealed form's root key is %d bytes, not %d", len(root), ed25519.PublicKeySize)
		}
		inner, err := i.signer.signGrant(ctx, grant{Capability: c, biscuitRoot: base64.RawURLEncoding.EncodeToString(root)})
		if err != nil {
			return "", fmt.Errorf("limes: sign sealed token: %w", err)
		}
		return inner, nil
	}
	return i.form.Seal(ctx, sign)
}

// Attenuation is one offline narrowing. Empty fields leave that dimension as
// it is; set ones replace it and must be within what the token already
// allows, or the verifier refuses the whole token.
type Attenuation struct {
	// Ops the token keeps.
	Ops []Op
	// ResourcePrefixes and ResourceURIs the token keeps. Setting either
	// replaces both.
	ResourcePrefixes []string
	ResourceURIs     []string
	// Audience the token keeps.
	Audience []string
	// ExpiresAt, when earlier than the token's, becomes its expiry.
	ExpiresAt time.Time
	// ConfirmationJKT binds the token to a key (dpop.Thumbprint). Only an
	// unbound token can be bound offline: a bound one stays bound to its key,
	// because rebinding needs no key and anyone could do it.
	ConfirmationJKT string
	// Requests and Budget give this copy limits of its own, counted apart
	// from its siblings' and within the capability's: requests made with
	// it, and its spend in the capability's unit, which a block does not
	// carry — so Budget names no UnitCode, and the verifier gives it the
	// capability's. nil sets none; a ceiling of 0 freezes the copy. Each
	// must be within every limit already in force, and an Unlimited one is
	// refused: a copy only narrows.
	Requests *RequestCeiling
	Budget   *BudgetCeiling
}

// Validate refuses an attenuation no block can carry: a binding that is no
// thumbprint, or a copy limit that is not a bound. The verifier refuses
// such a block whatever form carried it.
func (a Attenuation) Validate() error {
	if a.ConfirmationJKT != "" {
		if err := validateThumbprint(a.ConfirmationJKT); err != nil {
			return err
		}
	}
	return checkCopyCeilings(a.Requests, a.Budget)
}

// checkCopyCeilings refuses a copy limit that is not a bound within range:
// a copy only narrows. A budget names no unit: the copy counts in its
// capability's.
func checkCopyCeilings(r *RequestCeiling, b *BudgetCeiling) error {
	if r != nil && (r.Unlimited || r.Validate() != nil) {
		return invalidRequest("a copy's request ceiling is a bound within 0..%d", int64(MaxIJSONInteger))
	}
	if b != nil && (b.Unlimited || b.Max.Validate() != nil || b.UnitCode != "") {
		return invalidRequest("a copy's budget ceiling is a bound within 0..%s, in its capability's unit, which it does not name", MaxNanos)
	}
	return nil
}

// applyAttenuation folds one block, a, into cur, refusing anything it
// cannot enforce and anything that would widen cur. id is the block's
// revocation id, which keys the counters of the limits it sets. a comes from
// the token's holder: it is checked here as Attenuate checks it, whatever
// form decoded it.
func applyAttenuation(cur grant, a Attenuation, id []byte) (grant, error) {
	if err := a.Validate(); err != nil {
		return cur, err
	}
	if a.ConfirmationJKT != "" && cur.ConfirmationJKT != "" && a.ConfirmationJKT != cur.ConfirmationJKT {
		return cur, errors.New("a key-bound token cannot be rebound offline")
	}
	next := grant{Capability: a.narrow(cur.Capability), copies: cur.copies}
	if err := Narrows(cur.Capability, next.Capability); err != nil {
		return cur, err
	}
	if a.Requests == nil && a.Budget == nil {
		return next, nil
	}
	ceiling := CopyCeiling{RevocationID: id, Requests: cloneCeiling(a.Requests), Budget: cloneCeiling(a.Budget)}
	// A copy spends in its capability's unit, which the block leaves out.
	if ceiling.Budget != nil {
		ceiling.Budget.UnitCode = cur.Caveats.Budget.UnitCode
	}
	// An axis the block left alone has no ceiling of this copy's own.
	if err := copyCeilingNarrows(cur, ceiling); err != nil {
		return cur, err
	}
	next.copies = append([]CopyCeiling{ceiling}, cur.copies...)
	return next, nil
}

// cloneCeiling is a copy of *c, or nil.
func cloneCeiling[C any](c *C) *C {
	if c == nil {
		return nil
	}
	v := *c
	return &v
}

// narrow is cur with a applied. Whether the result is in fact narrower is
// for Narrows to decide.
func (a Attenuation) narrow(cur Capability) Capability {
	next := cur
	next.Caveats.Ops = slices.Clone(cur.Caveats.Ops)
	if len(a.Ops) > 0 {
		next.Caveats.Ops = slices.Clone(a.Ops)
	}
	// A block's resources replace every resource caveat, name prefixes
	// included: kept beside the block's, they would widen the copy.
	if len(a.ResourcePrefixes)+len(a.ResourceURIs) > 0 {
		next.Caveats.ResourcePrefixes = slices.Clone(a.ResourcePrefixes)
		next.Caveats.ResourceURIs = slices.Clone(a.ResourceURIs)
		next.Caveats.ResourceNamePrefixes = nil
	}
	if len(a.Audience) > 0 {
		next.Audience = slices.Clone(a.Audience)
	}
	if !a.ExpiresAt.IsZero() && a.ExpiresAt.Before(next.ExpiresAt) {
		next.ExpiresAt = a.ExpiresAt
	}
	if a.ConfirmationJKT != "" {
		next.ConfirmationJKT = a.ConfirmationJKT
	}
	return next
}

// copyCeilingNarrows checks a copy's ceilings against every one already in
// force on cur: the capability's own and each enclosing copy's.
func copyCeilingNarrows(cur grant, c CopyCeiling) error {
	err := copyAxisNarrows("requests", c.Requests, cur.Caveats.Requests, cur.copies,
		func(o CopyCeiling) *RequestCeiling { return o.Requests })
	if err != nil {
		return err
	}
	return copyAxisNarrows("budget", c.Budget, cur.Caveats.Budget, cur.copies,
		func(o CopyCeiling) *BudgetCeiling { return o.Budget })
}

// ceiling is a limit on one axis of a copy: RequestCeiling or BudgetCeiling.
type ceiling[C any] interface {
	Validate() error
	Within(outer C) bool
	fmt.Stringer
}

// copyAxisNarrows checks a copy's ceiling c on one axis against the
// capability's on it and each enclosing copy's, which of reads. A nil c is
// an axis the block left alone: the copy keeps no ceiling of its own there,
// and the ones above it still bound it. A block sets only a bound
// (readBlockFacts), never an Unlimited ceiling.
func copyAxisNarrows[C ceiling[C]](axis string, c *C, capability C, enclosing []CopyCeiling, of func(CopyCeiling) *C) error {
	if c == nil {
		return nil
	}
	if err := (*c).Validate(); err != nil {
		return fmt.Errorf("copy %w", err)
	}
	if !(*c).Within(capability) {
		return fmt.Errorf("copy %s %s exceeds the capability's %s", axis, *c, capability)
	}
	for _, o := range enclosing {
		if outer := of(o); outer != nil && !(*c).Within(*outer) {
			return fmt.Errorf("copy %s %s exceeds an enclosing copy's %s", axis, *c, *outer)
		}
	}
	return nil
}

// MaxBiscuitBlocks bounds the attenuation blocks a Biscuit may carry. Each
// block is a revocation id the verifier looks up, and anyone holding a copy
// can append blocks offline, so an unbounded chain is a lookup per block on
// every request. Attenuate refuses to go past it, and the verifier refuses a
// token that has.
const MaxBiscuitBlocks = 32

// unsealedGrant folds u, what a form read past the sealed token parent,
// into the capability the token grants, and returns it with the token's
// revocation ids. u comes from the token's holder: its shape is checked
// before any block is folded in.
func unsealedGrant(parent *grant, u Unsealed) (*grant, [][]byte, error) {
	if err := checkUnsealed(u); err != nil {
		return nil, nil, refuseToken(ErrInvalidToken, err)
	}
	cur := grant{Capability: parent.Capability}
	for i, a := range u.Blocks {
		next, err := applyAttenuation(cur, a, u.RevocationIDs[i+1])
		if err != nil {
			return nil, nil, refuseToken(ErrBiscuitAttenuation, fmt.Errorf("block %d: %w", i+1, err))
		}
		cur = next
	}
	// Each block narrows what it was handed, but the result must still
	// be caveats the issuer could have minted, under the sealed token's
	// type.
	if err := cur.Caveats.Validate(); err != nil {
		return nil, nil, refuseToken(ErrBiscuitAttenuation, err)
	}
	return &cur, u.RevocationIDs, nil
}

// checkUnsealed refuses blocks past MaxBiscuitBlocks, and revocation ids
// that are not one for the first block and one for each other, each named.
func checkUnsealed(u Unsealed) error {
	switch {
	case len(u.Blocks) > MaxBiscuitBlocks:
		return fmt.Errorf("%d attenuation blocks, past MaxBiscuitBlocks (%d)", len(u.Blocks), MaxBiscuitBlocks)
	case len(u.RevocationIDs) != len(u.Blocks)+1:
		return fmt.Errorf("%d revocation ids, want the sealing block's and one for each of %d blocks", len(u.RevocationIDs), len(u.Blocks))
	}
	for i, id := range u.RevocationIDs {
		if len(id) == 0 {
			return fmt.Errorf("block %d has no revocation id", i)
		}
	}
	return nil
}

// sealedRoot is the root key the sealed token parent names.
func sealedRoot(parent *grant) (ed25519.PublicKey, error) {
	root, err := jose.UnB64(parent.biscuitRoot)
	if err != nil || len(root) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("%w: sealed token names no biscuit root", ErrInvalidToken)
	}
	return ed25519.PublicKey(root), nil
}
