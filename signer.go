package limes

import (
	"cmp"
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"
	"uuid"

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
	key   *sealedSigner
	// pub is the key's public half, read once: each signature is checked
	// against it before a token carries it. An array, so Signer stays
	// comparable.
	pub [ed25519.PublicKeySize]byte
	// typ is the token type it mints.
	typ TokenType
	// alg is the alg its tokens name.
	alg SigningAlgorithm
}

// SignerConfig configures a Signer.
type SignerConfig struct {
	// KeyID is the kid tokens carry, by which verifiers find the key.
	// Required.
	KeyID string
	// Key signs, sealed (SealKey): an ed25519.PrivateKey, or a
	// crypto.Signer over a key held elsewhere — one that also implements
	// ContextSigner is given the request's context. Its public key must be
	// Ed25519. Required.
	Key SealedKey
	// TokenType is the format the signer mints. Empty: TokenTypeV6.
	// TokenTypeV7, the RFC 9068 access token, is minted here once every
	// verifier of the tokens reads it (VerifierConfig.AcceptTokenTypes), and
	// needs each capability's ClientID.
	TokenType TokenType
	// Algorithm is the alg the tokens name. Empty: SigningAlgorithmEd25519,
	// the name RFC 9864 gives the algorithm. SigningAlgorithmEdDSA names it
	// as JOSE libraries that predate RFC 9864 do; the signature is the same,
	// and every verifier of this package reads both.
	Algorithm SigningAlgorithm
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
	key := cfg.Key.signer()
	if key == nil {
		return nil, errors.New("limes: SignerConfig.Key required")
	}
	// An ed25519.PrivateKey of the wrong length panics in Public.
	if k, ok := key.(ed25519.PrivateKey); ok && len(k) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("limes: SignerConfig.Key is %d bytes, not an Ed25519 private key's %d", len(k), ed25519.PrivateKeySize)
	}
	pub, ok := key.Public().(ed25519.PublicKey)
	if !ok || len(pub) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("limes: SignerConfig.Key must be Ed25519, not %T", key.Public())
	}
	typ := cmp.Or(cfg.TokenType, TokenTypeV6)
	if !slices.Contains(tokenTypes, typ) {
		return nil, fmt.Errorf("limes: SignerConfig.TokenType %q is not one of %q", typ, tokenTypes)
	}
	alg := cmp.Or(cfg.Algorithm, SigningAlgorithmEd25519)
	if !slices.Contains(signingAlgorithms, alg) {
		return nil, fmt.Errorf("limes: SignerConfig.Algorithm %q is not one of %q", alg, signingAlgorithms)
	}
	return &Signer{keyID: cfg.KeyID, key: cfg.Key.s, pub: [ed25519.PublicKeySize]byte(pub), typ: typ, alg: alg}, nil
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

	// ClientID and Format are a TokenTypeV7 token's alone, and required of
	// it: RFC 9068's client_id, and the capability format, which at+jwt
	// leaves the type unable to name. A TokenTypeV6 token carries neither.
	ClientID string `json:"client_id,omitempty"`
	Format   int64  `json:"limes_v,omitzero"`
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
	if c.ID == uuid.Nil() {
		return "", errors.New("limes: ID required")
	}
	if c.Issuer == "" {
		return "", errors.New("limes: Issuer required")
	}
	if c.Subject.TenantID == uuid.Nil() {
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

	header := jwtHeader{Alg: string(s.alg), Kid: s.keyID, Typ: string(s.typ)}
	// A v6 token has no member for name prefixes: signing one without them
	// would drop a restriction, and widen the capability.
	if s.typ == TokenTypeV6 && len(c.Caveats.ResourceNamePrefixes) > 0 {
		return "", errors.New("limes: ResourceNamePrefixes need a TokenTypeV7 signer")
	}
	// RFC 9068 makes client_id required of an at+jwt: none is invented.
	if s.typ == TokenTypeV7 && c.ClientID == "" {
		return "", errors.New("limes: a TokenTypeV7 capability needs a ClientID (RFC 9068 client_id)")
	}
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
	if cs, ok := s.key.open().(ContextSigner); ok {
		sig, err = cs.SignContext(ctx, rand.Reader, []byte(signingInput), crypto.Hash(0))
	} else {
		sig, err = s.key.open().Sign(rand.Reader, []byte(signingInput), crypto.Hash(0))
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
// v6 spells every ceiling out — an absent max_requests, max_budget or
// max_copies is a ceiling of 0, and only unlimited_requests,
// unlimited_budget or unlimited_copies lifts it — names the parent agent's
// run parent_run_id, and knows only the built-in operations and namespaced
// ones. Earlier formats are not accepted: v5, which had no quota of Biscuit
// copies (max_copies), v4, with limes_gen, parent_agent_id and the
// unnamespaced presign, tag, search and embed, nor v3, which read an absent
// bound as none.
const TokenTypeV6 TokenType = "limes-cap-v6+jwt" //nolint:gosec // G101: a JWT type label, not a credential

// TokenTypeV7 is the capability as an OAuth 2.0 access token in the JWT
// profile of RFC 9068: typ at+jwt, so a resource server or gateway that
// validates that profile reads it, with the claims the profile requires,
// client_id among them, beside v6's. The type no longer names the format:
// limes_v does, and a verifier reads the claims under the schema it names,
// strictly as ever, so an at+jwt of another issuer's making, or a v6 payload
// under this type, is refused before any claim is trusted. That makes the
// issuer's key the one thing that tells a capability from another access
// token: never share it with an authorization server that mints at+jwt
// (docs/adr/0004-token-format-v7.md).
const TokenTypeV7 TokenType = "at+jwt" //nolint:gosec // G101: a JWT type label, not a credential

// formatV7 is the limes_v of a TokenTypeV7 token. A later format under the
// same type would be read by dispatching on it, as on typ before.
const formatV7 = 7

// TokenType is a token format, as the JWT "typ" header names it.
type TokenType string

// tokenTypes are the token types this package has a schema for.
var tokenTypes = []TokenType{TokenTypeV6, TokenTypeV7}

// SigningAlgorithm is the JWS alg a token names: Ed25519, by either of its
// names.
type SigningAlgorithm string

// The names of Ed25519 a token may carry.
const (
	// SigningAlgorithmEd25519 is the fully-specified name of RFC 9864.
	SigningAlgorithmEd25519 SigningAlgorithm = jose.AlgEd25519
	// SigningAlgorithmEdDSA is the polymorphic name RFC 9864 deprecates, the
	// only one many JOSE libraries, gateways and identity providers know.
	SigningAlgorithmEdDSA SigningAlgorithm = jose.AlgEdDSA
)

// signingAlgorithms are the names a signer writes and a verifier reads.
var signingAlgorithms = []SigningAlgorithm{SigningAlgorithmEd25519, SigningAlgorithmEdDSA}

func parseHeader(seg string) (jwtHeader, error) {
	raw, err := jose.UnB64(seg)
	if err != nil {
		return jwtHeader{}, fmt.Errorf("limes: decode header: %w", err)
	}
	var h jwtHeader
	if err := json.Unmarshal(raw, &h, strictClaims); err != nil {
		return jwtHeader{}, fmt.Errorf("limes: parse header: %w", err)
	}
	// Either name of Ed25519: the fully-specified one of RFC 9864, or the
	// polymorphic EdDSA it deprecates, which a signer writes for JOSE
	// libraries that know no other (SignerConfig.Algorithm). Both name one
	// algorithm over the one key type a verifier resolves, so the header
	// still chooses nothing (RFC 8725 §3.1).
	if !slices.Contains(signingAlgorithms, SigningAlgorithm(h.Alg)) {
		return jwtHeader{}, fmt.Errorf("limes: unexpected alg %q (want one of %q)", h.Alg, signingAlgorithms)
	}
	return h, nil
}

// claimsOf is the payload Sign writes for c under the token type typ: the
// one place a Capability becomes claims.
func claimsOf(g grant, typ TokenType) jwtClaims {
	c := g.Capability
	claims := jwtClaims{
		Issuer:    c.Issuer,
		Subject:   c.Subject.Subject,
		Audience:  c.Audience,
		IssuedAt:  c.IssuedAt.Unix(),
		ExpiresAt: c.ExpiresAt.Unix(),
		ID:        c.ID.String(),
		Principal: toWirePrincipal(c.Subject),
		Caveats:   toWireCaveats(c.Caveats),
	}
	if !c.NotBefore.IsZero() {
		claims.NotBefore = c.NotBefore.Unix()
	}
	if c.ParentID != uuid.Nil() {
		claims.ParentID = c.ParentID.String()
	}
	claims.BiscuitRoot = g.biscuitRoot
	if c.ConfirmationJKT != "" {
		claims.Confirmation = &cnfClaim{JKT: c.ConfirmationJKT}
	}
	if typ == TokenTypeV7 {
		claims.ClientID, claims.Format = c.ClientID, formatV7
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
	if err := json.Unmarshal(raw, &claims, strictClaims); err != nil {
		return nil, fmt.Errorf("limes: parse claims: %w", err)
	}
	switch typ {
	case TokenTypeV6:
		if claims.ClientID != "" || claims.Format != 0 {
			return nil, errors.New("limes: a v6 token carries no client_id or limes_v")
		}
		if len(claims.Caveats.ResourceNamePrefixes) > 0 {
			return nil, errors.New("limes: a v6 token carries no resource_name_prefixes")
		}
	case TokenTypeV7:
		if claims.Format != formatV7 {
			return nil, fmt.Errorf("limes: limes_v %d is not the format of typ %q (%d)", claims.Format, typ, formatV7)
		}
		if claims.ClientID == "" {
			return nil, errors.New("limes: an at+jwt capability names its client_id (RFC 9068 §2.2)")
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

	id, err := canonicalUUID("jti", claims.ID)
	if err != nil {
		return nil, err
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
	cap.Subject = claims.Principal.principal()
	if claims.Confirmation != nil {
		// A cnf the signer writes always names a key; an empty one would read
		// as an unbound capability.
		if claims.Confirmation.JKT == "" {
			return nil, errors.New("limes: cnf names no jkt")
		}
		cap.ConfirmationJKT = claims.Confirmation.JKT
	}
	cap.ClientID = claims.ClientID
	if claims.ParentID != "" {
		pid, err := canonicalUUID("limes_parent_id", claims.ParentID)
		if err != nil {
			return nil, err
		}
		cap.ParentID = pid
	}
	return &grant{Capability: *cap, biscuitRoot: claims.BiscuitRoot}, nil
}

// canonicalUUID reads claim's value as the signer writes a UUID: lowercase,
// hyphenated, and not the nil UUID. uuid.Parse also reads braces, a urn:uuid:
// prefix, uppercase and no hyphens, which would give one capability several
// signed spellings.
func canonicalUUID(claim, s string) (uuid.UUID, error) {
	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.Nil(), fmt.Errorf("limes: parse %s: %w", claim, err)
	}
	if id == uuid.Nil() || id.String() != s {
		return uuid.Nil(), fmt.Errorf("limes: %s %q is not a canonical UUID", claim, s)
	}
	return id, nil
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
