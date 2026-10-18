package limes

import (
	"cmp"
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/go-json-experiment/json"
	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes/internal/jose"
	"github.com/oleg-tkachuk/limes/internal/link"
)

// Signer holds the key an Issuer mints capability tokens with: it encodes a
// Capability as the token format and signs it with an Ed25519 key, which may
// be in memory or held in a KMS or an HSM behind crypto.Signer. It signs only
// for an Issuer (IssuerConfig.Signer). Verifiers pick up the public keys
// (statically or from a JWKS endpoint) and validate locally — no RPC per
// request.
type Signer struct {
	keyID string
	key   crypto.Signer
	// pub is the key's public half, read once: each signature is checked
	// against it before a token carries it. An array, so Signer stays
	// comparable.
	pub [ed25519.PublicKeySize]byte
	// typ is the token type it mints.
	typ TokenType
}

// SignerConfig configures a Signer.
type SignerConfig struct {
	// KeyID is the kid tokens carry, by which verifiers find the key.
	// Required.
	KeyID string
	// Key signs. Its public key must be Ed25519: an ed25519.PrivateKey, or
	// a crypto.Signer over a key held elsewhere — one that also implements
	// ContextSigner is given the request's context. Required.
	Key crypto.Signer
	// TokenType is the format the signer mints. Empty: TokenTypeV4, which
	// every verifier of this module accepts. Set TokenTypeV5 once every
	// verifier of the tokens accepts it (VerifierConfig.AcceptTokenTypes).
	TokenType TokenType
}

// ContextSigner is a crypto.Signer that signs under a context, as a key
// held in a KMS or an HSM does for its call over the network. Signer uses
// SignContext when its key has it, so a slow or cancelled call is bounded
// by the request that asked for the token.
type ContextSigner interface {
	crypto.Signer
	SignContext(ctx context.Context, rand io.Reader, message []byte, opts crypto.SignerOpts) ([]byte, error)
}

// NewSigner builds a Signer, refusing an empty kid or a key that is not
// Ed25519.
func NewSigner(cfg SignerConfig) (*Signer, error) {
	if cfg.KeyID == "" {
		return nil, errors.New("limes: SignerConfig.KeyID required")
	}
	if cfg.Key == nil {
		return nil, errors.New("limes: SignerConfig.Key required")
	}
	// An ed25519.PrivateKey of the wrong length panics in Public.
	if k, ok := cfg.Key.(ed25519.PrivateKey); ok && len(k) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("limes: SignerConfig.Key is %d bytes, not an Ed25519 private key's %d", len(k), ed25519.PrivateKeySize)
	}
	pub, ok := cfg.Key.Public().(ed25519.PublicKey)
	if !ok || len(pub) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("limes: SignerConfig.Key must be Ed25519, not %T", cfg.Key.Public())
	}
	typ := cmp.Or(cfg.TokenType, TokenTypeV4)
	if !slices.Contains(tokenTypes, typ) {
		return nil, fmt.Errorf("limes: SignerConfig.TokenType %q is not one of %q", typ, tokenTypes)
	}
	return &Signer{keyID: cfg.KeyID, key: cfg.Key, pub: [ed25519.PublicKeySize]byte(pub), typ: typ}, nil
}

// jwtHeader is the protected header. We use compact serialization:
// `header.payload.signature` with each segment base64url-encoded.
type jwtHeader struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
	Typ string `json:"typ"`
}

// jwtClaims is the payload. We follow JWT conventions for standard
// claims (iss/sub/aud/iat/nbf/exp/jti) and prefix capability-specific
// claims with `limes_`. The verifier reads it strictly (strictClaims): no
// member it does not know, none twice, every name in its exact case.
type jwtClaims struct {
	// Standard JWT claims.
	Issuer    string   `json:"iss"`
	Subject   string   `json:"sub"` // Principal.Subject, for JWT tooling
	Audience  []string `json:"aud"`
	IssuedAt  int64    `json:"iat"`
	NotBefore int64    `json:"nbf,omitzero"`
	ExpiresAt int64    `json:"exp"`
	ID        string   `json:"jti"`

	// Capability claims.
	Principal wirePrincipal `json:"limes_principal"`
	Caveats   wireCaveats   `json:"limes_caveats"`
	ParentID  string        `json:"limes_parent_id,omitempty"`

	// Confirmation is RFC 7800's `cnf`, omitted for an unbound capability.
	Confirmation *cnfClaim `json:"cnf,omitzero"`

	// BiscuitRoot roots the signature chain of the Biscuit this token is
	// sealed in; omitted on every ordinary token.
	BiscuitRoot string `json:"limes_bsk,omitempty"`
}

