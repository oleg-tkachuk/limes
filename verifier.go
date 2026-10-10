package limes

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"maps"
	"net/netip"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"
)

// KeyResolver resolves a JWKS `kid` to the public key the verifier
// should use. StaticKeyResolver holds keys in memory; RemoteJWKSResolver
// fetches them from an issuer's JWKS endpoint with a cache.
type KeyResolver interface {
	// PublicKey returns the Ed25519 public key for the given kid.
	// Returns ErrUnknownKID if the key set does not contain the kid;
	// the verifier maps that to ErrInvalidSignature so an unknown
	// kid never short-circuits to "trusted".
	PublicKey(ctx context.Context, kid string) (ed25519.PublicKey, error)
}

// ErrUnknownKID is the typed not-found return from KeyResolver. The
// verifier converts it to ErrInvalidSignature for caller-facing errors;
// keeping the typed return lets ops dashboards distinguish "we don't
// know this kid" from "signature mismatch".
var ErrUnknownKID = errors.New("limes: unknown kid")

// StaticKeyResolver is the in-memory implementation used in tests and
// single-node deploys. Maps kid → public key; concurrency-safe via a
// sync.RWMutex so rotation can swap a kid without restarting callers.
type StaticKeyResolver struct {
	mu   sync.RWMutex
	keys map[string]ed25519.PublicKey
}

// NewStaticKeyResolver builds a resolver from a kid → public key map.
// The map is copied to defend against caller mutation.
func NewStaticKeyResolver(keys map[string]ed25519.PublicKey) *StaticKeyResolver {
	return &StaticKeyResolver{keys: maps.Clone(keys)}
}

// PublicKey implements KeyResolver.
func (r *StaticKeyResolver) PublicKey(_ context.Context, kid string) (ed25519.PublicKey, error) {
	r.mu.RLock()
	k, ok := r.keys[kid]
	r.mu.RUnlock()
	if !ok {
		return nil, ErrUnknownKID
	}
	return k, nil
}

// SetKey adds or replaces a kid → key mapping. Rotation step 1: publish
// the new key alongside the old.
func (r *StaticKeyResolver) SetKey(kid string, key ed25519.PublicKey) {
	r.mu.Lock()
	if r.keys == nil {
		r.keys = make(map[string]ed25519.PublicKey)
	}
	r.keys[kid] = key
	r.mu.Unlock()
}

// RemoveKey withdraws a kid. Rotation step 3: call it only once the longest
// outstanding TTL signed under that key has elapsed.
func (r *StaticKeyResolver) RemoveKey(kid string) {
	r.mu.Lock()
	delete(r.keys, kid)
	r.mu.Unlock()
}

// Keys returns a snapshot of the current key set, e.g. for MarshalJWKS.
func (r *StaticKeyResolver) Keys() map[string]ed25519.PublicKey {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return maps.Clone(r.keys)
}

// DefaultMaxTokenBytes bounds the token a verifier will look at. A capability
// with a few dozen resource entries is a couple of kilobytes; anything far
// larger is refused before any decoding is spent on it.
const DefaultMaxTokenBytes = 16 << 10

// defaultLeeway is VerifierConfig.Leeway when unset.
const defaultLeeway = 30 * time.Second

// VerifierConfig wires the verifier. Keys, Revocations and TrustedIssuers
// are required.
type VerifierConfig struct {
	// Keys resolves JWKS kids to Ed25519 public keys.
	Keys KeyResolver

	// Revocations answers whether a capability — or any ancestor in its
	// delegation chain — is revoked. Wrap the store in a
	// CachedRevocationChecker to keep the per-call cost flat.
	Revocations RevocationLookup

	// TrustedIssuers is the set of issuer values the verifier accepts.
	// Tokens whose `iss` claim is not in the set are rejected with
	// ErrInvalidSignature.
	TrustedIssuers []string

	// KeyIssuers, when non-nil, binds each kid to the one issuer allowed
	// to sign with it: a token whose kid is absent from the map, or whose
	// `iss` differs from the kid's binding, is rejected. Without it any
	// trusted key may sign for any trusted issuer, which is fine for one
	// issuer and wrong for several that do not share a trust boundary.
	KeyIssuers map[string]string

	// Now is the time function the verifier uses for nbf/exp. Tests
	// override it; production passes time.Now.
	Now func() time.Time

	// Leeway widens the nbf / exp window to absorb clock skew between
	// issuer and verifier. Default 30s when zero.
	Leeway time.Duration

	// MaxTokenBytes rejects longer tokens before decoding them. Default
	// DefaultMaxTokenBytes when zero.
	MaxTokenBytes int

	// AcceptBiscuit accepts the Biscuit form (Issuer.Biscuit, Attenuate)
	// beside the JWT. Off, a Biscuit is refused like any malformed token.
	AcceptBiscuit bool

	// MeterCopies admits Biscuits whose attenuation blocks set a copy's own
	// request or budget limits (Attenuation.MaxRequests, MaxBudgetMicros).
	// Set it only when the Meter that enforces caveats counts
	// BumpRequest.Copies and the like; off, such a token is refused with
	// ErrCopyCountersNotMetered rather than accepted with its limits unkept.
	MeterCopies bool

	// BiscuitRevocations answers whether a copy of a Biscuit is revoked on
	// its own. Required when AcceptBiscuit is set; wrap the store in a
	// CachedBiscuitRevocationChecker.
	BiscuitRevocations BiscuitRevocationLookup

	// DPoP checks the proof of possession a key-bound capability (one with
	// a ConfirmationJKT) must come with. Without it the verifier refuses
	// every key-bound capability with ErrDPoPRequired, rather than accept
	// one as a bearer token.
	DPoP *DPoPVerifier
}

