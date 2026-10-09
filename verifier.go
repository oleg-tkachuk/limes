package limes

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"maps"
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
var ErrUnknownKID = errors.New("capability: unknown kid")

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
		return nil, errors.New("capability: VerifierConfig.Keys required")
	}
	if cfg.Revocations == nil {
		return nil, errors.New("capability: VerifierConfig.Revocations required")
	}
	if len(cfg.TrustedIssuers) == 0 {
		return nil, errors.New("capability: VerifierConfig.TrustedIssuers required")
	}
	if cfg.AcceptBiscuit && cfg.BiscuitRevocations == nil {
		return nil, errNoBiscuitRevocations
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
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
func (v *StandardVerifier) Verify(ctx context.Context, token string, audience string) (Capability, error) {
	if len(token) > v.cfg.MaxTokenBytes {
		return Capability{}, fmt.Errorf("%w: token is %d bytes (limit %d)",
			ErrInvalidSignature, len(token), v.cfg.MaxTokenBytes)
	}

	var cap *Capability
	var biscuitIDs [][]byte // set for a Biscuit, from its verified chain
	if IsBiscuit(token) {
		if !v.cfg.AcceptBiscuit {
			return Capability{}, fmt.Errorf("%w: Biscuit tokens are not accepted here", ErrInvalidSignature)
		}
		var err error
		if cap, biscuitIDs, err = v.verifyBiscuit(ctx, token, v.cfg.MeterCopies); err != nil {
			return Capability{}, err
		}
	} else {
		var err error
		if cap, err = v.verifySigned(ctx, token); err != nil {
			return Capability{}, err
		}
		// The token sealed inside a Biscuit, lifted out to shed the
		// Biscuit's attenuation.
		if cap.BiscuitRoot != "" {
			return Capability{}, fmt.Errorf("%w: token is sealed in a Biscuit; present the Biscuit", ErrInvalidSignature)
		}
	}

	// 4) Time window. Leeway absorbs clock skew on both ends.
	now := v.cfg.Now()
	if !cap.NotBefore.IsZero() && now.Add(v.cfg.Leeway).Before(cap.NotBefore) {
		return Capability{}, ErrNotYetValid
	}
	if cap.ExpiresAt.IsZero() || now.Add(-v.cfg.Leeway).After(cap.ExpiresAt) {
		return Capability{}, ErrExpired
	}

	// 5) Audience match.
	if !slices.Contains(cap.Audience, audience) {
		return Capability{}, fmt.Errorf("%w: token audience %v lacks %q",
			ErrAudienceMismatch, cap.Audience, audience)
	}

	// 6) Revocation of this capability or any ancestor. Last so cheap
	// rejections short-circuit before hitting the cache / store.
	revoked, err := v.cfg.Revocations.IsRevoked(ctx, cap.ID)
	if err != nil {
		return Capability{}, fmt.Errorf("capability: revocation lookup: %w", err)
	}
	if revoked {
		return Capability{}, ErrRevoked
	}
	// 7) Revocation of this copy of a Biscuit, or a copy it was attenuated
	// from.
	if biscuitIDs != nil {
		revoked, err := v.cfg.BiscuitRevocations.IsBiscuitRevoked(ctx, biscuitIDs)
		if err != nil {
			return Capability{}, fmt.Errorf("capability: biscuit revocation lookup: %w", err)
		}
		if revoked {
			return Capability{}, ErrRevoked
		}
	}

	return *cap, nil
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
