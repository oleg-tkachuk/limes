package limes

import (
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
	"google.golang.org/protobuf/proto"
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
// limit would go unenforced, so the token is refused instead.
var ErrCopyCountersNotMetered = fmt.Errorf("%w: copy limits need a verifier with MeterCopies", ErrBiscuitAttenuation)

// ErrBiscuitAttenuation — an attenuation block the verifier cannot honour:
// outside the vocabulary, malformed, or widening its parent. Matches
// ErrInvalidSignature, so consumers that map only that sentinel refuse it.
var ErrBiscuitAttenuation error = &popError{"capability: invalid Biscuit attenuation"}

// IsBiscuit reports whether token is in the Biscuit form rather than a JWT:
// a JWT has exactly two dots, base64url has none.
func IsBiscuit(token string) bool {
	return token != "" && !strings.Contains(token, ".")
}

// Biscuit returns the Biscuit form of a capability this issuer issued: the
// same capability — same ID, caveats, expiry and binding, revoked together
// with it — in a token its holder can attenuate offline. One copy can also be
// revoked on its own; see BiscuitRevocationStore.
func (i *Issuer) Biscuit(c Capability) (string, error) {
	rootPub, rootPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", fmt.Errorf("capability: biscuit root key: %w", err)
	}
	sealed := c
	sealed.BiscuitRoot = base64.RawURLEncoding.EncodeToString(rootPub)
	inner, err := i.signer.Sign(sealed)
	if err != nil {
		return "", fmt.Errorf("capability: sign sealed token: %w", err)
	}
	b := biscuit.NewBuilder(rootPriv)
	if err := b.AddAuthorityFact(biscuitFact(biscuitFactCapability, biscuit.String(inner))); err != nil {
		return "", fmt.Errorf("capability: biscuit authority: %w", err)
	}
	token, err := b.Build()
	if err != nil {
		return "", fmt.Errorf("capability: build biscuit: %w", err)
	}
	return serializeBiscuit(token)
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
	// Planes (audiences) the token keeps.
	Planes []string
	// ExpiresAt, when earlier than the token's, becomes its expiry.
	ExpiresAt time.Time
	// ConfirmationJKT binds the token to a key (KeyThumbprint). Only an
	// unbound token can be bound offline: a bound one stays bound to its key,
	// because rebinding needs no key and anyone could do it.
	ConfirmationJKT string
	// MaxRequests and MaxBudget give this copy limits of its own, counted
	// apart from its siblings' and within the capability's: requests made
	// with it, and its spend in the capability's unit. 0 sets none. Each
	// must be within every limit already in force.
	MaxRequests int64
	MaxBudget   Nanos
}