// Verifier validates tokens. A deployment typically resolves keys through a
// cached JWKS (RemoteJWKSResolver) and checks revocation through a short-TTL
// cache (CachedRevocationChecker), so revocation propagates quickly without
// hammering the store.
type Verifier interface {
	// Verify checks the token itself: its signature, issuer and tenant, its
	// validity window, its audience, the proof of possession a key-bound
	// capability needs, and revocation. It returns the capability the token
	// grants.
	Verify(ctx context.Context, req VerifyRequest) (Capability, error)

	// Authorize is Verify followed by every caveat that can be checked
	// without state: the operation, the resource, the idempotency key, the
	// tainted read (Caveats.Check), and the client's address
	// (Caveats.CheckSource). The request and budget limits are stateful:
	// the Meter enforces them, through Bump and Charge, inside the
	// consumer's own transaction.
	Authorize(ctx context.Context, req AuthorizeRequest) (Capability, error)
}

// VerifyRequest is a token as it arrived, and what it arrived for.
type VerifyRequest struct {
	// Token is the capability token, a JWT or a Biscuit.
	Token string
	// Audience is the service the call is hitting: the token's audience
	// must include it.
	Audience string
	// DPoP is the request's proof of possession, needed when the
	// capability is key-bound and ignored when it is not.
	DPoP DPoPProof
	// Scheme is the Authorization scheme the token came under
	// (ParseAuthorization). Set, it must match the binding: DPoP for a
	// key-bound capability, Bearer for any other.
	Scheme AuthScheme
}

// AuthorizeRequest is a VerifyRequest and the operation it is presented for.
type AuthorizeRequest struct {
	VerifyRequest
	CheckRequest
	// ClientAddr is the client address the consumer observed. A capability
	// constrained to source networks refuses the zero address.
	ClientAddr netip.Addr
}

// StandardVerifier is the production verifier — signature first, then
// claims, then issuer / tenant / time / audience / revocation gates.
//
// Verification is independent of the persistence layer apart from the
// revocation lookup, so the verifier scales horizontally without contention
// on the capability records.
type StandardVerifier struct {
	cfg VerifierConfig
}

