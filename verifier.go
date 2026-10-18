package limes

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"maps"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes/dpop"
	"github.com/oleg-tkachuk/limes/internal/jose"
	"github.com/oleg-tkachuk/limes/jwks"
)

// KeyResolver resolves a JWKS `kid` to the public key the verifier
// should use. jwks.Static holds keys in memory; jwks.Remote
// fetches them from an issuer's JWKS endpoint with a cache.
//
// Keys are Ed25519 for all of 1.x: the token format, not the key, names
// the algorithm, so another algorithm would be another token type
// (AcceptTokenTypes) rather than a key this interface returns.
type KeyResolver interface {
	// PublicKey returns the Ed25519 public key for the given kid.
	// Returns jwks.ErrUnknownKID if the key set does not contain the kid,
	// which the verifier reads as the token's fault (ErrInvalidToken).
	// Any other error is the resolver's: the verifier refuses the token
	// with it as it is, the server's fault rather than the token's.
	PublicKey(ctx context.Context, kid string) (ed25519.PublicKey, error)
}

// DefaultMaxTokenBytes bounds the token a verifier will look at. A capability
// with a few dozen resource entries is a couple of kilobytes; anything far
// larger is refused before any decoding is spent on it.
const DefaultMaxTokenBytes = 16 << 10

// DefaultLeeway is VerifierConfig.Leeway when it is zero.
const DefaultLeeway = 30 * time.Second

// MaxLeeway is the widest VerifierConfig.Leeway a verifier takes: a token is
// accepted for at most this long past its expiry, which is what lets a store
// purge it safely after MinPurgeRetention.
const MaxLeeway = 5 * time.Minute

// VerifierConfig wires the verifier. Keys, Revocations and TrustedIssuers
// are required.
type VerifierConfig struct {
	// Keys resolves JWKS kids to Ed25519 public keys.
	Keys KeyResolver

	// Revocations answers whether a capability — or any ancestor in its
	// delegation chain — is revoked. Wrap the store in a
	// RevocationCache to keep the per-call cost flat.
	Revocations RevocationLookup

	// TrustedIssuers is the set of issuer values the verifier accepts.
	// Tokens whose `iss` claim is not in the set are rejected with
	// ErrInvalidToken.
	TrustedIssuers []string

	// KeyIssuers binds each kid to the one issuer allowed to sign with it:
	// a token whose kid is absent from the map, or whose `iss` differs from
	// the kid's binding, is rejected. Empty binds no kid: enough with one
	// trusted issuer. With several it is required, unless SharedKeys says otherwise:
	// any trusted key signing for any trusted issuer is wrong for issuers
	// that do not share a trust boundary, so it is never the default.
	KeyIssuers map[string]string

	// SharedKeys lets every trusted key sign for every trusted issuer, for
	// several issuers that do share one trust boundary. It and KeyIssuers
	// exclude each other.
	SharedKeys bool

	// Now is the time function the verifier uses for nbf/exp. Tests
	// override it; production passes time.Now.
	Now func() time.Time

	// Leeway widens the nbf / exp window to absorb clock skew between
	// issuer and verifier. Zero: DefaultLeeway; negative or past MaxLeeway
	// is refused.
	Leeway time.Duration
	// NoLeeway holds nbf and exp to the verifier's clock exactly, for an
	// issuer that shares it. It excludes a Leeway.
	NoLeeway bool

	// MaxTokenBytes rejects longer tokens before decoding them. Default
	// DefaultMaxTokenBytes when zero; negative is refused.
	MaxTokenBytes int

	// Biscuit accepts the Biscuit form (Issuer.Biscuit, Attenuate) beside
	// the JWT, as it says. Nil: a Biscuit is refused like any malformed
	// token.
	Biscuit *BiscuitConfig

	// AcceptTokenTypes are the token formats (the JWT "typ") the verifier
	// accepts. Empty: every type this package reads, TokenTypeV5 alone. A new format ships so: verifiers accept it first,
	// issuers mint it after (SignerConfig.TokenType). A type this package
	// has no schema for is refused: a verifier cannot enforce a format it
	// cannot read.
	AcceptTokenTypes []TokenType

	// DPoP checks the proof of possession a key-bound capability (one with
	// a ConfirmationJKT) must come with. Without it the verifier refuses
	// every key-bound capability with ErrProofUnsupported, rather than accept
	// one as a bearer token. Built by dpop.NewVerifier, with its own clock.
	DPoP *dpop.Verifier

	// Realm names the protection space in every WWW-Authenticate challenge
	// ErrorResponse gives (RFC 9110 §11.5). Empty: DefaultRealm. Only the
	// characters RFC 6750 §3 allows in an auth-param value.
	Realm string
}

