package limes

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"maps"
	"net/netip"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes/dpop"
	"github.com/oleg-tkachuk/limes/internal/jose"
	"github.com/oleg-tkachuk/limes/jwks"
)

// KeyResolver resolves a JWKS `kid` to the public key the verifier
// should use. jwks.Static holds keys in memory; jwks.Remote
// fetches them from an issuer's JWKS endpoint with a cache.
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
	// ErrInvalidToken.
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
	// issuer and verifier. Zero: DefaultLeeway; negative is refused.
	Leeway time.Duration

	// MaxTokenBytes rejects longer tokens before decoding them. Default
	// DefaultMaxTokenBytes when zero; negative is refused.
	MaxTokenBytes int

	// AcceptBiscuit accepts the Biscuit form (Issuer.Biscuit, Attenuate)
	// beside the JWT. Off, a Biscuit is refused like any malformed token.
	AcceptBiscuit bool

	// MeterCopies admits Biscuits whose attenuation blocks set a copy's own
	// request or budget limits (Attenuation.MaxRequests, MaxBudget).
	// Set it only when the Meter that enforces caveats counts
	// the copies a MeterSubject carries; off, such a token is refused with
	// ErrCopyCountersNotMetered rather than accepted with its limits unkept.
	MeterCopies bool

	// BiscuitRevocations answers whether a copy of a Biscuit is revoked on
	// its own. Required when AcceptBiscuit is set; wrap the store in a
	// CachedBiscuitRevocationChecker.
	BiscuitRevocations BiscuitRevocationLookup

	// DPoP checks the proof of possession a key-bound capability (one with
	// a ConfirmationJKT) must come with. Without it the verifier refuses
	// every key-bound capability with dpop.ErrRequired, rather than accept
	// one as a bearer token. Built by dpop.NewVerifier, with its own clock.
	DPoP *dpop.Verifier
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
	DPoP dpop.Proof
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
// claims, then issuer / tenant / time / audience / revocation gates. A
// deployment typically resolves keys through a cached JWKS (jwks.Remote) and
// checks revocation through a short-TTL cache (CachedRevocationChecker), so
// revocation propagates quickly without hammering the store. A consumer that
// mocks it declares the narrow interface it needs.
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
	if cfg.Leeway < 0 {
		return nil, fmt.Errorf("limes: VerifierConfig.Leeway %s is negative", cfg.Leeway)
	}
	if cfg.MaxTokenBytes < 0 {
		return nil, fmt.Errorf("limes: VerifierConfig.MaxTokenBytes %d is negative", cfg.MaxTokenBytes)
	}
	if cfg.Leeway == 0 {
		cfg.Leeway = DefaultLeeway
	}
	if cfg.MaxTokenBytes == 0 {
		cfg.MaxTokenBytes = DefaultMaxTokenBytes
	}
	cfg.TrustedIssuers = slices.Clone(cfg.TrustedIssuers)
	if cfg.KeyIssuers != nil && len(cfg.KeyIssuers) == 0 {
		return nil, errors.New("limes: VerifierConfig.KeyIssuers binds no kid; leave it nil to bind none")
	}
	cfg.KeyIssuers = maps.Clone(cfg.KeyIssuers)
	return &StandardVerifier{cfg: cfg}, nil
}