// NewStandardVerifier validates wiring and builds the verifier.
func NewStandardVerifier(cfg VerifierConfig) (*StandardVerifier, error) {
	if cfg.Keys == nil {
		return nil, errors.New("limes: VerifierConfig.Keys required")
	}
	if cfg.Revocations == nil {
		return nil, errors.New("limes: VerifierConfig.Revocations required")
	}
	if len(cfg.TrustedIssuers) == 0 {
		return nil, errors.New("limes: VerifierConfig.TrustedIssuers required")
	}
	if cfg.AcceptBiscuit && cfg.BiscuitRevocations == nil {
		return nil, errNoBiscuitRevocations
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.DPoP != nil {
		if cfg.DPoP.Replay == nil {
			return nil, errNoReplayCache
		}
		dpop := *cfg.DPoP // the caller's stays theirs
		if dpop.Now == nil {
			dpop.Now = cfg.Now
		}
		cfg.DPoP = &dpop
	}
	if cfg.Leeway == 0 {
		cfg.Leeway = defaultLeeway
	}
	if cfg.MaxTokenBytes == 0 {
		cfg.MaxTokenBytes = DefaultMaxTokenBytes
	}
	cfg.TrustedIssuers = slices.Clone(cfg.TrustedIssuers)
	cfg.KeyIssuers = maps.Clone(cfg.KeyIssuers)
	return &StandardVerifier{cfg: cfg}, nil
}

// Verify implements Verifier. Returns the typed Capability on success.
// Errors return one of the package's typed sentinels so the caller can
// branch on the failure mode.
//
// Order matters: nothing in the claims is read until the signature over
// them has verified, so a forged token costs a header decode and one
// signature check, and no claim-parsing path is reachable by an attacker.
// The checks that need no I/O come before the revocation lookup.
func (v *StandardVerifier) Verify(ctx context.Context, req VerifyRequest) (Capability, error) {
	token, audience := req.Token, req.Audience
	if audience == "" {
		return Capability{}, fmt.Errorf("%w: no audience to verify against", ErrAudienceMismatch)
	}
	cap, biscuitIDs, err := v.verifyToken(ctx, token)
	if err != nil {
		return Capability{}, err
	}

	// 5) Audience match.
	if !slices.Contains(cap.Audience, audience) {
		return Capability{}, fmt.Errorf("%w: token audience %v lacks %q",
			ErrAudienceMismatch, cap.Audience, audience)
	}

	// 6) Proof of possession, for a key-bound capability — over the token
	// as presented, the Biscuit when it is one — under the scheme its
	// binding calls for.
	if err := checkScheme(req.Scheme, cap.ConfirmationJKT != ""); err != nil {
		return Capability{}, err
	}
	if cap.ConfirmationJKT != "" {
		if v.cfg.DPoP == nil {
			return Capability{}, fmt.Errorf("%w: capability is key-bound and this verifier has no DPoP configured", ErrDPoPRequired)
		}
		if err := v.cfg.DPoP.check(ctx, *cap, token, req.DPoP); err != nil {
			return Capability{}, err
		}
	}

	// 7) Revocation. Last so cheap rejections short-circuit before hitting
	// the cache / store.
	if err := v.checkRevoked(ctx, cap.ID, biscuitIDs); err != nil {
		return Capability{}, err
	}
	return *cap, nil
}

// Introspect checks token as Verify does, except for the two checks that
// belong to the request it came with — its audience and its proof of
// possession — which the resource server that received it makes: what an
// RFC 7662 introspection endpoint reports on.
func (v *StandardVerifier) Introspect(ctx context.Context, token string) (Capability, error) {
	cap, biscuitIDs, err := v.verifyToken(ctx, token)
	if err != nil {
		return Capability{}, err
	}
	if err := v.checkRevoked(ctx, cap.ID, biscuitIDs); err != nil {
		return Capability{}, err
	}
	return *cap, nil
}

// verifyToken checks the token itself: identify's checks, and its validity
// window. It returns the capability and, for a Biscuit, its revocation ids.
func (v *StandardVerifier) verifyToken(ctx context.Context, token string) (*Capability, [][]byte, error) {
	cap, biscuitIDs, err := v.identify(ctx, token)
	if err != nil {
		return nil, nil, err
	}
	// 4) Time window. Leeway absorbs clock skew on both ends.
	now := v.cfg.Now()
	if !cap.NotBefore.IsZero() && now.Add(v.cfg.Leeway).Before(cap.NotBefore) {
		return nil, nil, ErrNotYetValid
	}
	if cap.ExpiresAt.IsZero() || now.Add(-v.cfg.Leeway).After(cap.ExpiresAt) {
		return nil, nil, ErrExpired
	}
	return cap, biscuitIDs, nil
}

// identify checks that token is one this verifier's issuers signed — its
// size, signature, issuer and tenant, and a Biscuit's chain — whatever its
// validity window: what a token must prove before it may be revoked.
func (v *StandardVerifier) identify(ctx context.Context, token string) (*Capability, [][]byte, error) {
	if token == "" {
		return nil, nil, fmt.Errorf("%w: no token", ErrInvalidSignature)
	}
	if len(token) > v.cfg.MaxTokenBytes {
		return nil, nil, fmt.Errorf("%w: token is %d bytes (limit %d)",
			ErrInvalidSignature, len(token), v.cfg.MaxTokenBytes)
	}

	var cap *Capability
	var biscuitIDs [][]byte // set for a Biscuit, from its verified chain
	if IsBiscuit(token) {
		if !v.cfg.AcceptBiscuit {
			return nil, nil, fmt.Errorf("%w: Biscuit tokens are not accepted here", ErrInvalidSignature)
		}
		var err error
		if cap, biscuitIDs, err = v.verifyBiscuit(ctx, token, v.cfg.MeterCopies); err != nil {
			return nil, nil, err
		}
	} else {
		var err error
		if cap, err = v.verifySigned(ctx, token); err != nil {
			return nil, nil, err
		}
		// The token sealed inside a Biscuit, lifted out to shed the
		// Biscuit's attenuation.
		if cap.BiscuitRoot != "" {
			return nil, nil, fmt.Errorf("%w: token is sealed in a Biscuit; present the Biscuit", ErrInvalidSignature)
		}
	}
	return cap, biscuitIDs, nil
}

// checkRevoked refuses a capability revoked itself or through an ancestor,
// and a Biscuit copy revoked itself or through the copy it was attenuated
// from.
func (v *StandardVerifier) checkRevoked(ctx context.Context, id uuid.UUID, biscuitIDs [][]byte) error {
	revoked, err := v.cfg.Revocations.IsRevoked(ctx, id)
	if err != nil {
		return fmt.Errorf("limes: revocation lookup: %w", err)
	}
	if revoked {
		return ErrRevoked
	}
	if biscuitIDs != nil {
		revoked, err := v.cfg.BiscuitRevocations.IsBiscuitRevoked(ctx, biscuitIDs)
		if err != nil {
			return fmt.Errorf("limes: biscuit revocation lookup: %w", err)
		}
		if revoked {
			return ErrRevoked
		}
	}
	return nil
}

// DPoPNonce is the nonce to send in the DPoP-Nonce header — with a
// response refused by ErrDPoPNonceRequired, and with any other for the
// client's next proof. Empty when the verifier issues none.
func (v *StandardVerifier) DPoPNonce(ctx context.Context) (string, error) {
	if v.cfg.DPoP == nil || v.cfg.DPoP.Nonces == nil {
		return "", nil
	}
	return v.cfg.DPoP.Nonces.Nonce(ctx)
}

// Authorize implements Verifier.
func (v *StandardVerifier) Authorize(ctx context.Context, req AuthorizeRequest) (Capability, error) {
	c, err := v.Verify(ctx, req.VerifyRequest)
	if err != nil {
		return Capability{}, err
	}
	if err := c.Caveats.CheckSource(req.ClientAddr); err != nil {
		return Capability{}, err
	}
	if err := c.Caveats.Check(req.CheckRequest); err != nil {
		return Capability{}, err
	}
	return c, nil
}

// verifyBiscuit checks a Biscuit's sealed token as verifySigned does, then its
// signature chain and attenuation blocks. It returns the capability the
// Biscuit grants and its revocation ids, authority first. meterCopies admits
// blocks that set a copy's own limits.
func (v *StandardVerifier) verifyBiscuit(ctx context.Context, token string, meterCopies bool) (*Capability, [][]byte, error) {
	inner, attenuate, err := openBiscuit(token)
	if err != nil {
		return nil, nil, err
	}
	sealed, err := v.verifySigned(ctx, inner)
	if err != nil {
		return nil, nil, err
	}
	if sealed.BiscuitRoot == "" {
		return nil, nil, fmt.Errorf("%w: biscuit seals an ordinary token", ErrInvalidSignature)
	}
	return attenuate(sealed, meterCopies)
}

// verifySigned checks a compact JWT's header, signature, issuer and tenant —
// everything about the token itself; the time, audience and revocation gates
// run in Verify on the capability the token finally grants.
func (v *StandardVerifier) verifySigned(ctx context.Context, token string) (*Capability, error) {
	// 1) Header: structure, algorithm, type, key id.
	parts, err := splitToken(token)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidSignature, err)
	}
	header, err := parseHeader(parts.header)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidSignature, err)
	}
	if header.Typ != TokenType {
		return nil, fmt.Errorf("%w: typ %q is not %q", ErrInvalidSignature, header.Typ, TokenType)
	}
	if header.Kid == "" {
		return nil, fmt.Errorf("%w: header missing kid", ErrInvalidSignature)
	}

	// 2) Key and signature — the cryptographic gate.
	pub, err := v.cfg.Keys.PublicKey(ctx, header.Kid)
	if err != nil {
		// Surface unknown-kid as invalid signature so callers don't
		// branch on the failure mode in security-sensitive paths.
		return nil, fmt.Errorf("%w: kid %q: %w", ErrInvalidSignature, header.Kid, err)
	}
	if err := verifyParts(parts, pub); err != nil {
		return nil, err
	}

	// 3) Claims, now known to be the issuer's.
	cap, err := parseClaims(parts.claims)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidSignature, err)
	}
	if !slices.Contains(v.cfg.TrustedIssuers, cap.Issuer) {
		return nil, fmt.Errorf("%w: untrusted issuer %q", ErrInvalidSignature, cap.Issuer)
	}
	if v.cfg.KeyIssuers != nil {
		if bound, ok := v.cfg.KeyIssuers[header.Kid]; !ok || bound != cap.Issuer {
			return nil, fmt.Errorf("%w: kid %q may not sign for issuer %q",
				ErrInvalidSignature, header.Kid, cap.Issuer)
		}
	}
	if cap.Subject.TenantID == uuid.Nil {
		return nil, fmt.Errorf("%w: capability has no tenant", ErrInvalidSignature)
	}

	return cap, nil
}
