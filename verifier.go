package limes

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/netip"
	"slices"
	"strings"
	"time"
	"uuid"

	"github.com/oleg-tkachuk/limes/dpop"
	"github.com/oleg-tkachuk/limes/internal/errs"
	"github.com/oleg-tkachuk/limes/internal/jose"
	"github.com/oleg-tkachuk/limes/jwks"
)

// KeyResolver resolves a JWKS `kid` to the public key the verifier
// should use. jwks.Static holds keys in memory; jwks.Remote
// fetches them from an issuer's JWKS endpoint with a cache.
//
// Keys are Ed25519 for all of 1.x: the token format, not the key, names
// the algorithm, so another algorithm would be another format (limes_v)
// rather than a key this interface returns.
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
	// One key signs for two issuers — a family of capabilities marked by
	// an iss of its own — under two kids, each bound here to its issuer:
	// two Issuers with the same key, each with its kid and IssuerName.
	// SharedKeys would let any trusted key sign either iss, which defeats
	// the mark.
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

	// DPoP checks the proof of possession a key-bound capability (one with
	// a ConfirmationJKT) must come with. Without it the verifier refuses
	// every key-bound capability with ErrProofUnsupported, rather than accept
	// one as a bearer token. Built by dpop.NewVerifier, with its own clock.
	DPoP *dpop.Verifier

	// Realm names the protection space in every WWW-Authenticate challenge
	// ErrorResponse gives (RFC 9110 §11.5). Empty: DefaultRealm. Only the
	// characters RFC 6750 §3 allows in an auth-param value.
	Realm string

	// Logger logs what ErrorResponse cannot answer the way the refusal
	// asks: a nonce the DPoP verifier cannot give. Nil: slog.Default().
	Logger *slog.Logger
}

// BiscuitConfig is how a verifier reads the Biscuit form
// (VerifierConfig.Biscuit).
type BiscuitConfig struct {
	// Revocations answers whether a copy of a Biscuit is revoked on its
	// own. Required; wrap the store in the RevocationCache that
	// VerifierConfig.Revocations holds, with RevocationCacheConfig.Biscuits
	// set. NewVerifier refuses a *RevocationCache without it, or a nil one;
	// a type of yours that wraps such a cache is not seen through, and
	// refuses every Biscuit at Verify instead.
	Revocations BiscuitRevocationLookup

	// Form reads the Biscuit form: github.com/oleg-tkachuk/limes/biscuit's
	// Form. Required.
	Form TokenForm

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
	if err := checkVerifierWiring(cfg); err != nil {
		return nil, err
	}
	biscuitCfg, err := biscuitConfig(cfg.Biscuit)
	if err != nil {
		return nil, err
	}
	cfg.Biscuit = biscuitCfg
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if err := setLeeway(&cfg); err != nil {
		return nil, err
	}
	if err := setMaxTokenBytes(&cfg); err != nil {
		return nil, err
	}
	if err := setRealm(&cfg); err != nil {
		return nil, err
	}
	cfg.TrustedIssuers = slices.Clone(cfg.TrustedIssuers)
	if err := setKeyIssuers(&cfg); err != nil {
		return nil, err
	}
	return &Verifier{cfg: cfg}, nil
}

// checkVerifierWiring refuses a config without its keys, its revocation
// lookup or an issuer to trust.
func checkVerifierWiring(cfg VerifierConfig) error {
	// A nil pointer in an interface is not nil: unchecked, it would pass
	// here and panic at the first token verified.
	switch {
	case isNilValue(cfg.Keys):
		return invalidConfig("limes: VerifierConfig.Keys required")
	case isNilValue(cfg.Revocations):
		return invalidConfig("limes: VerifierConfig.Revocations required")
	case len(cfg.TrustedIssuers) == 0:
		return invalidConfig("limes: VerifierConfig.TrustedIssuers required")
	}
	return nil
}

