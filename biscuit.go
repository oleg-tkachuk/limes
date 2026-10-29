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

	biscuit "github.com/eclipse-biscuit/biscuit-go/v2"
	"github.com/eclipse-biscuit/biscuit-go/v2/datalog"
	"github.com/eclipse-biscuit/biscuit-go/v2/pb"
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
// and into the copy limits VerifiedCapability.Copies reports. Everything downstream (Caveats.Check,
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
	if err := checkAttenuation(a); err != nil {
		return "", err
	}
	block := b.CreateBlock()
	var errs []error
	for _, fact := range attenuationFacts(a) {
		errs = append(errs, block.AddFact(fact))
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

// attenuationFacts are the facts of the block that carries a, which
// checkAttenuation accepted.
func attenuationFacts(a Attenuation) []biscuit.Fact {
	facts := slices.Concat(
		stringFacts(biscuitFactOp, a.Ops),
		stringFacts(biscuitFactResourcePrefix, a.ResourcePrefixes),
		stringFacts(biscuitFactResourceURI, a.ResourceURIs),
		stringFacts(biscuitFactPlane, a.Audience),
	)
	if !a.ExpiresAt.IsZero() {
		facts = append(facts, biscuitFact(biscuitFactExpires, biscuit.Date(a.ExpiresAt.UTC().Truncate(time.Second))))
	}
	if a.ConfirmationJKT != "" {
		facts = append(facts, biscuitFact(biscuitFactBind, biscuit.String(a.ConfirmationJKT)))
	}
	if a.Requests != nil {
		facts = append(facts, biscuitFact(biscuitFactMaxRequests, biscuit.Integer(a.Requests.Max)))
	}
	if a.Budget != nil {
		facts = append(facts, biscuitFact(biscuitFactMaxBudget, biscuit.Integer(a.Budget.Max)))
	}
	return facts
}

// stringFacts is one fact named name for each of values.
func stringFacts[S ~string](name string, values []S) []biscuit.Fact {
	facts := make([]biscuit.Fact, 0, len(values))
	for _, v := range values {
		facts = append(facts, biscuitFact(name, biscuit.String(v)))
	}
	return facts
}

// biscuitForm is the Biscuit form of a capability token (TokenForm): a
// Biscuit v3 whose authority block seals the JWT, and whose attenuation
// blocks speak the vocabulary of facts above.
type biscuitForm struct{}

// Seal builds a Biscuit around the JWT sign returns for a fresh root key,
// whose private half signs the authority block and is then dropped.
func (biscuitForm) Seal(ctx context.Context, sign func(ctx context.Context, root ed25519.PublicKey) (string, error)) (string, error) {
	rootPub, rootPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", fmt.Errorf("limes: biscuit root key: %w", err)
	}
	inner, err := sign(ctx, rootPub)
	if err != nil {
		return "", err
	}
	b := biscuit.NewBuilder(rootPriv)
	if err := b.AddAuthorityFact(biscuitFact(biscuitFactCapability, biscuit.String(inner))); err != nil {
		return "", fmt.Errorf("limes: biscuit authority: %w", err)
	}
	token, err := b.Build()
	if err != nil {
		return "", fmt.Errorf("limes: build biscuit: %w", err)
	}
	return serializeBiscuit(token)
}

// Open reads a Biscuit's sealed JWT and returns it with the function that
// finishes reading the token once the caller has verified that JWT: the
// signature chain under the root the JWT names, then each attenuation
// block read as an Attenuation.
func (biscuitForm) Open(token string) (string, func(root ed25519.PublicKey) (Unsealed, error), error) {
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
	unseal := func(root ed25519.PublicKey) (Unsealed, error) {
		return unsealBiscuit(raw, blocks, root)
	}
	return inner, unseal, nil
}

// unsealBiscuit verifies a Biscuit's chain under root and reads each of its
// attenuation blocks.
func unsealBiscuit(raw []byte, blocks []decodedBlock, root ed25519.PublicKey) (Unsealed, error) {
	ids, err := verifyBiscuitChain(raw, root)
	if err != nil {
		return Unsealed{}, err
	}
	u := Unsealed{RevocationIDs: ids}
	for i, blk := range blocks[1:] {
		f, err := readBlockFacts(blk)
		if err != nil {
			return Unsealed{}, refuseToken(ErrBiscuitAttenuation, fmt.Errorf("block %d: %w", i+1, err))
		}
		u.Blocks = append(u.Blocks, f.attenuation())
	}
	return u, nil
}

