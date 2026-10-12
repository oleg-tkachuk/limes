package limes

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	biscuit "github.com/biscuit-auth/biscuit-go/v2"
	"github.com/biscuit-auth/biscuit-go/v2/datalog"
	"github.com/biscuit-auth/biscuit-go/v2/pb"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	"github.com/oleg-tkachuk/limes/internal/jose"
)

// Offline attenuation, on Biscuit v3 tokens.
//
// A JWT capability can only be narrowed by asking the issuer (Delegate). A
// Biscuit can be narrowed by whoever holds it, with no key and no network:
// Attenuate appends a block that restricts it, and nobody — the holder
// included — can remove a block once appended. An agent can hand a sub-agent
// a strictly smaller token without a round trip.
//
// The Biscuit wraps an ordinary capability token: its authority block holds
// a JWT the issuer signed as usual (so a KMS-held key works), which carries
// the public half of a one-off Biscuit root key. The private half signs the
// authority block and is then dropped, so nobody can wrap the same JWT in a
// fresh Biscuit without the attenuation. The JWT itself is refused when
// presented alone.
//
// Attenuation blocks speak a fixed vocabulary of facts — the operations,
// resources, planes and expiry they allow, a key to bind to, and request and
// budget limits of the copy's own — which the verifier folds into a narrower
// Capability through Narrows, exactly as a server-side delegation is checked,
// and into Capability.Copies. Everything downstream (Caveats.Check,
// budgets, DPoP) then sees an ordinary Capability. A block holding anything
// outside the vocabulary — a rule, a check, another predicate — is refused:
// a restriction the verifier cannot enforce must not pass as one it did.

// Facts of the attenuation vocabulary. Each takes one term.
const (
	biscuitFactCapability     = "limes_capability"       // authority only: the sealed JWT
	biscuitFactOp             = "limes_op"               // string: an Op the token keeps
	biscuitFactResourcePrefix = "limes_resource_prefix"  // string
	biscuitFactResourceURI    = "limes_resource_uri"     // string
	biscuitFactPlane          = "limes_plane"            // string: an audience the token keeps
	biscuitFactExpires        = "limes_expires"          // date: an earlier expiry
	biscuitFactBind           = "limes_bind"             // string: a JWK thumbprint
	biscuitFactMaxRequests    = "limes_max_requests"     // integer: a copy's own request limit
	biscuitFactMaxBudget      = "limes_max_budget_nanos" // integer: a copy's own budget, in nanos
)

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
	c := vc.c
	switch {
	case c.ID == uuid.Nil:
		return "", invalidRequest("Biscuit requires a capability the verifier accepted")
	case vc.fromBiscuit:
		return "", invalidRequest("capability %s was read from a Biscuit; attenuate that instead", c.ID)
	case c.Issuer != i.issuerName:
		return "", invalidRequest("capability %s was issued by %q, not %q", c.ID, c.Issuer, i.issuerName)
	}
	if !i.clock().Before(c.ExpiresAt) {
		return "", fmt.Errorf("limes: biscuit of %s: %w", c.ID, ErrExpired)
	}
	revoked, err := i.store.IsRevoked(ctx, c.ID)
	if err != nil {
		return "", fmt.Errorf("limes: biscuit revocation lookup: %w", err)
	}
	if revoked {
		return "", fmt.Errorf("limes: biscuit of %s: %w", c.ID, ErrRevoked)
	}
	rootPub, rootPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", fmt.Errorf("limes: biscuit root key: %w", err)
	}
	sealed := c
	sealed.BiscuitRoot = base64.RawURLEncoding.EncodeToString(rootPub)
	inner, err := i.signer.sign(ctx, sealed)
	if err != nil {
		return "", fmt.Errorf("limes: sign sealed token: %w", err)
	}
	b := biscuit.NewBuilder(rootPriv)
	if err := b.AddAuthorityFact(biscuitFact(biscuitFactCapability, biscuit.String(inner))); err != nil {
		return "", fmt.Errorf("limes: biscuit authority: %w", err)
	}
	token, err := b.Build()
	if err != nil {
		return "", fmt.Errorf("limes: build biscuit: %w", err)
	}
	out, err := serializeBiscuit(token)
	if err != nil {
		return "", err
	}
	if err := i.fits(out); err != nil {
		return "", err
	}
	return out, nil
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
	// it, and its spend in the capability's unit. nil sets none; a ceiling
	// of 0 freezes the copy. Each must be within every limit already in
	// force, and an Unlimited one is refused: a copy only narrows.
	Requests *RequestCeiling
	Budget   *BudgetCeiling
}