// biscuitConfig is a copy of b, so the caller's struct stays the caller's,
// refused when it has no revocation lookup for Biscuits; nil for none.
func biscuitConfig(b *BiscuitConfig) (*BiscuitConfig, error) {
	if b == nil {
		return nil, nil
	}
	own := *b
	// A cache answers Biscuit questions only over an upstream for them: the
	// type accepts the call either way, so the gap is refused here rather
	// than at the first Biscuit presented. Any other nil lookup, a nil
	// pointer included, is refused as none.
	if c, ok := own.Revocations.(*RevocationCache); ok && (c == nil || !c.answersBiscuits()) {
		return nil, errNoBiscuitLookup
	}
	if isNilValue(own.Revocations) {
		return nil, errNoBiscuitRevocations
	}
	if isNilValue(own.Form) {
		return nil, errNoBiscuitForm
	}
	return &own, nil
}

// setLeeway refuses a leeway outside 0..MaxLeeway or beside NoLeeway, and
// gives an unset one DefaultLeeway.
func setLeeway(cfg *VerifierConfig) error {
	if cfg.Leeway < 0 || cfg.Leeway > MaxLeeway {
		return invalidConfig("limes: VerifierConfig.Leeway %s is outside 0..%s", cfg.Leeway, MaxLeeway)
	}
	switch {
	case cfg.NoLeeway && cfg.Leeway != 0:
		return invalidConfig("limes: VerifierConfig.Leeway %s beside NoLeeway", cfg.Leeway)
	case cfg.NoLeeway:
		// Leeway stays 0: the window is exact.
	case cfg.Leeway == 0:
		cfg.Leeway = DefaultLeeway
	}
	return nil
}

// setMaxTokenBytes refuses a negative bound and gives an unset one
// DefaultMaxTokenBytes.
func setMaxTokenBytes(cfg *VerifierConfig) error {
	if cfg.MaxTokenBytes < 0 {
		return invalidConfig("limes: VerifierConfig.MaxTokenBytes %d is negative", cfg.MaxTokenBytes)
	}
	if cfg.MaxTokenBytes == 0 {
		cfg.MaxTokenBytes = DefaultMaxTokenBytes
	}
	return nil
}

// setRealm refuses a realm RFC 6750 cannot carry and gives an unset one
// DefaultRealm.
func setRealm(cfg *VerifierConfig) error {
	if strings.ContainsFunc(cfg.Realm, func(r rune) bool { return !authParamChar(r) }) {
		return invalidConfig("limes: VerifierConfig.Realm %q holds a character RFC 6750 does not allow", cfg.Realm)
	}
	if cfg.Realm == "" {
		cfg.Realm = DefaultRealm
	}
	return nil
}

// setKeyIssuers refuses a config that trusts several issuers without saying
// which kid speaks for which, and keeps its own copy of the binding.
func setKeyIssuers(cfg *VerifierConfig) error {
	switch {
	case len(cfg.KeyIssuers) > 0 && cfg.SharedKeys:
		return invalidConfig("limes: VerifierConfig.KeyIssuers and SharedKeys exclude each other")
	case len(cfg.TrustedIssuers) > 1 && len(cfg.KeyIssuers) == 0 && !cfg.SharedKeys:
		return invalidConfig("limes: VerifierConfig trusts several issuers; bind each kid to one (KeyIssuers) or set SharedKeys")
	}
	cfg.KeyIssuers = maps.Clone(cfg.KeyIssuers)
	if len(cfg.KeyIssuers) == 0 {
		cfg.KeyIssuers = nil // no binding, whichever way it was spelled
	}
	return nil
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
	if err := checkVerifyRequest(req); err != nil {
		return VerifiedCapability{}, err
	}
	cap, biscuitIDs, err := v.authenticate(ctx, req.Token)
	if err != nil {
		return VerifiedCapability{}, err
	}
	// 5) Audience match.
	if !slices.Contains(cap.Audience, req.Audience) {
		return VerifiedCapability{}, fmt.Errorf("%w: token audience %v lacks %q",
			ErrAudienceMismatch, cap.Audience, req.Audience)
	}
	// 6–7) The scheme and the proof of possession, which need no I/O.
	proof, err := v.checkPossession(ctx, req, cap)
	if err != nil {
		return VerifiedCapability{}, err
	}
	// 8–9) Revocation, after the checks that need no I/O.
	if err := v.checkRevoked(ctx, cap, biscuitIDs); err != nil {
		return VerifiedCapability{}, err
	}
	// 10–11) What the verifier and its caller require beyond the token.
	if err := v.checkAccepted(cap, accept); err != nil {
		return VerifiedCapability{}, err
	}
	// 12) The proof's id, recorded in the replay cache, last.
	if err := v.recordProof(ctx, cap, proof); err != nil {
		return VerifiedCapability{}, err
	}
	return VerifiedCapability{c: cap.Capability, copies: cap.copies, fromBiscuit: biscuitIDs != nil}, nil
}