// Attenuate appends a block that narrows token. It needs no key and no
// network; the result is a new token, and the input still works as before.
func Attenuate(token string, a Attenuation) (string, error) {
	b, err := parseBiscuit(token)
	if err != nil {
		return "", err
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
	for _, p := range a.Planes {
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
	if a.MaxRequests < 0 || a.MaxBudget < 0 {
		return "", fmt.Errorf("%w: copy limits cannot be negative", ErrBiscuitAttenuation)
	}
	if a.MaxRequests > 0 {
		errs = append(errs, add(biscuitFactMaxRequests, biscuit.Integer(a.MaxRequests)))
	}
	if a.MaxBudget > 0 {
		errs = append(errs, add(biscuitFactMaxBudget, biscuit.Integer(a.MaxBudget)))
	}
	if err := errors.Join(errs...); err != nil {
		return "", fmt.Errorf("capability: attenuation block: %w", err)
	}
	next, err := b.Append(rand.Reader, block.Build())
	if err != nil {
		return "", fmt.Errorf("capability: append attenuation: %w", err)
	}
	return serializeBiscuit(next)
}

// openBiscuit reads a Biscuit's sealed JWT and returns it with the function
// that finishes verification once the caller has verified that JWT: the
// signature chain against the root the JWT vouches for, then each
// attenuation block folded in. That function also returns the token's
// revocation ids, authority first, read from the chain it verified.
// meterCopies admits blocks that set a copy's own limits.
func openBiscuit(token string) (string, func(parent *Capability, meterCopies bool) (*Capability, [][]byte, error), error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return "", nil, fmt.Errorf("%w: biscuit encoding: %w", ErrInvalidSignature, err)
	}
	var container pb.Biscuit
	if err := proto.Unmarshal(raw, &container); err != nil || container.GetAuthority() == nil {
		return "", nil, fmt.Errorf("%w: biscuit container", ErrInvalidSignature)
	}
	blocks, err := decodeBiscuitBlocks(&container)
	if err != nil {
		return "", nil, fmt.Errorf("%w: %w", ErrInvalidSignature, err)
	}
	inner, err := sealedToken(blocks[0])
	if err != nil {
		return "", nil, fmt.Errorf("%w: %w", ErrInvalidSignature, err)
	}
	// The chain is checked once the sealed token has named its root; the
	// caller verifies that token first and hands back what it vouches for.
	attenuate := func(parent *Capability, meterCopies bool) (*Capability, [][]byte, error) {
		root, err := base64.RawURLEncoding.DecodeString(parent.BiscuitRoot)
		if err != nil || len(root) != ed25519.PublicKeySize {
			return nil, nil, fmt.Errorf("%w: sealed token names no biscuit root", ErrInvalidSignature)
		}
		b, err := biscuit.Unmarshal(raw)
		if err != nil {
			return nil, nil, fmt.Errorf("%w: %w", ErrInvalidSignature, err)
		}
		if _, err := b.Authorizer(ed25519.PublicKey(root)); err != nil {
			return nil, nil, fmt.Errorf("%w: biscuit chain: %w", ErrInvalidSignature, err)
		}
		cur := *parent
		cur.BiscuitRoot = ""
		cur.Copies = nil
		ids := b.RevocationIds()
		for i, blk := range blocks[1:] {
			next, err := applyAttenuation(cur, blk, ids[i+1], meterCopies)
			if err != nil {
				if errors.Is(err, ErrCopyCountersNotMetered) {
					return nil, nil, fmt.Errorf("block %d: %w", i+1, err)
				}
				return nil, nil, fmt.Errorf("%w: block %d: %w", ErrBiscuitAttenuation, i+1, err)
			}
			cur = next
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

// decodeBiscuitBlocks reads every block's facts as data. The signatures are
// not checked here — the caller does that before trusting anything read.
func decodeBiscuitBlocks(c *pb.Biscuit) ([]decodedBlock, error) {
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
			name := symbols.Str(datalog.String(p.GetName()))
			if len(p.GetTerms()) != 1 {
				return nil, fmt.Errorf("block %d: fact %s takes one term", i, name)
			}
			var term any
			switch t := p.GetTerms()[0].GetContent().(type) {
			case *pb.TermV2_String_:
				term = symbols.Str(datalog.String(t.String_))
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
	if !ok || IsBiscuit(inner) {
		return "", errors.New("biscuit authority is not a sealed capability")
	}
	return inner, nil
}

// applyAttenuation folds one block into cur, refusing anything it cannot
// enforce and anything that would widen cur. id is the block's revocation id,
// which keys the counters of the limits it sets.
func applyAttenuation(cur Capability, blk decodedBlock, id []byte, meterCopies bool) (Capability, error) {
	f, err := readBlockFacts(blk, cur.ConfirmationJKT)
	if err != nil {
		return cur, err
	}
	next := f.narrow(cur)
	if err := Narrows(cur, next); err != nil {
		return cur, err
	}
	if !f.ceiling.set() {
		return next, nil
	}
	if !meterCopies {
		return cur, ErrCopyCountersNotMetered
	}
	f.ceiling.RevocationID = id
	if err := copyCeilingNarrows(cur, f.ceiling); err != nil {
		return cur, err
	}
	next.Copies = append([]CopyCeiling{f.ceiling}, cur.Copies...)
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
	ceiling        CopyCeiling
}

// set reports whether a block put a limit on its copy.
func (c CopyCeiling) set() bool { return c.MaxRequests > 0 || c.MaxBudget > 0 }

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
		if !ok || n <= 0 {
			return fmt.Errorf("%s takes a positive integer", fact.name)
		}
		if fact.name == biscuitFactMaxRequests {
			f.ceiling.MaxRequests = n
		} else {
			f.ceiling.MaxBudget = Nanos(n)
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

// copyCeilingNarrows checks a copy's limits against every limit already in
// force on cur: the capability's own and each enclosing copy's. A limit not
// set by the new block stays as it was, so only the ones it sets are checked.
func copyCeilingNarrows(cur Capability, c CopyCeiling) error {
	if c.MaxRequests > 0 {
		own := int64(cur.Caveats.MaxRequests)
		if err := limitNarrows("requests", c.MaxRequests, own, cur.Copies, func(o CopyCeiling) int64 { return o.MaxRequests }); err != nil {
			return err
		}
	}
	if c.MaxBudget > 0 {
		if c.MaxBudget > MaxNanos {
			return fmt.Errorf("copy budget %s exceeds %s", c.MaxBudget, MaxNanos)
		}
		own := int64(cur.Caveats.MaxBudgetAmount)
		if err := limitNarrows("budget nanos", int64(c.MaxBudget), own, cur.Copies, func(o CopyCeiling) int64 { return int64(o.MaxBudget) }); err != nil {
			return err
		}
	}
	return nil
}

// limitNarrows checks a copy's limit n against the capability's own (≤ 0:
// none) and each enclosing copy's, read by of (≤ 0: none).
func limitNarrows(what string, n, own int64, outer []CopyCeiling, of func(CopyCeiling) int64) error {
	if own > 0 && n > own {
		return fmt.Errorf("copy %s %d exceed the capability's %d", what, n, own)
	}
	for _, o := range outer {
		if limit := of(o); limit > 0 && n > limit {
			return fmt.Errorf("copy %s %d exceed an enclosing copy's %d", what, n, limit)
		}
	}
	return nil
}

func biscuitFact(name string, term biscuit.Term) biscuit.Fact {
	return biscuit.Fact{Predicate: biscuit.Predicate{Name: name, IDs: []biscuit.Term{term}}}
}

func parseBiscuit(token string) (*biscuit.Biscuit, error) {
	if !IsBiscuit(token) {
		return nil, fmt.Errorf("%w: not a Biscuit token", ErrInvalidSignature)
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return nil, fmt.Errorf("%w: biscuit encoding: %w", ErrInvalidSignature, err)
	}
	b, err := biscuit.Unmarshal(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidSignature, err)
	}
	return b, nil
}

func serializeBiscuit(b *biscuit.Biscuit) (string, error) {
	raw, err := b.Serialize()
	if err != nil {
		return "", fmt.Errorf("capability: serialize biscuit: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