// verifyBiscuitChain verifies the signature chain of the Biscuit raw under
// root, and returns its revocation ids, authority first.
func verifyBiscuitChain(raw []byte, root ed25519.PublicKey) ([][]byte, error) {
	var ids [][]byte
	if err := guardBiscuit(func() error {
		b, err := biscuit.Unmarshal(raw)
		if err != nil {
			return err
		}
		if _, err := b.Authorizer(root); err != nil {
			return fmt.Errorf("biscuit chain: %w", err)
		}
		ids = b.RevocationIds()
		return nil
	}); err != nil {
		return nil, err
	}
	return ids, nil
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
	if n := len(c.GetBlocks()); n > MaxBiscuitBlocks {
		return nil, fmt.Errorf("%d attenuation blocks, past MaxBiscuitBlocks (%d)", n, MaxBiscuitBlocks)
	}
	signed := append([]*pb.SignedBlock{c.GetAuthority()}, c.GetBlocks()...)
	symbols := &datalog.SymbolTable{}
	out := make([]decodedBlock, 0, len(signed))
	for i, sb := range signed {
		d, err := decodeBlock(sb, symbols)
		if err != nil {
			return nil, fmt.Errorf("block %d: %w", i, err)
		}
		out = append(out, d)
	}
	return out, nil
}

// decodeBlock reads one block's facts, adding its symbols to symbols: a
// block's symbols extend those of the blocks before it.
func decodeBlock(sb *pb.SignedBlock, symbols *datalog.SymbolTable) (decodedBlock, error) {
	var blk pb.Block
	if err := proto.Unmarshal(sb.GetBlock(), &blk); err != nil {
		return decodedBlock{}, err
	}
	*symbols = append(*symbols, blk.GetSymbols()...)
	d := decodedBlock{others: len(blk.GetRulesV2()) + len(blk.GetChecksV2())}
	for _, f := range blk.GetFactsV2() {
		fact, err := decodeFact(f, *symbols)
		if err != nil {
			return decodedBlock{}, err
		}
		d.facts = append(d.facts, fact)
	}
	return d, nil
}

// decodeFact reads a fact of one term.
func decodeFact(f *pb.FactV2, symbols datalog.SymbolTable) (decodedFact, error) {
	p := f.GetPredicate()
	name, err := biscuitSymbol(symbols, p.GetName())
	if err != nil {
		return decodedFact{}, fmt.Errorf("fact name: %w", err)
	}
	if len(p.GetTerms()) != 1 {
		return decodedFact{}, fmt.Errorf("fact %s takes one term", name)
	}
	term, err := decodeTerm(name, p.GetTerms()[0], symbols)
	if err != nil {
		return decodedFact{}, err
	}
	return decodedFact{name: name, term: term}, nil
}