// checkVerifyRequest refuses a request with no token, or none of the
// server's audience: the server's own call names no audience, its error, not
// the token's.
func checkVerifyRequest(req VerifyRequest) error {
	if req.Token == "" {
		return ErrNoCredentials
	}
	if req.Audience == "" {
		return invalidRequest("VerifyRequest.Audience is required")
	}
	return nil
}

// checkPossession holds the request to what cap's binding calls for (6): the
// scheme, and a verifier able to check the proof it needs; then, for a
// key-bound capability, checks the proof (7) over the token as presented,
// the Biscuit when it is one. Everything but the replay check, which needs
// the cache and comes last.
func (v *Verifier) checkPossession(ctx context.Context, req VerifyRequest, cap *grant) (dpop.CheckedProof, error) {
	bound := cap.ConfirmationJKT != ""
	if err := checkScheme(req.Scheme, bound); err != nil {
		return dpop.CheckedProof{}, err
	}
	if !bound {
		return dpop.CheckedProof{}, nil
	}
	if v.cfg.DPoP == nil {
		return dpop.CheckedProof{}, ErrProofUnsupported
	}
	proof, err := v.cfg.DPoP.Check(ctx, dpop.Request{Proof: req.DPoP, Token: req.Token, JKT: cap.ConfirmationJKT, Holder: cap.ID.String()})
	if err != nil {
		return dpop.CheckedProof{}, proofError(err)
	}
	return proof, nil
}

// checkAccepted refuses what passes every check of the token but the
// verifier or its caller does not accept: a copy whose own limits need a
// Meter that counts copies (10), after every check of the token, so a token
// refused for what it is gets that refusal, not this one, which is about the
// verifier; then the caller's own refusals (11), Authorize's caveats.
func (v *Verifier) checkAccepted(cap *grant, accept func(Capability) error) error {
	if len(cap.copies) > 0 && !v.cfg.Biscuit.MeterCopies {
		return ErrCopyCountersNotMetered
	}
	if accept == nil {
		return nil
	}
	return accept(cap.Capability)
}

// recordProof records a key-bound capability's proof id in the replay
// cache (12): the cache's room is shared by every copy of the capability, so
// only a request this verifier accepts spends it — not a revoked token, a
// copy it cannot meter, nor a request a caveat refuses.
func (v *Verifier) recordProof(ctx context.Context, cap *grant, proof dpop.CheckedProof) error {
	if cap.ConfirmationJKT == "" {
		return nil
	}
	if err := v.cfg.DPoP.Record(ctx, proof); err != nil {
		return proofError(err)
	}
	return nil
}

