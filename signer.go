package limes

import (
	"cmp"
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json/jsontext"
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
	"github.com/oleg-tkachuk/limes/jwks"
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
	// alg is the alg its tokens name.
	alg SigningAlgorithm
}

// SignerConfig configures a Signer.
type SignerConfig struct {
	// KeyID is the kid tokens carry, by which verifiers find the key.
	// Required, in limes's namespace: jwks.KeyIDPrefix, as jwks.KeyID and
	// GenerateKey write it.
	KeyID string
	// Key signs, sealed (SealKey): an ed25519.PrivateKey, or a
	// crypto.Signer over a key held elsewhere — one that also implements
	// ContextSigner is given the request's context. Its public key must be
	// Ed25519. Required.
	Key SealedKey
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

// NewSigner builds a Signer, refusing a kid outside limes's namespace or a
// key that is not Ed25519.
func NewSigner(cfg SignerConfig) (*Signer, error) {
	if !jwks.IsKeyID(cfg.KeyID) {
		return nil, fmt.Errorf("limes: SignerConfig.KeyID %q is not a limes kid (%q and a name)", cfg.KeyID, jwks.KeyIDPrefix)
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
	alg := cmp.Or(cfg.Algorithm, SigningAlgorithmEd25519)
	if !slices.Contains(signingAlgorithms, alg) {
		return nil, fmt.Errorf("limes: SignerConfig.Algorithm %q is not one of %q", alg, signingAlgorithms)
	}
	return &Signer{keyID: cfg.KeyID, key: cfg.Key.s, pub: [ed25519.PublicKeySize]byte(pub), alg: alg}, nil
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

	// ClientID and Format are required: RFC 9068's client_id, and the
	// capability format, which at+jwt leaves the type unable to name.
	ClientID string `json:"client_id"`
	Format   int64  `json:"limes_v"`

	// OnBehalfOf and Actor are a token's whose capability acts on behalf of
	// another (Capability.OnBehalfOf): that principal, whose subject sub
	// then is, and RFC 8693's act naming the holder, prior actors nested
	// within it. Present together or not at all.
	OnBehalfOf *wirePrincipal `json:"limes_on_behalf_of,omitzero"`
	Actor      *actClaim      `json:"act,omitzero"`
}

// actClaim is RFC 8693 §4.1's act: the party acting, and nested within it
// the party that acted before it.
type actClaim struct {
	Subject string    `json:"sub"`
	Actor   *actClaim `json:"act,omitzero"`
}

// actOf is the act claim of a holder and the prior actors it was delegated
// through, nearest first.
func actOf(holder string, prior []string) *actClaim {
	act := &actClaim{Subject: holder}
	for at, i := act, 0; i < len(prior); i++ {
		at.Actor = &actClaim{Subject: prior[i]}
		at = at.Actor
	}
	return act
}

// priorActors reads back what actOf wrote: the actors nested in act, nearest
// first, refusing an empty subject and a chain longer than any delegation.
func priorActors(act *actClaim) ([]string, error) {
	var prior []string
	for at := act.Actor; at != nil; at = at.Actor {
		if at.Subject == "" {
			return nil, errors.New("limes: an act names no sub")
		}
		if len(prior) == MaxDelegationDepth {
			return nil, fmt.Errorf("limes: act nests more than %d prior actors", MaxDelegationDepth)
		}
		prior = append(prior, at.Subject)
	}
	return prior, nil
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
// two ways. An id inside a claim (a tenant, a run) is read only in the
// canonical spelling the signer writes, as jti and limes_parent_id are
// (canonicalUUID): a reader comparing the strings would otherwise see two
// values for one id.
var strictClaims = json.JoinOptions(
	json.RejectUnknownMembers(true),
	json.WithUnmarshalers(json.UnmarshalFromFunc(unmarshalCanonicalUUID)),
)

// unmarshalCanonicalUUID reads a JSON string into id only when it is the id's
// canonical spelling: lowercase and hyphenated. The nil id is its own
// canonical spelling here; whether it is allowed is the claim's to say.
func unmarshalCanonicalUUID(dec *jsontext.Decoder, id *uuid.UUID) error {
	tok, err := dec.ReadToken()
	if err != nil {
		return err
	}
	if tok.Kind() != '"' {
		return fmt.Errorf("limes: a UUID is a JSON string, not %v", tok.Kind())
	}
	s := tok.String()
	parsed, err := uuid.Parse(s)
	if err != nil {
		return fmt.Errorf("limes: parse UUID: %w", err)
	}
	if parsed.String() != s {
		return fmt.Errorf("limes: %q is not a canonical UUID", s)
	}
	*id = parsed
	return nil
}

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
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := checkSignable(g.Capability); err != nil {
		return "", err
	}
	claims := claimsOf(g)
	// Nothing is signed that the verifier would refuse to read.
	if err := checkIJSONIntegers(claims); err != nil {
		return "", err
	}
	signingInput, err := s.signingInput(claims)
	if err != nil {
		return "", err
	}
	sig, err := s.signature(ctx, signingInput)
	if err != nil {
		return "", err
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// checkSignable refuses a capability the verifier would refuse to read:
// nothing is signed that it would not.
func checkSignable(c Capability) error {
	if err := checkRequired(c); err != nil {
		return err
	}
	if err := c.Caveats.Validate(); err != nil {
		return err
	}
	if err := c.Subject.Validate(); err != nil {
		return err
	}
	if c.ExpiresAt.Before(c.IssuedAt) || c.ExpiresAt.IsZero() {
		return errors.New("limes: ExpiresAt must be after IssuedAt")
	}
	return checkActing(c)
}

// checkRequired refuses a capability missing a member every token carries.
func checkRequired(c Capability) error {
	switch {
	case c.ID == uuid.Nil():
		return errors.New("limes: ID required")
	case c.Issuer == "":
		return errors.New("limes: Issuer required")
	case c.Subject.TenantID == uuid.Nil():
		return errors.New("limes: Subject.TenantID required")
	case c.Subject.Subject == "":
		return errors.New("limes: Subject.Subject required (RFC 9068 sub)")
	case len(c.Caveats.Ops) == 0:
		return errors.New("limes: at least one Op required")
	case c.ClientID == "":
		// RFC 9068 makes client_id required of an at+jwt: none is invented.
		return errors.New("limes: a capability needs a ClientID (RFC 9068 client_id)")
	}
	return nil
}

// signingInput is the JWS signing input of a token carrying claims: its
// header and claims, each base64url-encoded, joined by ".".
func (s *Signer) signingInput(claims jwtClaims) (string, error) {
	header := jwtHeader{Alg: string(s.alg), Kid: s.keyID, Typ: TokenType}
	headerJSON, err := json.Marshal(header)
	if err != nil {
		return "", fmt.Errorf("limes: marshal header: %w", err)
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("limes: marshal claims: %w", err)
	}
	return strings.Join([]string{
		base64.RawURLEncoding.EncodeToString(headerJSON),
		base64.RawURLEncoding.EncodeToString(claimsJSON),
	}, "."), nil
}

// signature signs signingInput with the signer's key, and refuses what is no
// signature of it under the key's public key.
func (s *Signer) signature(ctx context.Context, signingInput string) ([]byte, error) {
	// Ed25519 hashes the message itself (PureEdDSA): crypto.Hash(0) is the
	// stdlib's way to ask for it.
	var sig []byte
	var err error
	if cs, ok := s.key.open().(ContextSigner); ok {
		sig, err = cs.SignContext(ctx, rand.Reader, []byte(signingInput), crypto.Hash(0))
	} else {
		sig, err = s.key.open().Sign(rand.Reader, []byte(signingInput), crypto.Hash(0))
	}
	if err != nil {
		return nil, fmt.Errorf("limes: sign: %w", err)
	}
	if len(sig) != ed25519.SignatureSize {
		return nil, fmt.Errorf("limes: sign: an Ed25519 signature is %d bytes, not %d", ed25519.SignatureSize, len(sig))
	}
	// A key held elsewhere — a KMS, an HSM — may sign with another key than
	// the one it reports, or return bytes that are no signature at all:
	// a token carrying either would be refused by every verifier, and
	// recorded as issued.
	if !ed25519.Verify(s.pub[:], []byte(signingInput), sig) {
		return nil, errors.New("limes: sign: the signature does not verify under the key's public key")
	}
	return sig, nil
}

// TokenType is the JWT typ of every capability token: an OAuth 2.0 access
// token in the JWT profile of RFC 9068, so a resource server or gateway that
// validates that profile reads it, with the claims the profile requires,
// client_id among them. The type does not name the format: limes_v does, and
// a verifier reads the claims under the schema it names, strictly, so an
// at+jwt of another issuer's making is refused before any claim is trusted.
// What else tells a capability from another access token is its key, whose
// kid is in limes's namespace (jwks.KeyIDPrefix): never share it with an
// authorization server that mints at+jwt (docs/adr/0006-one-token-format.md).
//
// Earlier formats, each with a type of its own, are not accepted:
// limes-cap-v6+jwt, v5, v4 and v3.
const TokenType = "at+jwt" //nolint:gosec // G101: a JWT type label, not a credential

// formatV7 is the limes_v of a capability token. A later format under the
// same type would be read by dispatching on it.
const formatV7 = 7

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

// claimsOf is the payload Sign writes for c: the one place a Capability
// becomes claims.
func claimsOf(g grant) jwtClaims {
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
		ClientID:  c.ClientID,
		Format:    formatV7,
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
	if c.OnBehalfOf != nil {
		obo := toWirePrincipal(*c.OnBehalfOf)
		claims.Subject, claims.OnBehalfOf = c.OnBehalfOf.Subject, &obo
		claims.Actor = actOf(c.Subject.Subject, c.PriorActors)
	}
	return claims
}

// parseClaims reads a payload under the schema its limes_v names.
func parseClaims(seg string) (*grant, error) {
	claims, err := decodeClaims(seg)
	if err != nil {
		return nil, err
	}
	return grantOf(claims)
}

// decodeClaims decodes a payload strictly, refusing one of another format,
// without a client_id, or with an integer outside I-JSON.
func decodeClaims(seg string) (jwtClaims, error) {
	raw, err := jose.UnB64(seg)
	if err != nil {
		return jwtClaims{}, fmt.Errorf("limes: decode claims: %w", err)
	}
	var claims jwtClaims
	if err := json.Unmarshal(raw, &claims, strictClaims); err != nil {
		return jwtClaims{}, fmt.Errorf("limes: parse claims: %w", err)
	}
	if claims.Format != formatV7 {
		return jwtClaims{}, fmt.Errorf("limes: limes_v %d is not the format this package reads (%d)", claims.Format, formatV7)
	}
	if claims.ClientID == "" {
		return jwtClaims{}, errors.New("limes: a capability names its client_id (RFC 9068 §2.2)")
	}
	return claims, checkIJSONIntegers(claims)
}

// grantOf is the grant claims carry, refused when what they say of it
// disagrees with itself.
func grantOf(claims jwtClaims) (*grant, error) {
	onBehalfOf, prior, err := readActing(claims)
	if err != nil {
		return nil, err
	}
	id, err := canonicalUUID("jti", claims.ID)
	if err != nil {
		return nil, err
	}
	jkt, err := readConfirmation(claims.Confirmation)
	if err != nil {
		return nil, err
	}
	parentID, err := readParentID(claims.ParentID, prior)
	if err != nil {
		return nil, err
	}
	c := Capability{
		ID:              id,
		Issuer:          claims.Issuer,
		Subject:         claims.Principal.principal(),
		Audience:        claims.Audience,
		Caveats:         claims.Caveats.caveats(),
		IssuedAt:        time.Unix(claims.IssuedAt, 0).UTC(),
		ExpiresAt:       time.Unix(claims.ExpiresAt, 0).UTC(),
		ConfirmationJKT: jkt,
		ClientID:        claims.ClientID,
		OnBehalfOf:      onBehalfOf,
		PriorActors:     prior,
		ParentID:        parentID,
	}
	if claims.NotBefore != 0 {
		c.NotBefore = time.Unix(claims.NotBefore, 0).UTC()
	}
	return &grant{Capability: c, biscuitRoot: claims.BiscuitRoot}, nil
}

// readConfirmation is the key a cnf binds the capability to, "" for none.
func readConfirmation(cnf *cnfClaim) (string, error) {
	if cnf == nil {
		return "", nil
	}
	// A cnf the signer writes always names a key; an empty one would read
	// as an unbound capability.
	if cnf.JKT == "" {
		return "", errors.New("limes: cnf names no jkt")
	}
	return cnf.JKT, nil
}

// readParentID reads limes_parent_id, which a capability with prior actors
// must name: they are the holders it was delegated through.
func readParentID(parentID string, prior []string) (uuid.UUID, error) {
	if parentID == "" {
		if len(prior) > 0 {
			return uuid.Nil(), errors.New("limes: prior actors in act, and no limes_parent_id")
		}
		return uuid.Nil(), nil
	}
	return canonicalUUID("limes_parent_id", parentID)
}

// checkActing refuses to sign what readActing would refuse to read.
func checkActing(c Capability) error {
	if c.OnBehalfOf == nil {
		if len(c.PriorActors) > 0 {
			return errors.New("limes: PriorActors need an OnBehalfOf")
		}
		return nil
	}
	if err := validateOnBehalfOf(*c.OnBehalfOf, c.Subject); err != nil {
		return err
	}
	if len(c.PriorActors) > 0 && c.ParentID == uuid.Nil() {
		return errors.New("limes: PriorActors need a ParentID")
	}
	if len(c.PriorActors) > MaxDelegationDepth || slices.Contains(c.PriorActors, "") {
		return fmt.Errorf("limes: PriorActors are at most %d subjects, none empty", MaxDelegationDepth)
	}
	return nil
}

// readActing reads who a token's capability acts for and through. sub
// repeats limes_principal's subject for JWT tooling, or, when the capability
// acts on behalf of another, limes_on_behalf_of's, with act naming the
// holder: each pair must agree, or a parser that takes one would read
// another capability than one that takes the other.
func readActing(claims jwtClaims) (*Principal, []string, error) {
	if claims.Principal.Subject == "" {
		return nil, nil, errors.New("limes: limes_principal names no subject (RFC 9068 sub)")
	}
	if claims.OnBehalfOf == nil {
		if claims.Actor != nil {
			return nil, nil, errors.New("limes: act without limes_on_behalf_of")
		}
		if claims.Subject != claims.Principal.Subject {
			return nil, nil, fmt.Errorf("limes: sub %q differs from limes_principal.subject %q", claims.Subject, claims.Principal.Subject)
		}
		return nil, nil, nil
	}
	obo := claims.OnBehalfOf.principal()
	if err := validateOnBehalfOf(obo, claims.Principal.principal()); err != nil {
		return nil, nil, err
	}
	if claims.Subject != obo.Subject {
		return nil, nil, fmt.Errorf("limes: sub %q differs from limes_on_behalf_of.subject %q", claims.Subject, obo.Subject)
	}
	if claims.Actor == nil || claims.Actor.Subject != claims.Principal.Subject {
		return nil, nil, errors.New("limes: act names not limes_principal.subject")
	}
	prior, err := priorActors(claims.Actor)
	if err != nil {
		return nil, nil, err
	}
	return &obo, prior, nil
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
	if _, err := parseHeader(parts.Header); err != nil {
		return Capability{}, err
	}
	c, err := parseClaims(parts.Payload)
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