// BiscuitConfig is how a verifier reads the Biscuit form
// (VerifierConfig.Biscuit).
type BiscuitConfig struct {
	// Revocations answers whether a copy of a Biscuit is revoked on its
	// own. Required; wrap the store in a BiscuitRevocationCache.
	Revocations BiscuitRevocationLookup

	// MeterCopies admits Biscuits whose attenuation blocks set a copy's own
	// request or budget limits (Attenuation.Requests, Attenuation.Budget).
	// Set it only when the Meter that enforces caveats counts
	// the copies a MeterSubject carries; off, such a token is refused with
	// ErrCopyCountersNotMetered rather than accepted with its limits unkept
	// — once it has passed every other check, so a token refused for what
	// it is gets that refusal instead.
	MeterCopies bool
}

// DefaultRealm is the realm of a verifier whose config names none.
const DefaultRealm = "limes"

// VerifyRequest is a token as it arrived, and what it arrived for.
type VerifyRequest struct {
	// Token is the capability token, a JWT or a Biscuit.
	Token string
	// Audience is the service the call is hitting: the token's audience
	// must include it.
	Audience string
	// DPoP is the request's proof of possession, needed when the
	// capability is key-bound and ignored when it is not.
	DPoP dpop.Proof
	// Scheme is the Authorization scheme the token came under
	// (ParseAuthorization). Set, it must match the binding: DPoP for a
	// key-bound capability, Bearer for any other.
	Scheme AuthScheme
}

// AuthorizeRequest is a VerifyRequest and the operation it is presented for.
type AuthorizeRequest struct {
	// Verify is the token and how it was presented.
	Verify VerifyRequest
	// Check is what the token is asked to allow.
	Check CheckRequest
	// ClientAddr is the client address the consumer observed. A capability
	// constrained to source networks refuses the zero address.
	ClientAddr netip.Addr
}

// Verifier is the production verifier — signature first, then
// claims, then issuer / tenant / time / audience / revocation gates. A
// deployment typically resolves keys through a cached JWKS (jwks.Remote) and
// checks revocation through a short-TTL cache (RevocationCache), so
// revocation propagates quickly without hammering the store. A consumer that
// mocks it declares the narrow interface it needs.
//
// Verification is independent of the persistence layer apart from the
// revocation lookup, so the verifier scales horizontally without contention
// on the capability records.
type Verifier struct {
	cfg VerifierConfig
}