// Verify checks the token itself: its signature, issuer and tenant, its
// validity window, its audience, the proof of possession a key-bound
// capability needs, and revocation. It returns the capability the token
// grants; a refusal is one of the package's sentinels, so the caller can
// branch on the failure mode.
//
// Order matters: nothing in the claims is read until the signature over
// them has verified, so a forged token costs a header decode and one
// signature check, and no claim-parsing path is reachable by an attacker.
// The checks that need no I/O come before the revocation lookup.
func (v *StandardVerifier) Verify(ctx context.Context, req VerifyRequest) (VerifiedCapability, error) {
	token, audience := req.Token, req.Audience
	if token == "" {
		return VerifiedCapability{}, fmt.Errorf("%w: no token", ErrInvalidToken)
	}
	if audience == "" {
		return VerifiedCapability{}, fmt.Errorf("%w: no audience to verify against", ErrAudienceMismatch)
	}
	if len(token) > v.cfg.MaxTokenBytes {
		return VerifiedCapability{}, fmt.Errorf("%w: token is %d bytes (limit %d)",
			ErrInvalidToken, len(token), v.cfg.MaxTokenBytes)
	}

	var cap *Capability
	var biscuitIDs [][]byte // set for a Biscuit, from its verified chain
	if isBiscuit(token) {
		if !v.cfg.AcceptBiscuit {
			return VerifiedCapability{}, fmt.Errorf("%w: Biscuit tokens are not accepted here", ErrInvalidToken)
		}
		var err error
		if cap, biscuitIDs, err = v.verifyBiscuit(ctx, token, v.cfg.MeterCopies); err != nil {
			return VerifiedCapability{}, err
		}
	} else {
		var err error
		if cap, err = v.verifySigned(ctx, token); err != nil {
			return VerifiedCapability{}, err
		}
		// The token sealed inside a Biscuit, lifted out to shed the
		// Biscuit's attenuation.
		if cap.BiscuitRoot != "" {
			return VerifiedCapability{}, fmt.Errorf("%w: token is sealed in a Biscuit; present the Biscuit", ErrInvalidToken)
		}
	}

	// 4) Time window. Leeway absorbs clock skew on both ends.
	now := v.cfg.Now()
	if !cap.NotBefore.IsZero() && now.Add(v.cfg.Leeway).Before(cap.NotBefore) {
		return VerifiedCapability{}, ErrNotYetValid
	}
	if cap.ExpiresAt.IsZero() || now.Add(-v.cfg.Leeway).After(cap.ExpiresAt) {
		return VerifiedCapability{}, ErrExpired
	}

	// 5) Audience match.
	if !slices.Contains(cap.Audience, audience) {
		return VerifiedCapability{}, fmt.Errorf("%w: token audience %v lacks %q",
			ErrAudienceMismatch, cap.Audience, audience)
	}

	// 6) Proof of possession, for a key-bound capability — over the token
	// as presented, the Biscuit when it is one — under the scheme its
	// binding calls for.
	if err := checkScheme(req.Scheme, cap.ConfirmationJKT != ""); err != nil {
		return VerifiedCapability{}, err
	}
	if cap.ConfirmationJKT != "" {
		if v.cfg.DPoP == nil {
			return VerifiedCapability{}, fmt.Errorf("%w: capability is key-bound and this verifier has no DPoP configured",
				proofRefusal(dpop.ErrRequired))
		}
		if err := v.cfg.DPoP.Verify(ctx, dpop.Request{Proof: req.DPoP, Token: token, JKT: cap.ConfirmationJKT}); err != nil {
			if isProofRefusal(err) {
				return VerifiedCapability{}, proofRefusal(err)
			}
			return VerifiedCapability{}, fmt.Errorf("limes: proof of possession: %w", err)
		}
	}

	// 7) Revocation of this capability or any ancestor. Last so cheap
	// rejections short-circuit before hitting the cache / store.
	revoked, err := v.cfg.Revocations.IsRevoked(ctx, cap.ID)
	if err != nil {
		return VerifiedCapability{}, fmt.Errorf("limes: revocation lookup: %w", err)
	}
	if revoked {
		return VerifiedCapability{}, ErrRevoked
	}
	// 8) Revocation of this copy of a Biscuit, or a copy it was attenuated
	// from.
	if biscuitIDs != nil {
		revoked, err := v.cfg.BiscuitRevocations.IsBiscuitRevoked(ctx, biscuitIDs)
		if err != nil {
			return VerifiedCapability{}, fmt.Errorf("limes: biscuit revocation lookup: %w", err)
		}
		if revoked {
			return VerifiedCapability{}, ErrRevoked
		}
	}

	return VerifiedCapability{c: *cap}, nil
}

// DPoPNonce is the nonce to send in the DPoP-Nonce header — with a
// response refused by dpop.ErrNonceRequired, and with any other for the
// client's next proof. Empty when the verifier issues none.
func (v *StandardVerifier) DPoPNonce(ctx context.Context) (string, error) {
	if v.cfg.DPoP == nil {
		return "", nil
	}
	return v.cfg.DPoP.Nonce(ctx)
}