// authenticate is what Verify and Introspect check of a token itself: its
// size, its signatures and issuer (1–3), that it is not a token lifted out
// of a Biscuit, and its time window (4). It returns the grant and, for a
// Biscuit, the ids of its verified chain.
func (v *Verifier) authenticate(ctx context.Context, token string) (*grant, [][]byte, error) {
	if token == "" {
		return nil, nil, ErrNoCredentials
	}
	if len(token) > v.cfg.MaxTokenBytes {
		return nil, nil, fmt.Errorf("%w: token is %d bytes (limit %d)",
			ErrInvalidToken, len(token), v.cfg.MaxTokenBytes)
	}
	cap, biscuitIDs, err := v.verifyToken(ctx, token)
	if err != nil {
		return nil, nil, err
	}
	if err := v.checkWindow(cap); err != nil {
		return nil, nil, err
	}
	return cap, biscuitIDs, nil
}

// verifyToken checks a Biscuit's chain or a JWT's signature (1–3), and
// returns the grant and, for a Biscuit, the ids of its verified chain.
func (v *Verifier) verifyToken(ctx context.Context, token string) (*grant, [][]byte, error) {
	if isBiscuit(token) {
		if v.cfg.Biscuit == nil {
			return nil, nil, fmt.Errorf("%w: Biscuit tokens are not accepted here", ErrInvalidToken)
		}
		return v.verifyBiscuit(ctx, token)
	}
	cap, err := v.verifySigned(ctx, token)
	if err != nil {
		return nil, nil, err
	}
	// The token sealed inside a Biscuit, lifted out to shed the
	// Biscuit's attenuation.
	if cap.biscuitRoot != "" {
		return nil, nil, fmt.Errorf("%w: token is sealed in a Biscuit; present the Biscuit", ErrInvalidToken)
	}
	return cap, nil, nil
}

// checkWindow refuses a grant outside its time window (4). Leeway absorbs
// clock skew on both ends.
func (v *Verifier) checkWindow(cap *grant) error {
	now := v.cfg.Now()
	if !cap.NotBefore.IsZero() && now.Add(v.cfg.Leeway).Before(cap.NotBefore) {
		return ErrNotYetValid
	}
	// RFC 7519 §4.1.4: refused on or after exp, less the leeway.
	if cap.ExpiresAt.IsZero() || !now.Add(-v.cfg.Leeway).Before(cap.ExpiresAt) {
		return ErrExpired
	}
	return nil
}

// checkRevoked refuses a capability revoked itself or through any ancestor
// (8), and a Biscuit copy revoked itself or through a copy it was attenuated
// from (9). A lookup that fails is the server's error, not a refusal.
func (v *Verifier) checkRevoked(ctx context.Context, cap *grant, biscuitIDs [][]byte) error {
	revoked, err := v.cfg.Revocations.IsRevoked(ctx, cap.ID)
	if err != nil {
		return errs.Errorf("limes: revocation lookup: %w", err)
	}
	if revoked {
		return ErrRevoked
	}
	if biscuitIDs != nil {
		revoked, err := v.cfg.Biscuit.Revocations.IsBiscuitRevoked(ctx, biscuitIDs)
		if err != nil {
			return errs.Errorf("limes: biscuit revocation lookup: %w", err)
		}
		if revoked {
			return ErrRevoked
		}
	}
	return nil
}

// Introspection is what Introspect says of an active token.
type Introspection struct {
	// Capability is the capability the token carries.
	Capability Capability
	// FromBiscuit reports that the token is a Biscuit.
	FromBiscuit bool
	// Copies are the limits the token's Biscuit blocks set on this copy of
	// the capability, as VerifiedCapability.Copies has them: a copy is held
	// to these as well as to the capability's caveats. Empty for a JWT and a
	// Biscuit whose blocks set none.
	Copies []CopyCeiling
	_      noCompare
}

