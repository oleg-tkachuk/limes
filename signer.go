package limes

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-json-experiment/json"
	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes/internal/jose"
)

// Signer mints capability tokens with an Ed25519 key; verifiers pick up
// the public keys (statically or from a JWKS endpoint) and validate
// locally — no RPC per request. Implement it over a KMS to keep the
// private key out of process memory.
//
// Signer is intentionally narrow: callers populate a Capability struct
// and ask Sign to encode + sign. The compact JWT format is the wire
// representation; Decode reads one back without the verifier path, for
// debugging tools, and VerifySignature checks its signature alone.
type Signer interface {
	// KeyID is the JWKS key ID embedded in tokens this signer mints.
	// Verifiers index their key set by it, and rotation works by
	// running two signers in parallel until clients have caught up.
	KeyID() string

	// Sign emits a compact JWT for the supplied capability. The cap's
	// IssuedAt / NotBefore / ExpiresAt must already be populated; Sign
	// does not normalise times. ctx bounds the signing, which for a key
	// held in a KMS or an HSM is a call over the network.
	Sign(ctx context.Context, c Capability) (string, error)
}

// ed25519Signer is the Ed25519-backed Signer.
type ed25519Signer struct {
	keyID string
	priv  ed25519.PrivateKey
}

// NewEd25519Signer constructs a signer from a key ID and Ed25519 private
// key. Caller is responsible for key generation / KMS interaction —
// this package is pure crypto + encoding.
func NewEd25519Signer(keyID string, priv ed25519.PrivateKey) (Signer, error) {
	if keyID == "" {
		return nil, errors.New("limes: signer key ID required")
	}
	if len(priv) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("limes: ed25519 private key wrong length %d", len(priv))
	}
	return &ed25519Signer{keyID: keyID, priv: priv}, nil
}

func (s *ed25519Signer) KeyID() string { return s.keyID }

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
func (s *ed25519Signer) Sign(_ context.Context, c Capability) (string, error) {
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

	header := jwtHeader{Alg: jose.AlgEd25519, Kid: s.keyID, Typ: TokenType}
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

	// Ed25519 ignores the crypto.Hash argument (PureEdDSA hashes the
	// message itself); we pass crypto.Hash(0) per the stdlib contract.
	sig, err := s.priv.Sign(nil, []byte(signingInput), crypto.Hash(0))
	if err != nil {
		return "", fmt.Errorf("limes: sign: %w", err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// TokenType is the JWT "typ" header every capability token carries. The
// verifier requires it, so a different kind of EdDSA JWT signed by the same
// key — an ID token, another product's token — cannot be presented as a
// capability. It names the format's version: a token of another version is
// refused here, before a claim of it is read under the wrong schema.
const TokenType = "limes-cap-v3+jwt" //nolint:gosec // G101: a JWT type label, not a credential

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
	// TokenType.
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

// Decode parses a compact-form token without verifying the signature
// or applying time / audience / revocation gates. Use only for tooling
// that displays a token; production code goes through Verifier.Verify,
// which never reads a claim before the signature has checked out.
func Decode(token string) (Capability, error) {
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

// VerifySignature checks the Ed25519 signature on a compact token
// against the supplied public key. Time / audience / revocation gates
// are NOT applied here — Verify in the production verifier wraps this
// call with the full check chain.
func VerifySignature(token string, pub ed25519.PublicKey) error {
	parts, err := jose.Split(token)
	if err != nil {
		return err
	}
	return verifyParts(parts, pub)
}

func verifyParts(parts jose.Parts, pub ed25519.PublicKey) error {
	if len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("%w: public key wrong length %d", ErrInvalidToken, len(pub))
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts.Signature)
	if err != nil {
		return fmt.Errorf("%w: decode sig: %w", ErrInvalidToken, err)
	}
	if !ed25519.Verify(pub, []byte(parts.SigningInput()), sig) {
		return ErrInvalidToken
	}
	return nil
}