// Attenuate appends a block that narrows token. It needs no key and no
// network; the result is a new token, and the input still works as before.
func Attenuate(token string, a Attenuation) (string, error) {
	b, err := parseBiscuit(token)
	if err != nil {
		return "", err
	}
	// BlockCount counts the attenuation blocks, not the authority.
	if attenuations := b.BlockCount(); attenuations >= MaxBiscuitBlocks {
		return "", invalidRequest("the token already has %d attenuation blocks, MaxBiscuitBlocks", attenuations)
	}
	block := b.CreateBlock()
	add := func(name string, term biscuit.Term) error {
		return block.AddFact(biscuitFact(name, term))
	}
	var errs []error
	for _, op := range a.Ops {
		errs = append(errs, add(biscuitFactOp, biscuit.String(op)))
	}
	for _, p := range a.ResourcePrefixes {
		errs = append(errs, add(biscuitFactResourcePrefix, biscuit.String(p)))
	}
	for _, u := range a.ResourceURIs {
		errs = append(errs, add(biscuitFactResourceURI, biscuit.String(u)))
	}
	for _, p := range a.Audience {
		errs = append(errs, add(biscuitFactPlane, biscuit.String(p)))
	}
	if !a.ExpiresAt.IsZero() {
		errs = append(errs, add(biscuitFactExpires, biscuit.Date(a.ExpiresAt.UTC().Truncate(time.Second))))
	}
	if a.ConfirmationJKT != "" {
		if err := validateThumbprint(a.ConfirmationJKT); err != nil {
			return "", err
		}
		errs = append(errs, add(biscuitFactBind, biscuit.String(a.ConfirmationJKT)))
	}
	if r := a.Requests; r != nil {
		if r.Unlimited || r.Validate() != nil {
			return "", invalidRequest("a copy's request ceiling is a bound within 0..%d", int64(MaxIJSONInteger))
		}
		errs = append(errs, add(biscuitFactMaxRequests, biscuit.Integer(r.Max)))
	}
	if b := a.Budget; b != nil {
		if b.Unlimited || b.Validate() != nil {
			return "", invalidRequest("a copy's budget ceiling is a bound within 0..%s", MaxNanos)
		}
		errs = append(errs, add(biscuitFactMaxBudget, biscuit.Integer(b.Max)))
	}
	if err := errors.Join(errs...); err != nil {
		return "", fmt.Errorf("limes: attenuation block: %w", err)
	}
	next, err := b.Append(rand.Reader, block.Build())
	if err != nil {
		return "", fmt.Errorf("limes: append attenuation: %w", err)
	}
	return serializeBiscuit(next)
}

// openBiscuit reads a Biscuit's sealed JWT and returns it with the function
// that finishes verification once the caller has verified that JWT: the
// signature chain against the root the JWT vouches for, then each
// attenuation block folded in. That function also returns the token's
// revocation ids, authority first, read from the chain it verified.
func openBiscuit(token string) (string, func(parent *Capability) (*Capability, [][]byte, error), error) {
	raw, container, err := decodeBiscuitContainer(token)
	if err != nil {
		return "", nil, err
	}
	blocks, err := decodeBiscuitBlocks(container)
	if err != nil {
		return "", nil, refuseToken(ErrInvalidToken, err)
	}
	inner, err := sealedToken(blocks[0])
	if err != nil {
		return "", nil, refuseToken(ErrInvalidToken, err)
	}
	// The chain is checked once the sealed token has named its root; the
	// caller verifies that token first and hands back what it vouches for.
	attenuate := func(parent *Capability) (*Capability, [][]byte, error) {
		root, err := jose.UnB64(parent.BiscuitRoot)
		if err != nil || len(root) != ed25519.PublicKeySize {
			return nil, nil, fmt.Errorf("%w: sealed token names no biscuit root", ErrInvalidToken)
		}
		var ids [][]byte
		if err := guardBiscuit(func() error {
			b, err := biscuit.Unmarshal(raw)
			if err != nil {
				return err
			}
			if _, err := b.Authorizer(ed25519.PublicKey(root)); err != nil {
				return fmt.Errorf("biscuit chain: %w", err)
			}
			ids = b.RevocationIds()
			return nil
		}); err != nil {
			return nil, nil, err
		}
		cur := *parent
		cur.BiscuitRoot = ""
		cur.Copies = nil
		for i, blk := range blocks[1:] {
			next, err := applyAttenuation(cur, blk, ids[i+1])
			if err != nil {
				return nil, nil, refuseToken(ErrBiscuitAttenuation, fmt.Errorf("block %d: %w", i+1, err))
			}
			cur = next
		}
		// Each block narrows what it was handed, but the result must still
		// be caveats the issuer could have minted.
		if err := cur.Caveats.Validate(); err != nil {
			return nil, nil, refuseToken(ErrBiscuitAttenuation, err)
		}
		return &cur, ids, nil
	}
	return inner, attenuate, nil
}