// jwtClaimsV4 is a TokenTypeV4 payload: v5's claims and limes_gen, a counter
// v4 carried for audit and nothing read. It is read, range-checked and
// dropped; nothing writes it.
type jwtClaimsV4 struct {
	jwtClaims  `json:",inline"`
	Generation int64 `json:"limes_gen,omitzero"`
}

// MaxIJSONInteger is the largest integer a token carries: 2^53 − 1, the
// largest an IEEE 754 double holds exactly. RFC 7493 (I-JSON) §2.2 keeps
// numbers within it so that every JSON reader — JavaScript's among them —
// reads the same value; larger amounts travel as decimal strings (Nanos).
const MaxIJSONInteger = jose.MaxIJSONInteger

// The integer claims' names, as their json tags spell them, for errors.
const (
	claimIssuedAt    = "iat"
	claimNotBefore   = "nbf"
	claimExpiresAt   = "exp"
	claimGeneration  = "limes_gen"
	claimMaxRequests = "max_requests"
)

// checkIJSONIntegers refuses claims whose integers lie outside what I-JSON
// lets a reader rely on, naming the first in claim order.
func checkIJSONIntegers(c jwtClaims) error {
	for _, claim := range []struct {
		name string
		n    int64
	}{
		{claimIssuedAt, c.IssuedAt}, {claimNotBefore, c.NotBefore}, {claimExpiresAt, c.ExpiresAt},
		{claimMaxRequests, c.Caveats.MaxRequests},
	} {
		if claim.n < -MaxIJSONInteger || claim.n > MaxIJSONInteger {
			return fmt.Errorf("limes: %s %d is outside the I-JSON integer range (RFC 7493)", claim.name, claim.n)
		}
	}
	return nil
}

// strictClaims is how a token's header and claims are read. Unknown members
// are refused, not skipped: a caveat this verifier does not know is one it
// cannot enforce, and must not pass as one it did. Duplicate names are
// refused and names match in their exact case — both by default in the
// encoding/json/v2 semantics — so no two parsers can read one signed token
// two ways.
var strictClaims = json.RejectUnknownMembers(true)

type cnfClaim struct {
	JKT string `json:"jkt"`
}

// sign encodes c as a compact token and signs it. c's IssuedAt and
// ExpiresAt must be set; sign does not normalise times. Unexported: a
// token is minted only through an Issuer, which records the capability
// first, so every token can be revoked and its usage traced.
func (s *Signer) sign(ctx context.Context, c Capability) (string, error) {
	return s.signGrant(ctx, grant{Capability: c})
}