// Introspect answers an introspection endpoint (RFC 7662) about a token a
// resource server or gateway holds a copy of: nil error if it is active —
// genuine, from a trusted issuer, within its time window and not revoked,
// itself, through an ancestor or, for a Biscuit, through a copy — with the
// capability it carries. It checks neither an audience nor proof of
// possession: a key-bound token is active though no key was proved, and the
// answer says the token is bound, not that its presenter holds the key. A
// token lifted out of the Biscuit it was sealed in is refused, as Verify
// refuses it. So is a Biscuit copy that sets request or budget limits of its
// own, on a verifier without BiscuitConfig.MeterCopies: ErrCopyCountersNotMetered,
// as Verify answers it; with MeterCopies, the answer carries those limits in
// Introspection.Copies. A refusal of the token is ErrUnauthenticated's
// family, which the endpoint answers as inactive; any other error is the
// server's, such as a revocation lookup that failed, and never reads as
// inactive.
//
// Introspect is no authorization: it yields no VerifiedCapability, so its
// answer can be neither delegated from nor checked against a request.
func (v *Verifier) Introspect(ctx context.Context, token string) (Introspection, error) {
	cap, biscuitIDs, err := v.authenticate(ctx, token)
	if err != nil {
		return Introspection{}, err
	}
	if err := v.checkRevoked(ctx, cap, biscuitIDs); err != nil {
		return Introspection{}, err
	}
	// As Verify: a copy whose limits no Meter here counts is not vouched
	// for, so no answer states it narrower in words than it is in effect.
	if len(cap.copies) > 0 && !v.cfg.Biscuit.MeterCopies {
		return Introspection{}, ErrCopyCountersNotMetered
	}
	return Introspection{Capability: cap.Clone(), FromBiscuit: biscuitIDs != nil, Copies: cap.copies}, nil
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
	inner, unseal, err := v.cfg.Biscuit.Form.Open(token)
	if err != nil {
		return nil, nil, formRefusal(err)
	}
	sealed, err := v.verifySigned(ctx, inner)
	if err != nil {
		return nil, nil, err
	}
	if sealed.biscuitRoot == "" {
		return nil, nil, fmt.Errorf("%w: biscuit seals an ordinary token", ErrInvalidToken)
	}
	root, err := sealedRoot(sealed)
	if err != nil {
		return nil, nil, err
	}
	u, err := unseal(root)
	if err != nil {
		return nil, nil, formRefusal(err)
	}
	return unsealedGrant(sealed, u)
}

// verifySigned checks a compact JWT's header, signature, issuer and tenant —
// everything about the token itself; the time, audience and revocation gates
// run in Verify on the capability the token finally grants.
func (v *Verifier) verifySigned(ctx context.Context, token string) (*grant, error) {
	parts, header, err := readSignedHeader(token)
	if err != nil {
		return nil, err
	}
	if err := v.checkSignature(ctx, parts, header.Kid); err != nil {
		return nil, err
	}
	return v.readSignedClaims(parts.Payload, header.Kid)
}

// readSignedHeader splits a compact JWT and reads its header (1): its
// structure, algorithm, type and key id.
func readSignedHeader(token string) (jose.Parts, jwtHeader, error) {
	parts, err := jose.Split(token)
	if err != nil {
		return jose.Parts{}, jwtHeader{}, refuseToken(ErrInvalidToken, err)
	}
	header, err := parseHeader(parts.Header)
	if err != nil {
		return jose.Parts{}, jwtHeader{}, refuseToken(ErrInvalidToken, err)
	}
	if header.Typ != TokenType {
		return jose.Parts{}, jwtHeader{}, fmt.Errorf("%w: typ %q is not %q", ErrInvalidToken, header.Typ, TokenType)
	}
	// A kid outside limes's namespace names a key no limes issuer signs
	// with, and is refused before any key is looked up: at+jwt no longer
	// tells a capability from another access token, and the kid then does.
	if !jwks.IsKeyID(header.Kid) {
		return jose.Parts{}, jwtHeader{}, fmt.Errorf("%w: kid %q is not a limes kid (%q and a name)", ErrInvalidToken, header.Kid, jwks.KeyIDPrefix)
	}
	return parts, header, nil
}