// NewVerifier validates wiring and builds the verifier.
func NewVerifier(cfg VerifierConfig) (*Verifier, error) {
	if cfg.Keys == nil {
		return nil, errors.New("limes: VerifierConfig.Keys required")
	}
	if cfg.Revocations == nil {
		return nil, errors.New("limes: VerifierConfig.Revocations required")
	}
	if len(cfg.TrustedIssuers) == 0 {
		return nil, errors.New("limes: VerifierConfig.TrustedIssuers required")
	}
	if cfg.Biscuit != nil {
		b := *cfg.Biscuit // the caller's struct stays the caller's
		cfg.Biscuit = &b
	}
	if cfg.Biscuit != nil && cfg.Biscuit.Revocations == nil {
		return nil, errNoBiscuitRevocations
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Leeway < 0 || cfg.Leeway > MaxLeeway {
		return nil, fmt.Errorf("limes: VerifierConfig.Leeway %s is outside 0..%s", cfg.Leeway, MaxLeeway)
	}
	if cfg.MaxTokenBytes < 0 {
		return nil, fmt.Errorf("limes: VerifierConfig.MaxTokenBytes %d is negative", cfg.MaxTokenBytes)
	}
	switch {
	case cfg.NoLeeway && cfg.Leeway != 0:
		return nil, fmt.Errorf("limes: VerifierConfig.Leeway %s beside NoLeeway", cfg.Leeway)
	case cfg.NoLeeway:
		// Leeway stays 0: the window is exact.
	case cfg.Leeway == 0:
		cfg.Leeway = DefaultLeeway
	}
	if cfg.MaxTokenBytes == 0 {
		cfg.MaxTokenBytes = DefaultMaxTokenBytes
	}
	if strings.ContainsFunc(cfg.Realm, func(r rune) bool { return !authParamChar(r) }) {
		return nil, fmt.Errorf("limes: VerifierConfig.Realm %q holds a character RFC 6750 does not allow", cfg.Realm)
	}
	if cfg.Realm == "" {
		cfg.Realm = DefaultRealm
	}
	cfg.TrustedIssuers = slices.Clone(cfg.TrustedIssuers)
	switch {
	case len(cfg.KeyIssuers) > 0 && cfg.SharedKeys:
		return nil, errors.New("limes: VerifierConfig.KeyIssuers and SharedKeys exclude each other")
	case len(cfg.TrustedIssuers) > 1 && len(cfg.KeyIssuers) == 0 && !cfg.SharedKeys:
		return nil, errors.New("limes: VerifierConfig trusts several issuers; bind each kid to one (KeyIssuers) or set SharedKeys")
	}
	cfg.KeyIssuers = maps.Clone(cfg.KeyIssuers)
	if len(cfg.KeyIssuers) == 0 {
		cfg.KeyIssuers = nil // no binding, whichever way it was spelled
	}
	if len(cfg.AcceptTokenTypes) == 0 {
		cfg.AcceptTokenTypes = slices.Clone(tokenTypes)
	}
	for _, typ := range cfg.AcceptTokenTypes {
		if !slices.Contains(tokenTypes, typ) {
			return nil, fmt.Errorf("limes: VerifierConfig.AcceptTokenTypes names %q, which is not one of %q", typ, tokenTypes)
		}
	}
	cfg.AcceptTokenTypes = slices.Clone(cfg.AcceptTokenTypes)
	return &Verifier{cfg: cfg}, nil
}

// Verify checks the token itself: its signature, issuer and tenant, its
// validity window, its audience, the proof of possession a key-bound
// capability needs, and revocation. It returns the capability the token
// grants; a refusal is one of the package's sentinels, so the caller can
// branch on the failure mode.
//
// Order matters: nothing in the claims is read until the signature over
// them has verified, so a forged JWT costs a header decode and one signature
// check, and no claim-parsing path is reachable by an attacker. A Biscuit is
// read further before its signatures are checked: its container and every
// block are decoded and their facts read as data, which MaxTokenBytes and
// MaxBiscuitBlocks bound; none of it is trusted until the signatures have
// verified. The checks that need no I/O come before the revocation lookup —
// the proof of possession among them, so a holder of a key-bound token
// without its key learns nothing of the token's revocation and causes no
// lookup — and the proof's id is recorded in the replay cache last, so
// neither a revoked token nor a copy this verifier cannot meter records one.
func (v *Verifier) Verify(ctx context.Context, req VerifyRequest) (VerifiedCapability, error) {
	return v.verify(ctx, req, nil)
}

// verify is Verify with accept, when set, run on the capability after every
// check of the token and before the proof's id is recorded: a request accept
// refuses spends none of the replay cache's room either.
func (v *Verifier) verify(ctx context.Context, req VerifyRequest, accept func(Capability) error) (VerifiedCapability, error) {
	token, audience := req.Token, req.Audience
	if token == "" {
		return VerifiedCapability{}, ErrNoCredentials
	}
	if audience == "" {
		// The server's own call names no audience: its error, not the token's.
		return VerifiedCapability{}, invalidRequest("VerifyRequest.Audience is required")
	}
	if len(token) > v.cfg.MaxTokenBytes {
		return VerifiedCapability{}, fmt.Errorf("%w: token is %d bytes (limit %d)",
			ErrInvalidToken, len(token), v.cfg.MaxTokenBytes)
	}

	var cap *grant
	var biscuitIDs [][]byte // set for a Biscuit, from its verified chain
	if isBiscuit(token) {
		if v.cfg.Biscuit == nil {
			return VerifiedCapability{}, fmt.Errorf("%w: Biscuit tokens are not accepted here", ErrInvalidToken)
		}
		var err error
		if cap, biscuitIDs, err = v.verifyBiscuit(ctx, token); err != nil {
			return VerifiedCapability{}, err
		}
	} else {
		var err error
		if cap, _, err = v.verifySigned(ctx, token); err != nil {
			return VerifiedCapability{}, err
		}
		// The token sealed inside a Biscuit, lifted out to shed the
		// Biscuit's attenuation.
		if cap.biscuitRoot != "" {
			return VerifiedCapability{}, fmt.Errorf("%w: token is sealed in a Biscuit; present the Biscuit", ErrInvalidToken)
		}
	}

	// 4) Time window. Leeway absorbs clock skew on both ends.
	now := v.cfg.Now()
	if !cap.NotBefore.IsZero() && now.Add(v.cfg.Leeway).Before(cap.NotBefore) {
		return VerifiedCapability{}, ErrNotYetValid
	}
	// RFC 7519 §4.1.4: refused on or after exp, less the leeway.
	if cap.ExpiresAt.IsZero() || !now.Add(-v.cfg.Leeway).Before(cap.ExpiresAt) {
		return VerifiedCapability{}, ErrExpired
	}

	// 5) Audience match.
	if !slices.Contains(cap.Audience, audience) {
		return VerifiedCapability{}, fmt.Errorf("%w: token audience %v lacks %q",
			ErrAudienceMismatch, cap.Audience, audience)
	}

	// 6) The scheme the binding calls for, and a verifier able to check the
	// proof it needs. No I/O, so before the revocation lookup.
	if err := checkScheme(req.Scheme, cap.ConfirmationJKT != ""); err != nil {
		return VerifiedCapability{}, err
	}
	if cap.ConfirmationJKT != "" && v.cfg.DPoP == nil {
		return VerifiedCapability{}, ErrProofUnsupported
	}
	// 7) Proof of possession, for a key-bound capability — over the token
	// as presented, the Biscuit when it is one. Everything but the replay
	// check, which needs the cache and comes last.
	var proof dpop.CheckedProof
	if cap.ConfirmationJKT != "" {
		var err error
		proof, err = v.cfg.DPoP.Check(ctx, dpop.Request{Proof: req.DPoP, Token: token, JKT: cap.ConfirmationJKT, Holder: cap.ID.String()})
		if err != nil {
			return VerifiedCapability{}, proofError(err)
		}
	}

	// 8) Revocation of this capability or any ancestor, after the checks
	// that need no I/O.
	revoked, err := v.cfg.Revocations.IsRevoked(ctx, cap.ID)
	if err != nil {
		return VerifiedCapability{}, fmt.Errorf("limes: revocation lookup: %w", err)
	}
	if revoked {
		return VerifiedCapability{}, ErrRevoked
	}
	// 9) Revocation of this copy of a Biscuit, or a copy it was attenuated
	// from.
	if biscuitIDs != nil {
		revoked, err := v.cfg.Biscuit.Revocations.IsBiscuitRevoked(ctx, biscuitIDs)
		if err != nil {
			return VerifiedCapability{}, fmt.Errorf("limes: biscuit revocation lookup: %w", err)
		}
		if revoked {
			return VerifiedCapability{}, ErrRevoked
		}
	}

	// 10) A copy's own limits need a Meter that counts copies. After every
	// check of the token, so a token refused for what it is gets that
	// refusal, not this one, which is about the verifier.
	if len(cap.copies) > 0 && !v.cfg.Biscuit.MeterCopies {
		return VerifiedCapability{}, ErrCopyCountersNotMetered
	}
	// 11) The caller's own refusals: Authorize's caveats.
	if accept != nil {
		if err := accept(cap.Capability); err != nil {
			return VerifiedCapability{}, err
		}
	}
	// 12) The proof's id, recorded in the replay cache, last: the cache's
	// room is shared by every copy of the capability, so only a request this
	// verifier accepts spends it — not a revoked token, a copy it cannot
	// meter, nor a request a caveat refuses.
	if cap.ConfirmationJKT != "" {
		if err := v.cfg.DPoP.Record(ctx, proof); err != nil {
			return VerifiedCapability{}, proofError(err)
		}
	}

	return VerifiedCapability{c: cap.Capability, copies: cap.copies, fromBiscuit: biscuitIDs != nil}, nil
}

// DPoPNonce is the nonce to send in the DPoP-Nonce header — with a
// response refused by dpop.ErrNonceRequired, and with any other for the
// client's next proof. Empty when the verifier issues none.
func (v *Verifier) DPoPNonce(ctx context.Context) (string, error) {
	if v.cfg.DPoP == nil {
		return "", nil
	}
	return v.cfg.DPoP.Nonce(ctx)
}

// Authorize is Verify followed by every caveat that can be checked without
// state: the operation, the resource, the idempotency key, the tainted read
// (Caveats.Check), and the client's address (Caveats.CheckSource). The
// request and budget limits are stateful: the Meter enforces them, through
// Bump and Charge, inside the consumer's own transaction. The caveats are
// checked before the proof's id is recorded, so a presenter they refuse —
// one outside the allowed networks, say — spends none of the replay cache's
// room, which every holder of the capability shares.
func (v *Verifier) Authorize(ctx context.Context, req AuthorizeRequest) (VerifiedCapability, error) {
	return v.verify(ctx, req.Verify, func(c Capability) error {
		if err := c.Caveats.CheckSource(req.ClientAddr); err != nil {
			return err
		}
		return c.Caveats.Check(req.Check)
	})
}

// verifyBiscuit checks a Biscuit's sealed token as verifySigned does, then its
// signature chain and attenuation blocks. It returns the capability the
// Biscuit grants, copy limits included, and its revocation ids, authority
// first.
func (v *Verifier) verifyBiscuit(ctx context.Context, token string) (*grant, [][]byte, error) {
	inner, attenuate, err := openBiscuit(token)
	if err != nil {
		return nil, nil, err
	}
	sealed, typ, err := v.verifySigned(ctx, inner)
	if err != nil {
		return nil, nil, err
	}
	if sealed.biscuitRoot == "" {
		return nil, nil, fmt.Errorf("%w: biscuit seals an ordinary token", ErrInvalidToken)
	}
	return attenuate(sealed, typ)
}

// verifySigned checks a compact JWT's header, signature, issuer and tenant —
// everything about the token itself; the time, audience and revocation gates
// run in Verify on the capability the token finally grants. It returns the
// token's type too, under whose rules a Biscuit sealing it is read.
func (v *Verifier) verifySigned(ctx context.Context, token string) (*grant, TokenType, error) {
	// 1) Header: structure, algorithm, type, key id.
	parts, err := jose.Split(token)
	if err != nil {
		return nil, "", refuseToken(ErrInvalidToken, err)
	}
	header, err := parseHeader(parts.Header)
	if err != nil {
		return nil, "", refuseToken(ErrInvalidToken, err)
	}
	if !slices.Contains(v.cfg.AcceptTokenTypes, TokenType(header.Typ)) {
		return nil, "", fmt.Errorf("%w: typ %q is not one of %q", ErrInvalidToken, header.Typ, v.cfg.AcceptTokenTypes)
	}
	if header.Kid == "" {
		return nil, "", fmt.Errorf("%w: header missing kid", ErrInvalidToken)
	}

	// 2) Key and signature — the cryptographic gate.
	pub, err := v.cfg.Keys.PublicKey(ctx, header.Kid)
	if err != nil {
		// An unknown kid is the token's fault; a resolver that cannot
		// answer — the issuer's keys out of reach, a store down — is ours.
		if errors.Is(err, jwks.ErrUnknownKID) {
			return nil, "", fmt.Errorf("%w: kid %q: %w", ErrInvalidToken, header.Kid, err)
		}
		return nil, "", fmt.Errorf("limes: resolve kid %q: %w", header.Kid, err)
	}
	if err := verifyParts(parts, pub); err != nil {
		return nil, "", err
	}

	// 3) Claims, now known to be the issuer's.
	typ := TokenType(header.Typ)
	cap, err := parseClaims(parts.Payload, typ)
	if err != nil {
		return nil, "", refuseToken(ErrInvalidToken, err)
	}
	if !slices.Contains(v.cfg.TrustedIssuers, cap.Issuer) {
		return nil, "", fmt.Errorf("%w: untrusted issuer %q", ErrInvalidToken, cap.Issuer)
	}
	if v.cfg.KeyIssuers != nil {
		if bound, ok := v.cfg.KeyIssuers[header.Kid]; !ok || bound != cap.Issuer {
			return nil, "", fmt.Errorf("%w: kid %q may not sign for issuer %q",
				ErrInvalidToken, header.Kid, cap.Issuer)
		}
	}
	if cap.Subject.TenantID == uuid.Nil {
		return nil, "", fmt.Errorf("%w: capability has no tenant", ErrInvalidToken)
	}
	if err := cap.Subject.Validate(); err != nil {
		return nil, "", refuseToken(ErrInvalidToken, err)
	}
	// What Issue refuses to mint, no token may carry: a caveat outside the
	// rules has no one meaning to enforce — a negative limit read as none,
	// an operation nobody defined.
	if err := cap.Caveats.Validate(); err != nil {
		return nil, "", refuseToken(ErrInvalidToken, err)
	}

	return cap, typ, nil
}

// proofRefusals are the dpop errors that refuse a proof; any other error
// from dpop.Verifier is a failure of its replay cache or nonces.
var proofRefusals = []error{dpop.ErrRequired, dpop.ErrInvalid, dpop.ErrReplayed, dpop.ErrNonceRequired}

// proofError is an error from dpop.Verifier as Verify returns it: a refused
// proof in the ErrProofOfPossession family, a failure of the replay cache or
// nonces wrapped as one.
func proofError(err error) error {
	if isProofRefusal(err) {
		return proofRefusal(err)
	}
	return fmt.Errorf("limes: proof of possession: %w", err)
}

func isProofRefusal(err error) bool {
	for _, r := range proofRefusals {
		if errors.Is(err, r) {
			return true
		}
	}
	return false
}

// refuseToken is err as a refusal of the presented token in family: the
// family and the cause's text, but not the cause's own sentinels. A token
// whose caveats do not validate is a refused token, not a malformed request
// — matching ErrInvalidRequest as well would let a consumer answer it as the
// server's fault.
func refuseToken(family, err error) error { return fmt.Errorf("%w: %s", family, err.Error()) }

// proofRefusal is a dpop refusal as this module reads it: a refusal of the
// credentials (ErrProofOfPossession, and so ErrUnauthenticated), with the
// dpop sentinel still matchable.
func proofRefusal(err error) error { return fmt.Errorf("%w: %w", ErrProofOfPossession, err) }

// VerifiedCapability is a capability a Verifier accepted. Only Verify
// and Authorize make one, so a function that takes it — Issuer.Delegate —
// knows the token it came from was checked, rather than trusting a caller to
// have checked it.
type VerifiedCapability struct {
	c Capability
	// copies are the limits a Biscuit copy's blocks set (Copies).
	copies []CopyCeiling
	// fromBiscuit marks one read from a Biscuit copy: what it grants is
	// the copy's, revoked with the copy, which a token minted from it would
	// not be.
	fromBiscuit bool
}

// FromBiscuit reports whether the capability was read from a copy of a
// Biscuit rather than from a JWT. Issuer.Delegate and Issuer.Biscuit take
// only the latter.
func (v VerifiedCapability) FromBiscuit() bool { return v.fromBiscuit }

// Capability is what the token grants: a copy, the caller's to change.
func (v VerifiedCapability) Capability() Capability { return v.c.clone() }

// Copies are the request and budget limits the attenuation blocks of a
// verified Biscuit set, innermost block first; none for a JWT, or a Biscuit
// whose blocks set none. Each is counted on its own, beside the capability's
// counters, by every Meter call its MeterSubject is handed.
func (v VerifiedCapability) Copies() []CopyCeiling { return cloneCopyCeilings(v.copies) }