// decodedBlock is one block, its symbols resolved.
type decodedBlock struct {
	facts  []decodedFact
	others int // rules and checks: none is allowed
}

type decodedFact struct {
	name string
	term any // string, time.Time or int64
}

// MaxBiscuitBlocks bounds the attenuation blocks a Biscuit may carry. Each
// block is a revocation id the verifier looks up, and anyone holding a copy
// can append blocks offline, so an unbounded chain is a lookup per block on
// every request. Attenuate refuses to go past it, and the verifier refuses a
// token that has.
const MaxBiscuitBlocks = 32

// decodeBiscuitBlocks reads every block's facts as data. The signatures are
// not checked here — the caller does that before trusting anything read.
func decodeBiscuitBlocks(c *pb.Biscuit) ([]decodedBlock, error) {
	if n := len(c.GetBlocks()); n > MaxBiscuitBlocks {
		return nil, fmt.Errorf("%d attenuation blocks, past MaxBiscuitBlocks (%d)", n, MaxBiscuitBlocks)
	}
	signed := append([]*pb.SignedBlock{c.GetAuthority()}, c.GetBlocks()...)
	symbols := &datalog.SymbolTable{}
	out := make([]decodedBlock, 0, len(signed))
	for i, sb := range signed {
		var blk pb.Block
		if err := proto.Unmarshal(sb.GetBlock(), &blk); err != nil {
			return nil, fmt.Errorf("block %d: %w", i, err)
		}
		*symbols = append(*symbols, blk.GetSymbols()...)
		d := decodedBlock{others: len(blk.GetRulesV2()) + len(blk.GetChecksV2())}
		for _, f := range blk.GetFactsV2() {
			p := f.GetPredicate()
			name, err := biscuitSymbol(*symbols, p.GetName())
			if err != nil {
				return nil, fmt.Errorf("block %d: fact name: %w", i, err)
			}
			if len(p.GetTerms()) != 1 {
				return nil, fmt.Errorf("block %d: fact %s takes one term", i, name)
			}
			var term any
			switch t := p.GetTerms()[0].GetContent().(type) {
			case *pb.TermV2_String_:
				s, err := biscuitSymbol(*symbols, t.String_)
				if err != nil {
					return nil, fmt.Errorf("block %d: fact %s: %w", i, name, err)
				}
				term = s
			case *pb.TermV2_Date:
				term = time.Unix(int64(t.Date), 0).UTC() //nolint:gosec // a Biscuit date is seconds since the epoch
			case *pb.TermV2_Integer:
				term = t.Integer
			default:
				return nil, fmt.Errorf("block %d: fact %s has an unsupported term", i, name)
			}
			d.facts = append(d.facts, decodedFact{name: name, term: term})
		}
		out = append(out, d)
	}
	return out, nil
}

// sealedToken is the JWT the authority block carries.
func sealedToken(authority decodedBlock) (string, error) {
	if authority.others != 0 || len(authority.facts) != 1 || authority.facts[0].name != biscuitFactCapability {
		return "", errors.New("biscuit authority is not a sealed capability")
	}
	inner, ok := authority.facts[0].term.(string)
	if !ok || isBiscuit(inner) {
		return "", errors.New("biscuit authority is not a sealed capability")
	}
	return inner, nil
}

// applyAttenuation folds one block into cur, refusing anything it cannot
// enforce and anything that would widen cur. id is the block's revocation id,
// which keys the counters of the limits it sets.
func applyAttenuation(cur Capability, blk decodedBlock, id []byte) (Capability, error) {
	f, err := readBlockFacts(blk, cur.ConfirmationJKT)
	if err != nil {
		return cur, err
	}
	next := f.narrow(cur)
	if err := Narrows(cur, next); err != nil {
		return cur, err
	}
	if f.requests == nil && f.budget == nil {
		return next, nil
	}
	// An axis the block left alone has no ceiling of this copy's own.
	ceiling := CopyCeiling{RevocationID: id, Requests: f.requests, Budget: f.budget}
	if err := copyCeilingNarrows(cur, ceiling); err != nil {
		return cur, err
	}
	next.Copies = append([]CopyCeiling{ceiling}, cur.Copies...)
	return next, nil
}