// Authorize is Verify followed by every caveat that can be checked without
// state: the operation, the resource, the idempotency key, the tainted read
// (Caveats.Check), and the client's address (Caveats.CheckSource). The
// request and budget limits are stateful: the Meter enforces them, through
// Bump and Charge, inside the consumer's own transaction.
func (v *StandardVerifier) Authorize(ctx context.Context, req AuthorizeRequest) (VerifiedCapability, error) {
	vc, err := v.Verify(ctx, req.VerifyRequest)
	if err != nil {
		return VerifiedCapability{}, err
	}
	if err := vc.c.Caveats.CheckSource(req.ClientAddr); err != nil {
		return VerifiedCapability{}, err
	}
	if err := vc.c.Caveats.Check(req.CheckRequest); err != nil {
		return VerifiedCapability{}, err
	}
	return vc, nil
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
		return nil, nil, fmt.Errorf("%w: biscuit seals an ordinary token", ErrInvalidToken)
	}
	return attenuate(sealed, meterCopies)
}

// verifySigned checks a compact JWT's header, signature, issuer and tenant —
// everything about the token itself; the time, audience and revocation gates
// run in Verify on the capability the token finally grants.
func (v *StandardVerifier) verifySigned(ctx context.Context, token string) (*Capability, error) {
	// 1) Header: structure, algorithm, type, key id.
	parts, err := jose.Split(token)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidToken, err)
	}
	header, err := parseHeader(parts.Header)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidToken, err)
	}
	if header.Typ != tokenType {
		return nil, fmt.Errorf("%w: typ %q is not %q", ErrInvalidToken, header.Typ, tokenType)
	}
	if header.Kid == "" {
		return nil, fmt.Errorf("%w: header missing kid", ErrInvalidToken)
	}

	// 2) Key and signature — the cryptographic gate.
	pub, err := v.cfg.Keys.PublicKey(ctx, header.Kid)
	if err != nil {
		// An unknown kid is the token's fault; a resolver that cannot
		// answer — the issuer's keys out of reach, a store down — is ours.
		if errors.Is(err, jwks.ErrUnknownKID) {
			return nil, fmt.Errorf("%w: kid %q: %w", ErrInvalidToken, header.Kid, err)
		}
		return nil, fmt.Errorf("limes: resolve kid %q: %w", header.Kid, err)
	}
	if err := verifyParts(parts, pub); err != nil {
		return nil, err
	}

	// 3) Claims, now known to be the issuer's.
	cap, err := parseClaims(parts.Payload)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidToken, err)
	}
	if !slices.Contains(v.cfg.TrustedIssuers, cap.Issuer) {
		return nil, fmt.Errorf("%w: untrusted issuer %q", ErrInvalidToken, cap.Issuer)
	}
	if v.cfg.KeyIssuers != nil {
		if bound, ok := v.cfg.KeyIssuers[header.Kid]; !ok || bound != cap.Issuer {
			return nil, fmt.Errorf("%w: kid %q may not sign for issuer %q",
				ErrInvalidToken, header.Kid, cap.Issuer)
		}
	}
	if cap.Subject.TenantID == uuid.Nil {
		return nil, fmt.Errorf("%w: capability has no tenant", ErrInvalidToken)
	}
	if err := cap.Subject.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidToken, err)
	}

	return cap, nil
}

// proofRefusals are the dpop errors that refuse a proof; any other error
// from dpop.Verifier is a failure of its replay cache or nonces.
var proofRefusals = []error{dpop.ErrRequired, dpop.ErrInvalid, dpop.ErrReplayed, dpop.ErrNonceRequired}

func isProofRefusal(err error) bool {
	for _, r := range proofRefusals {
		if errors.Is(err, r) {
			return true
		}
	}
	return false
}

// proofRefusal is a dpop refusal as this module reads it: a refusal of the
// credentials (ErrProofOfPossession, and so ErrUnauthenticated), with the
// dpop sentinel still matchable.
func proofRefusal(err error) error { return fmt.Errorf("%w: %w", ErrProofOfPossession, err) }

// VerifiedCapability is a capability a StandardVerifier accepted. Only Verify
// and Authorize make one, so a function that takes it — Issuer.Delegate —
// knows the token it came from was checked, rather than trusting a caller to
// have checked it.
type VerifiedCapability struct {
	c Capability
}

// Capability is what the token grants: a copy, the caller's to change.
func (v VerifiedCapability) Capability() Capability { return v.c.clone() }