// checkSignature resolves kid and verifies the signature under it (2) —
// the cryptographic gate.
func (v *Verifier) checkSignature(ctx context.Context, parts jose.Parts, kid string) error {
	pub, err := v.cfg.Keys.PublicKey(ctx, kid)
	if err != nil {
		// An unknown kid is the token's fault; a resolver that cannot
		// answer — the issuer's keys out of reach, a store down — is ours.
		if errors.Is(err, jwks.ErrUnknownKID) {
			return errs.Errorf("%w: kid %q: %w", ErrInvalidToken, kid, err)
		}
		return errs.Errorf("limes: resolve kid %q: %w", kid, err)
	}
	return verifyParts(parts, pub)
}

// readSignedClaims reads the claims (3), now known to be signed under kid:
// a trusted issuer kid may sign for, a tenant, and a principal and caveats
// within the rules.
func (v *Verifier) readSignedClaims(payload, kid string) (*grant, error) {
	cap, err := parseClaims(payload)
	if err != nil {
		return nil, refuseToken(ErrInvalidToken, err)
	}
	if err := v.checkIssuer(kid, cap.Issuer); err != nil {
		return nil, err
	}
	if cap.Subject.TenantID == uuid.Nil() {
		return nil, fmt.Errorf("%w: capability has no tenant", ErrInvalidToken)
	}
	if err := cap.Subject.Validate(); err != nil {
		return nil, refuseToken(ErrInvalidToken, err)
	}
	// What Issue refuses to mint, no token may carry: a caveat outside the
	// rules has no one meaning to enforce — a negative limit read as none,
	// an operation nobody defined.
	if err := cap.Caveats.Validate(); err != nil {
		return nil, refuseToken(ErrInvalidToken, err)
	}
	return cap, nil
}

// checkIssuer refuses an issuer the verifier does not trust, or one kid is
// not bound to sign for.
func (v *Verifier) checkIssuer(kid, issuer string) error {
	if !slices.Contains(v.cfg.TrustedIssuers, issuer) {
		return fmt.Errorf("%w: untrusted issuer %q", ErrInvalidToken, issuer)
	}
	if v.cfg.KeyIssuers == nil {
		return nil
	}
	if bound, ok := v.cfg.KeyIssuers[kid]; !ok || bound != issuer {
		return fmt.Errorf("%w: kid %q may not sign for issuer %q", ErrInvalidToken, kid, issuer)
	}
	return nil
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
	return errs.Errorf("limes: proof of possession: %w", err)
}

func isProofRefusal(err error) bool {
	for _, r := range proofRefusals {
		if errors.Is(err, r) {
			return true
		}
	}
	return false
}

// formRefusal is a TokenForm's error as a refusal of the token, by
// refuseToken: ErrBiscuitAttenuation when the form says a block is at fault,
// ErrInvalidToken otherwise.
func formRefusal(err error) error {
	if errors.Is(err, ErrBiscuitAttenuation) {
		return refuseToken(ErrBiscuitAttenuation, err)
	}
	return refuseToken(ErrInvalidToken, err)
}

// refuseToken is err as a refusal of the presented token in family: the
// family and the cause's text, but not the cause's own sentinels. A token
// whose caveats do not validate is a refused token, not a malformed request
// — matching ErrInvalidRequest as well would let a consumer answer it as the
// server's fault. A cause that names family already — a TokenForm's error
// may — keeps its text, so the family is named once.
func refuseToken(family, err error) error {
	if errors.Is(err, family) {
		return &tokenRefusal{family: family, msg: err.Error()}
	}
	return errs.Errorf("%w: %s", family, err.Error())
}

// tokenRefusal is a refusal in family whose text is msg, a cause's that
// already names the family.
type tokenRefusal struct {
	family error
	msg    string
}

func (r *tokenRefusal) Error() string { return r.msg }
func (r *tokenRefusal) Unwrap() error { return r.family }

// proofRefusal is a dpop refusal as this module reads it: a refusal of the
// credentials (ErrProofOfPossession, and so ErrUnauthenticated), with the
// dpop sentinel still matchable.
func proofRefusal(err error) error { return errs.Errorf("%w: %w", ErrProofOfPossession, err) }

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