// blockFacts is what one attenuation block says, read but not yet applied.
// A fact the block repeats accumulates (ops, resources, planes), takes the
// earliest value (expiry) or takes the last (binding, copy limits).
type blockFacts struct {
	ops            []Op
	prefixes, uris []string
	resources      bool // the block names resources, so they replace cur's
	planes         []string
	expires        time.Time
	bind           string
	// requests and budget are the copy ceilings the block set, nil for an
	// axis it left alone.
	requests *RequestCeiling
	budget   *BudgetCeiling
}

// readBlockFacts reads blk's facts, refusing any outside the vocabulary or
// with the wrong term. boundJKT is the key cur is already bound to, which a
// block may restate but not change.
func readBlockFacts(blk decodedBlock, boundJKT string) (blockFacts, error) {
	var f blockFacts
	if blk.others != 0 {
		return f, errors.New("rules and checks are not supported; use the limes_* facts")
	}
	for _, fact := range blk.facts {
		if err := f.read(fact, boundJKT); err != nil {
			return f, err
		}
	}
	return f, nil
}

func (f *blockFacts) read(fact decodedFact, boundJKT string) error {
	s, isString := fact.term.(string)
	switch fact.name {
	case biscuitFactOp:
		if !isString {
			return errors.New("limes_op takes a string")
		}
		f.ops = append(f.ops, Op(s))
	case biscuitFactResourcePrefix, biscuitFactResourceURI:
		if !isString || s == "" {
			return fmt.Errorf("%s takes a non-empty string", fact.name)
		}
		f.resources = true
		if fact.name == biscuitFactResourcePrefix {
			f.prefixes = append(f.prefixes, s)
		} else {
			f.uris = append(f.uris, s)
		}
	case biscuitFactPlane:
		if !isString {
			return errors.New("limes_plane takes a string")
		}
		f.planes = append(f.planes, s)
	case biscuitFactExpires:
		t, ok := fact.term.(time.Time)
		if !ok {
			return errors.New("limes_expires takes a date")
		}
		if f.expires.IsZero() || t.Before(f.expires) {
			f.expires = t
		}
	case biscuitFactBind:
		if !isString || validateThumbprint(s) != nil {
			return errors.New("limes_bind takes a JWK thumbprint")
		}
		if boundJKT != "" && boundJKT != s {
			return errors.New("a key-bound token cannot be rebound offline")
		}
		f.bind = s
	case biscuitFactMaxRequests, biscuitFactMaxBudget:
		n, ok := fact.term.(int64)
		if !ok || n < 0 {
			return fmt.Errorf("%s takes an integer of 0 or more", fact.name)
		}
		if fact.name == biscuitFactMaxRequests {
			f.requests = &RequestCeiling{Max: n}
		} else {
			f.budget = &BudgetCeiling{Max: Nanos(n)}
		}
	default:
		return fmt.Errorf("fact %q is not part of the attenuation vocabulary", fact.name)
	}
	return nil
}

// narrow is cur with the block's facts applied. Whether the result is in
// fact narrower is for Narrows to decide.
func (f blockFacts) narrow(cur Capability) Capability {
	next := cur
	next.Caveats.Ops = slices.Clone(cur.Caveats.Ops)
	if len(f.ops) > 0 {
		next.Caveats.Ops = f.ops
	}
	if f.resources {
		next.Caveats.ResourcePrefixes = f.prefixes
		next.Caveats.ResourceURIs = f.uris
	}
	if len(f.planes) > 0 {
		next.Audience = f.planes
	}
	if !f.expires.IsZero() && f.expires.Before(next.ExpiresAt) {
		next.ExpiresAt = f.expires
	}
	if f.bind != "" {
		next.ConfirmationJKT = f.bind
	}
	return next
}

// copyCeilingNarrows checks a copy's ceilings against every one already in
// force on cur: the capability's own and each enclosing copy's.
func copyCeilingNarrows(cur Capability, c CopyCeiling) error {
	// A nil axis is one this block left alone: the copy keeps no ceiling of
	// its own there, and the ones above it still bound it. A block sets only
	// a bound (readBlockFacts), never an Unlimited ceiling.
	if r := c.Requests; r != nil {
		if err := r.Validate(); err != nil {
			return fmt.Errorf("copy %w", err)
		}
		if !r.Within(cur.Caveats.Requests) {
			return fmt.Errorf("copy requests %s exceed the capability's %s", r, cur.Caveats.Requests)
		}
		for _, o := range cur.Copies {
			if o.Requests != nil && !r.Within(*o.Requests) {
				return fmt.Errorf("copy requests %s exceed an enclosing copy's %s", r, o.Requests)
			}
		}
	}
	if b := c.Budget; b != nil {
		if err := b.Validate(); err != nil {
			return fmt.Errorf("copy %w", err)
		}
		if !b.Within(cur.Caveats.Budget) {
			return fmt.Errorf("copy budget %s exceeds the capability's %s", b, cur.Caveats.Budget)
		}
		for _, o := range cur.Copies {
			if o.Budget != nil && !b.Within(*o.Budget) {
				return fmt.Errorf("copy budget %s exceeds an enclosing copy's %s", b, o.Budget)
			}
		}
	}
	return nil
}