// signGrant is sign for a grant: a token sealed in a Biscuit names its
// root. A grant's copies are the verifier's and never signed.
func (s *Signer) signGrant(ctx context.Context, g grant) (string, error) {
	c := g.Capability
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if c.ID == uuid.Nil {
		return "", errors.New("limes: ID required")
	}
	if c.Issuer == "" {
		return "", errors.New("limes: Issuer required")
	}
	if c.Subject.TenantID == uuid.Nil {
		return "", errors.New("limes: Subject.TenantID required")
	}
	if len(c.Caveats.Ops) == 0 {
		return "", errors.New("limes: at least one Op required")
	}
	// Nothing is signed that the verifier would refuse to read.
	if err := c.Caveats.Validate(); err != nil {
		return "", err
	}
	if err := c.Subject.Validate(); err != nil {
		return "", err
	}
	if c.ExpiresAt.Before(c.IssuedAt) || c.ExpiresAt.IsZero() {
		return "", errors.New("limes: ExpiresAt must be after IssuedAt")
	}

	header := jwtHeader{Alg: jose.AlgEd25519, Kid: s.keyID, Typ: string(s.typ)}
	claims := claimsOf(g, s.typ)
	// Nothing is signed that the verifier would refuse to read.
	if err := checkIJSONIntegers(claims); err != nil {
		return "", err
	}

	headerJSON, err := json.Marshal(header)
	if err != nil {
		return "", fmt.Errorf("limes: marshal header: %w", err)
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("limes: marshal claims: %w", err)
	}

	signingInput := strings.Join([]string{
		base64.RawURLEncoding.EncodeToString(headerJSON),
		base64.RawURLEncoding.EncodeToString(claimsJSON),
	}, ".")

	// Ed25519 hashes the message itself (PureEdDSA): crypto.Hash(0) is the
	// stdlib's way to ask for it.
	var sig []byte
	if cs, ok := s.key.(ContextSigner); ok {
		sig, err = cs.SignContext(ctx, rand.Reader, []byte(signingInput), crypto.Hash(0))
	} else {
		sig, err = s.key.Sign(rand.Reader, []byte(signingInput), crypto.Hash(0))
	}
	if err != nil {
		return "", fmt.Errorf("limes: sign: %w", err)
	}
	if len(sig) != ed25519.SignatureSize {
		return "", fmt.Errorf("limes: sign: an Ed25519 signature is %d bytes, not %d", ed25519.SignatureSize, len(sig))
	}
	// A key held elsewhere — a KMS, an HSM — may sign with another key than
	// the one it reports, or return bytes that are no signature at all:
	// a token carrying either would be refused by every verifier, and
	// recorded as issued.
	if !ed25519.Verify(s.pub[:], []byte(signingInput), sig) {
		return "", errors.New("limes: sign: the signature does not verify under the key's public key")
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// A token's JWT "typ" header names its format. The verifier requires a type
// it accepts (VerifierConfig.AcceptTokenTypes), so a different kind of EdDSA
// JWT signed by the same key — an ID token, another product's token —
// cannot be presented as a capability, and it reads each type's claims
// under that type's schema.
//
// v4 spells every ceiling out: an absent max_requests or max_budget is a
// ceiling of 0, and only unlimited_requests or unlimited_budget lifts it.
// v3, which read an absent bound as none, is not accepted.
//
// v5 is v4 without limes_gen, and without the operations presign, tag,
// search and embed, which a v4 token may name unnamespaced: a domain's
// operations are its consumer's, namespaced. A Signer mints v4 unless
// SignerConfig.TokenType says v5; a verifier accepts both.
const (
	TokenTypeV4 TokenType = "limes-cap-v4+jwt" //nolint:gosec // G101: a JWT type label, not a credential
	TokenTypeV5 TokenType = "limes-cap-v5+jwt" //nolint:gosec // G101: a JWT type label, not a credential
)

// TokenType is a token format, as the JWT "typ" header names it.
type TokenType string

// tokenTypes are the token types this package has a schema for.
var tokenTypes = []TokenType{TokenTypeV4, TokenTypeV5}

func parseHeader(seg string) (jwtHeader, error) {
	raw, err := jose.UnB64(seg)
	if err != nil {
		return jwtHeader{}, fmt.Errorf("limes: decode header: %w", err)
	}
	var h jwtHeader
	if err := json.Unmarshal(raw, &h, strictClaims); err != nil {
		return jwtHeader{}, fmt.Errorf("limes: parse header: %w", err)
	}
	// Only the fully-specified identifier (RFC 9864): a capability token is
	// this module's own, and none was ever written with another under any
	// of its token types.
	if h.Alg != jose.AlgEd25519 {
		return jwtHeader{}, fmt.Errorf("limes: unexpected alg %q (want %s)", h.Alg, jose.AlgEd25519)
	}
	return h, nil
}

// claimsOf is the payload Sign writes for c: the one place a Capability
// becomes claims.
func claimsOf(g grant, typ TokenType) jwtClaims {
	c := g.Capability
	claims := jwtClaims{
		Issuer:    c.Issuer,
		Subject:   c.Subject.Subject,
		Audience:  c.Audience,
		IssuedAt:  c.IssuedAt.Unix(),
		ExpiresAt: c.ExpiresAt.Unix(),
		ID:        c.ID.String(),
		Principal: toWirePrincipal(c.Subject, typ),
		Caveats:   toWireCaveats(c.Caveats),
	}
	if !c.NotBefore.IsZero() {
		claims.NotBefore = c.NotBefore.Unix()
	}
	if c.ParentID != uuid.Nil {
		claims.ParentID = c.ParentID.String()
	}
	claims.BiscuitRoot = g.biscuitRoot
	if c.ConfirmationJKT != "" {
		claims.Confirmation = &cnfClaim{JKT: c.ConfirmationJKT}
	}
	return claims
}

// parseClaims reads a payload under the schema of its token type typ, one
// of tokenTypes.
func parseClaims(seg string, typ TokenType) (*grant, error) {
	raw, err := jose.UnB64(seg)
	if err != nil {
		return nil, fmt.Errorf("limes: decode claims: %w", err)
	}
	var claims jwtClaims
	switch typ {
	case TokenTypeV4:
		var v4 jwtClaimsV4
		if err := json.Unmarshal(raw, &v4, strictClaims); err != nil {
			return nil, fmt.Errorf("limes: parse claims: %w", err)
		}
		if v4.Generation < -MaxIJSONInteger || v4.Generation > MaxIJSONInteger {
			return nil, fmt.Errorf("limes: %s %d is outside the I-JSON integer range (RFC 7493)", claimGeneration, v4.Generation)
		}
		claims = v4.jwtClaims
	case TokenTypeV5:
		if err := json.Unmarshal(raw, &claims, strictClaims); err != nil {
			return nil, fmt.Errorf("limes: parse claims: %w", err)
		}
	default:
		return nil, fmt.Errorf("limes: no claims schema for typ %q", typ)
	}
	if err := checkIJSONIntegers(claims); err != nil {
		return nil, err
	}
	// sub repeats the principal for JWT tooling; two subjects would be read
	// differently by a parser that takes one and a parser that takes the other.
	if claims.Subject != claims.Principal.Subject {
		return nil, fmt.Errorf("limes: sub %q differs from limes_principal.subject %q", claims.Subject, claims.Principal.Subject)
	}

	id, err := uuid.Parse(claims.ID)
	if err != nil {
		return nil, fmt.Errorf("limes: parse jti: %w", err)
	}
	cap := &Capability{
		ID:        id,
		Issuer:    claims.Issuer,
		Audience:  claims.Audience,
		Caveats:   claims.Caveats.caveats(),
		IssuedAt:  time.Unix(claims.IssuedAt, 0).UTC(),
		ExpiresAt: time.Unix(claims.ExpiresAt, 0).UTC(),
	}
	if claims.NotBefore != 0 {
		cap.NotBefore = time.Unix(claims.NotBefore, 0).UTC()
	}
	if cap.Subject, err = claims.Principal.principal(typ); err != nil {
		return nil, err
	}
	if claims.Confirmation != nil {
		cap.ConfirmationJKT = claims.Confirmation.JKT
	}
	if claims.ParentID != "" {
		pid, err := uuid.Parse(claims.ParentID)
		if err != nil {
			return nil, fmt.Errorf("limes: parse parent_id: %w", err)
		}
		cap.ParentID = pid
	}
	return &grant{Capability: *cap, biscuitRoot: claims.BiscuitRoot}, nil
}

func init() { link.DecodeToken = decodeToken }

// decodeToken reads a compact token's header and claims without checking
// its signature: inspect.Decode, for tooling that displays a token.
func decodeToken(token string) (any, error) {
	parts, err := jose.Split(token)
	if err != nil {
		return Capability{}, err
	}
	h, err := parseHeader(parts.Header)
	if err != nil {
		return Capability{}, err
	}
	c, err := parseClaims(parts.Payload, TokenType(h.Typ))
	if err != nil {
		return Capability{}, err
	}
	return c.Capability, nil
}

func verifyParts(parts jose.Parts, pub ed25519.PublicKey) error {
	if err := jose.VerifyEd25519(parts, pub); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidToken, err)
	}
	return nil
}