// decodeTerm reads the term of the fact named name: a string, a date or an
// integer, the only terms the attenuation vocabulary takes.
func decodeTerm(name string, t *pb.TermV2, symbols datalog.SymbolTable) (any, error) {
	switch t := t.GetContent().(type) {
	case *pb.TermV2_String_:
		s, err := biscuitSymbol(symbols, t.String_)
		if err != nil {
			return nil, fmt.Errorf("fact %s: %w", name, err)
		}
		return s, nil
	case *pb.TermV2_Date:
		return time.Unix(int64(t.Date), 0).UTC(), nil //nolint:gosec // a Biscuit date is seconds since the epoch
	case *pb.TermV2_Integer:
		return t.Integer, nil
	}
	return nil, fmt.Errorf("fact %s has an unsupported term", name)
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

// blockFacts is what one attenuation block says, read but not yet applied.
// A fact the block repeats accumulates (ops, resources, planes), takes the
// earliest value (expiry) or takes the last (binding, copy limits).
type blockFacts struct {
	ops            []Op
	prefixes, uris []string
	planes         []string
	expires        time.Time
	bind           string
	// requests and budget are the copy ceilings the block set, nil for an
	// axis it left alone.
	requests *RequestCeiling
	budget   *BudgetCeiling
}

// readBlockFacts reads blk's facts, refusing any outside the vocabulary or
// with the wrong term.
func readBlockFacts(blk decodedBlock) (blockFacts, error) {
	var f blockFacts
	if blk.others != 0 {
		return f, errors.New("rules and checks are not supported; use the limes_* facts")
	}
	for _, fact := range blk.facts {
		if err := f.read(fact); err != nil {
			return f, err
		}
	}
	return f, nil
}

// read adds fact to f, refusing one outside the vocabulary or with the
// wrong term.
func (f *blockFacts) read(fact decodedFact) error {
	switch fact.name {
	case biscuitFactOp, biscuitFactPlane:
		return f.readListed(fact)
	case biscuitFactResourcePrefix, biscuitFactResourceURI:
		return f.readResource(fact)
	case biscuitFactExpires:
		return f.readExpires(fact)
	case biscuitFactBind:
		return f.readBind(fact)
	case biscuitFactMaxRequests, biscuitFactMaxBudget:
		return f.readCeiling(fact)
	}
	return fmt.Errorf("fact %q is not part of the attenuation vocabulary", fact.name)
}

// readListed adds an op or a plane, which accumulate.
func (f *blockFacts) readListed(fact decodedFact) error {
	s, ok := fact.term.(string)
	if !ok {
		return fmt.Errorf("%s takes a string", fact.name)
	}
	if fact.name == biscuitFactOp {
		f.ops = append(f.ops, Op(s))
	} else {
		f.planes = append(f.planes, s)
	}
	return nil
}

// readResource adds a resource prefix or URI, which accumulate and replace
// every resource caveat of the token.
func (f *blockFacts) readResource(fact decodedFact) error {
	s, ok := fact.term.(string)
	if !ok || s == "" {
		return fmt.Errorf("%s takes a non-empty string", fact.name)
	}
	if fact.name == biscuitFactResourcePrefix {
		f.prefixes = append(f.prefixes, s)
	} else {
		f.uris = append(f.uris, s)
	}
	return nil
}

// readExpires keeps the earliest expiry the block names.
func (f *blockFacts) readExpires(fact decodedFact) error {
	t, ok := fact.term.(time.Time)
	if !ok {
		return errors.New("limes_expires takes a date")
	}
	if f.expires.IsZero() || t.Before(f.expires) {
		f.expires = t
	}
	return nil
}

// readBind keeps the key the block binds the token to. Whether the token
// may be bound to it is applyAttenuation's to decide.
func (f *blockFacts) readBind(fact decodedFact) error {
	s, ok := fact.term.(string)
	if !ok || validateThumbprint(s) != nil {
		return errors.New("limes_bind takes a JWK thumbprint")
	}
	f.bind = s
	return nil
}

// readCeiling keeps the last request or budget limit the block sets.
func (f *blockFacts) readCeiling(fact decodedFact) error {
	n, ok := fact.term.(int64)
	if !ok || n < 0 {
		return fmt.Errorf("%s takes an integer of 0 or more", fact.name)
	}
	if fact.name == biscuitFactMaxRequests {
		f.requests = &RequestCeiling{Max: n}
	} else {
		f.budget = &BudgetCeiling{Max: Nanos(n)}
	}
	return nil
}

// attenuation is what the block says, as the core folds it in.
func (f blockFacts) attenuation() Attenuation {
	return Attenuation{
		Ops: f.ops, ResourcePrefixes: f.prefixes, ResourceURIs: f.uris, Audience: f.planes,
		ExpiresAt: f.expires, ConfirmationJKT: f.bind, Requests: f.requests, Budget: f.budget,
	}
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

const (
	// base64Pad is base64url's padding, which biscuit-rust and the libraries
	// built on it write after a Biscuit and biscuit-go does not.
	base64Pad = "="
	// base64Quantum is the length padding rounds an encoding up to.
	base64Quantum = 4
)

// unB64Biscuit decodes a Biscuit written with or without padding, strictly
// either way: padding, when present, is exactly what the length needs. The
// encoding is not the token's identity — a copy is revoked by its blocks'
// signatures — so both spellings are the same token.
func unB64Biscuit(token string) ([]byte, error) {
	if bare := strings.TrimRight(token, base64Pad); bare != token {
		if len(token)%base64Quantum != 0 || len(token)-len(bare) != (base64Quantum-len(bare)%base64Quantum)%base64Quantum {
			return nil, errors.New("base64url: wrong padding")
		}
		token = bare
	}
	return jose.UnB64(token)
}

// decodeBiscuitContainer reads token's bytes and its container, refusing a
// container biscuit-go would trip over: no authority block, or a proof that
// is neither a next secret of a seed's length nor a final signature of a
// signature's. biscuit-go checks the keys and signatures of the blocks but
// hands the proof's secret to ed25519.NewKeyFromSeed as it is, which panics
// on any other length.
func decodeBiscuitContainer(token string) ([]byte, *pb.Biscuit, error) {
	raw, err := unB64Biscuit(token)
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