func biscuitFact(name string, term biscuit.Term) biscuit.Fact {
	return biscuit.Fact{Predicate: biscuit.Predicate{Name: name, IDs: []biscuit.Term{term}}}
}

func parseBiscuit(token string) (*biscuit.Biscuit, error) {
	if !isBiscuit(token) {
		return nil, fmt.Errorf("%w: not a Biscuit token", ErrInvalidToken)
	}
	raw, _, err := decodeBiscuitContainer(token)
	if err != nil {
		return nil, err
	}
	var b *biscuit.Biscuit
	if err := guardBiscuit(func() error {
		b, err = biscuit.Unmarshal(raw)
		return err
	}); err != nil {
		return nil, err
	}
	return b, nil
}

// decodeBiscuitContainer reads token's bytes and its container, refusing a
// container biscuit-go would trip over: no authority block, or a proof that
// is neither a next secret of a seed's length nor a final signature of a
// signature's. biscuit-go checks the keys and signatures of the blocks but
// hands the proof's secret to ed25519.NewKeyFromSeed as it is, which panics
// on any other length.
func decodeBiscuitContainer(token string) ([]byte, *pb.Biscuit, error) {
	raw, err := jose.UnB64(token)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: biscuit encoding: %w", ErrInvalidToken, err)
	}
	var c pb.Biscuit
	if err := proto.Unmarshal(raw, &c); err != nil || c.GetAuthority() == nil {
		return nil, nil, fmt.Errorf("%w: biscuit container", ErrInvalidToken)
	}
	switch p := c.GetProof().GetContent().(type) {
	case *pb.Proof_NextSecret:
		if len(p.NextSecret) != ed25519.SeedSize {
			return nil, nil, fmt.Errorf("%w: biscuit proof secret is %d bytes, not %d", ErrInvalidToken, len(p.NextSecret), ed25519.SeedSize)
		}
	case *pb.Proof_FinalSignature:
		if len(p.FinalSignature) != ed25519.SignatureSize {
			return nil, nil, fmt.Errorf("%w: biscuit final signature is %d bytes, not %d", ErrInvalidToken, len(p.FinalSignature), ed25519.SignatureSize)
		}
	default:
		return nil, nil, fmt.Errorf("%w: biscuit proof has no content", ErrInvalidToken)
	}
	return raw, &c, nil
}

// biscuitSymbol resolves a symbol id against the default symbols and the
// table the blocks so far declared. datalog.SymbolTable.Str converts the id
// to an int, so an id of 2^63 or more indexes the defaults at a negative
// offset and panics; and it reads an unknown id as the text "<invalid symbol
// N>", which a fact must not carry.
func biscuitSymbol(table datalog.SymbolTable, id uint64) (string, error) {
	defaults, offset := uint64(len(datalog.DEFAULT_SYMBOLS)), uint64(datalog.OFFSET) //nolint:gosec // G115: a length and biscuit-go's positive table offset
	switch {
	case id < defaults:
		return datalog.DEFAULT_SYMBOLS[id], nil
	case id >= offset && id-offset < uint64(len(table)):
		return table[id-offset], nil
	}
	return "", fmt.Errorf("symbol %d is in neither the default nor the block symbols", id)
}

// guardBiscuit runs f, a call into biscuit-go over bytes the token's holder
// chose, and returns its error as ErrInvalidToken — and a panic as one too.
// The container is checked before biscuit-go sees it, but biscuit-go also
// decodes every block's rules, checks and expressions, which limes then
// refuses; a token must never take the process down on the way there.
func guardBiscuit(f func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%w: biscuit: %v", ErrInvalidToken, r)
		}
	}()
	if err := f(); err != nil {
		return refuseToken(ErrInvalidToken, err)
	}
	return nil
}

func serializeBiscuit(b *biscuit.Biscuit) (string, error) {
	raw, err := b.Serialize()
	if err != nil {
		return "", fmt.Errorf("limes: serialize biscuit: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
