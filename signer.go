package limes

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/go-json-experiment/json"
	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes/internal/jose"
	"github.com/oleg-tkachuk/limes/internal/link"
)

// Signer mints capability tokens: it encodes a Capability as the token
// format and signs it with an Ed25519 key, which may be in memory or held in
// a KMS or an HSM behind crypto.Signer. Verifiers pick up the public keys
// (statically or from a JWKS endpoint) and validate locally — no RPC per
// request.
type Signer struct {
	keyID string
	key   crypto.Signer
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
	if pub, ok := cfg.Key.Public().(ed25519.PublicKey); !ok || len(pub) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("limes: SignerConfig.Key must be Ed25519, not %T", cfg.Key.Public())
	}
	return &Signer{keyID: cfg.KeyID, key: cfg.Key}, nil
}

// KeyID is the kid tokens this signer mints carry.
func (s *Signer) KeyID() string { return s.keyID }

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
	Principal  Principal `json:"limes_principal"`
	Caveats    Caveats   `json:"limes_caveats"`
	ParentID   string    `json:"limes_parent_id,omitempty"`
	Generation int64     `json:"limes_gen,omitzero"`

	// Confirmation is RFC 7800's `cnf`, omitted for an unbound capability.
	Confirmation *cnfClaim `json:"cnf,omitzero"`

	// BiscuitRoot roots the signature chain of the Biscuit this token is
	// sealed in; omitted on every ordinary token.
	BiscuitRoot string `json:"limes_bsk,omitempty"`
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
		{claimGeneration, c.Generation}, {claimMaxRequests, c.Caveats.MaxRequests},
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

// Sign implements Signer. Encodes the header + claims, signs the
// dotted concatenation, returns the compact form.
// Sign encodes c as a compact token and signs it. c's IssuedAt and
// ExpiresAt must be set; Sign does not normalise times.
func (s *Signer) Sign(ctx context.Context, c Capability) (string, error) {
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
	if c.ExpiresAt.Before(c.IssuedAt) || c.ExpiresAt.IsZero() {
		return "", errors.New("limes: ExpiresAt must be after IssuedAt")
	}

	header := jwtHeader{Alg: jose.AlgEd25519, Kid: s.keyID, Typ: tokenType}
	claims := jwtClaims{
		Issuer:     c.Issuer,
		Subject:    c.Subject.Subject,
		Audience:   c.Audience,
		IssuedAt:   c.IssuedAt.Unix(),
		ExpiresAt:  c.ExpiresAt.Unix(),
		ID:         c.ID.String(),
		Principal:  c.Subject,
		Caveats:    c.Caveats,
		Generation: c.Generation,
	}
	if !c.NotBefore.IsZero() {
		claims.NotBefore = c.NotBefore.Unix()
	}
	if c.ParentID != uuid.Nil {
		claims.ParentID = c.ParentID.String()
	}
	claims.BiscuitRoot = c.BiscuitRoot
	if c.ConfirmationJKT != "" {
		claims.Confirmation = &cnfClaim{JKT: c.ConfirmationJKT}
	}
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
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// tokenType is the JWT "typ" header every capability token carries. The
// verifier requires it, so a different kind of EdDSA JWT signed by the same
// key — an ID token, another product's token — cannot be presented as a
// capability. It names the format's version: a token of another version is
// refused here, before a claim of it is read under the wrong schema.
const tokenType = "limes-cap-v3+jwt" //nolint:gosec // G101: a JWT type label, not a credential

func parseHeader(seg string) (jwtHeader, error) {
	raw, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		return jwtHeader{}, fmt.Errorf("limes: decode header: %w", err)
	}
	var h jwtHeader
	if err := json.Unmarshal(raw, &h, strictClaims); err != nil {
		return jwtHeader{}, fmt.Errorf("limes: parse header: %w", err)
	}
	// Only the fully-specified identifier (RFC 9864): a capability token is
	// this module's own, and none was ever written with another under this
	// tokenType.
	if h.Alg != jose.AlgEd25519 {
		return jwtHeader{}, fmt.Errorf("limes: unexpected alg %q (want %s)", h.Alg, jose.AlgEd25519)
	}
	return h, nil
}

func parseClaims(seg string) (*Capability, error) {
	raw, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		return nil, fmt.Errorf("limes: decode claims: %w", err)
	}
	var claims jwtClaims
	if err := json.Unmarshal(raw, &claims, strictClaims); err != nil {
		return nil, fmt.Errorf("limes: parse claims: %w", err)
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
		ID:         id,
		Issuer:     claims.Issuer,
		Audience:   claims.Audience,
		Caveats:    claims.Caveats,
		IssuedAt:   time.Unix(claims.IssuedAt, 0).UTC(),
		ExpiresAt:  time.Unix(claims.ExpiresAt, 0).UTC(),
		Generation: claims.Generation,
	}
	if claims.NotBefore != 0 {
		cap.NotBefore = time.Unix(claims.NotBefore, 0).UTC()
	}
	cap.Subject = claims.Principal
	if claims.Confirmation != nil {
		cap.ConfirmationJKT = claims.Confirmation.JKT
	}
	cap.BiscuitRoot = claims.BiscuitRoot
	if claims.ParentID != "" {
		pid, err := uuid.Parse(claims.ParentID)
		if err != nil {
			return nil, fmt.Errorf("limes: parse parent_id: %w", err)
		}
		cap.ParentID = pid
	}
	return cap, nil
}

func init() { link.DecodeToken = decodeToken }

// decodeToken reads a compact token's header and claims without checking
// its signature: inspect.Decode, for tooling that displays a token.
func decodeToken(token string) (any, error) {
	parts, err := jose.Split(token)
	if err != nil {
		return Capability{}, err
	}
	if _, err := parseHeader(parts.Header); err != nil {
		return Capability{}, err
	}
	c, err := parseClaims(parts.Payload)
	if err != nil {
		return Capability{}, err
	}
	return *c, nil
}

func verifyParts(parts jose.Parts, pub ed25519.PublicKey) error {
	if err := jose.VerifyEd25519(parts, pub); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidToken, err)
	}
	return nil
}
