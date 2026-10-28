package exchange

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jws"
	"github.com/lestrrat-go/jwx/v3/jwt"
)

// Subject is a subject token a SubjectVerifier accepted: who it names, for
// which client, and until when.
type Subject struct {
	// Issuer and Subject are the token's iss and sub.
	Issuer, Subject string
	// ClientID is the client the token was issued to.
	ClientID string
	// Audience is the token's aud: the resources it was issued for.
	Audience []string
	// Scopes are the token's scope, split at spaces.
	Scopes []string
	// ID is the token's jti.
	ID string
	// IssuedAt and ExpiresAt are its iat and exp.
	IssuedAt, ExpiresAt time.Time
	// JKT is the RFC 7638 thumbprint of the key the token is bound to
	// (RFC 9449 cnf.jkt); empty for a bearer token.
	JKT string
	// Claims are all of the token's claims, for a policy that reads one of
	// its identity provider's own, such as an organisation.
	Claims map[string]any
}

// SubjectVerifier verifies a subject token: its signature, its issuer and
// its lifetime. Whether its audience is the client presenting it is the
// Exchanger's to check, with the Client.
type SubjectVerifier interface {
	VerifySubject(ctx context.Context, token string) (Subject, error)
}

// The JWT access token profile (RFC 9068): the types §2.1 gives the header,
// and the claims §2.2 requires.
const (
	TypeAccessToken      = "at+jwt"
	TypeAccessTokenMedia = "application/at+jwt"

	claimIssuer   = "iss"
	claimExpiry   = "exp"
	claimAudience = "aud"
	claimSubject  = "sub"
	claimIssuedAt = "iat"
	claimJWTID    = "jti"
	claimScope    = "scope"
	claimCnf      = "cnf"
	cnfJKT        = "jkt"
	// ClaimClientID is the claim RFC 9068 names the client in.
	ClaimClientID = "client_id"
)

// DefaultAlgorithms are the asymmetric JWS algorithms a JWTVerifier accepts
// when its config names none. A symmetric one would let anyone holding the
// key, the identity provider's other clients among them, mint tokens.
var DefaultAlgorithms = []string{"RS256", "RS384", "RS512", "PS256", "PS384", "PS512", "ES256", "ES384", "ES512", "EdDSA", "Ed25519"}

// keyKind is the key type an algorithm verifies with and, for an elliptic
// curve, the curve.
type keyKind struct {
	typ   jwa.KeyType
	curve jwa.EllipticCurveAlgorithm
}

// keyKinds are the keys each of DefaultAlgorithms verifies with (RFC 7518
// §3, RFC 8037, RFC 9864). An algorithm's signature is checked only under a
// key of its kind, never under one another algorithm would use.
var keyKinds = map[string]keyKind{
	"RS256": {typ: jwa.RSA()}, "RS384": {typ: jwa.RSA()}, "RS512": {typ: jwa.RSA()},
	"PS256": {typ: jwa.RSA()}, "PS384": {typ: jwa.RSA()}, "PS512": {typ: jwa.RSA()},
	"ES256": {jwa.EC(), jwa.P256()}, "ES384": {jwa.EC(), jwa.P384()}, "ES512": {jwa.EC(), jwa.P521()},
	"EdDSA": {jwa.OKP(), jwa.Ed25519()}, "Ed25519": {jwa.OKP(), jwa.Ed25519()},
}

// JWTVerifierConfig configures a JWTVerifier.
type JWTVerifierConfig struct {
	// Issuer is the identity provider's issuer identifier, which a token's
	// iss must equal. Required.
	Issuer string
	// Keys are the identity provider's public keys: CachedKeys over its JWKS
	// URL keeps them fresh. Required.
	Keys KeySource
	// Types are the header types accepted, compared without regard to case.
	// Empty: TypeAccessToken and TypeAccessTokenMedia, as RFC 9068 requires.
	// A provider that types its access tokens otherwise ("JWT") is accepted
	// only by naming its type here.
	Types []string
	// ClientIDClaim is the claim naming the client. Empty: ClaimClientID.
	// A provider that names it elsewhere ("azp") is read by naming it here.
	ClientIDClaim string
	// Algorithms are the JWS algorithms accepted. Empty: DefaultAlgorithms.
	Algorithms []string
	// ClockSkew is how far exp, nbf and iat may be off the clock.
	ClockSkew time.Duration
	// Now is the clock. Nil: time.Now.
	Now func() time.Time
}

// KeySource gives the identity provider's current public keys, or an error
// when it has none to give: its JWKS out of reach and never fetched. That is
// the server's failure, answered as one, never a refusal of the token.
type KeySource interface {
	Keys(ctx context.Context) (jwk.Set, error)
}

// staticKeys is a KeySource of a fixed set.
type staticKeys struct{ set jwk.Set }

func (k staticKeys) Keys(context.Context) (jwk.Set, error) { return k.set, nil }

// StaticKeys is a KeySource of a fixed key set.
func StaticKeys(set jwk.Set) KeySource { return staticKeys{set: set} }

// cachedKeys is a KeySource over a jwk.Cache.
type cachedKeys struct {
	cache *jwk.Cache
	url   string
}

func (k cachedKeys) Keys(ctx context.Context) (jwk.Set, error) {
	set, err := k.cache.Lookup(ctx, k.url)
	if err != nil {
		return nil, fmt.Errorf("exchange: the identity provider's keys at %s: %w", k.url, err)
	}
	return set, nil
}

// CachedKeys is a KeySource over cache, which keeps the JWKS at url fresh;
// url must be registered with it (jwk.Cache.Register). A set fetched once is
// served while the endpoint is out of reach; one never fetched is an error.
func CachedKeys(cache *jwk.Cache, url string) KeySource { return cachedKeys{cache: cache, url: url} }

// JWTVerifier is the SubjectVerifier for JWT access tokens (RFC 9068),
// verified through jwx: the library that implements the format, as no
// identity provider's SDK covers every provider.
type JWTVerifier struct {
	cfg JWTVerifierConfig
}

// NewJWTVerifier builds a JWTVerifier, refusing a config with no issuer or
// no keys, or one accepting a symmetric algorithm.
func NewJWTVerifier(cfg JWTVerifierConfig) (*JWTVerifier, error) {
	if cfg.Issuer == "" {
		return nil, errors.New("exchange: JWTVerifierConfig.Issuer required")
	}
	if cfg.Keys == nil {
		return nil, errors.New("exchange: JWTVerifierConfig.Keys required")
	}
	if len(cfg.Types) == 0 {
		cfg.Types = []string{TypeAccessToken, TypeAccessTokenMedia}
	}
	if cfg.ClientIDClaim == "" {
		cfg.ClientIDClaim = ClaimClientID
	}
	if len(cfg.Algorithms) == 0 {
		cfg.Algorithms = DefaultAlgorithms
	}
	for _, alg := range cfg.Algorithms {
		if !slices.Contains(DefaultAlgorithms, alg) {
			return nil, fmt.Errorf("exchange: JWTVerifierConfig.Algorithms names %q, which is not one of %q", alg, DefaultAlgorithms)
		}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	cfg.Types, cfg.Algorithms = slices.Clone(cfg.Types), slices.Clone(cfg.Algorithms)
	return &JWTVerifier{cfg: cfg}, nil
}

// VerifySubject verifies token as an RFC 9068 access token of the
// configured issuer: its type and algorithm, its signature under the
// issuer's keys, its lifetime, and every claim the profile requires. A
// token it refuses is ErrInvalidGrant; keys it cannot get are another
// error, the server's.
func (v *JWTVerifier) VerifySubject(ctx context.Context, token string) (Subject, error) {
	msg, err := jws.ParseString(token)
	if err != nil || len(msg.Signatures()) != 1 {
		return Subject{}, fmt.Errorf("%w: not a compact JWS", ErrInvalidGrant)
	}
	headers := msg.Signatures()[0].ProtectedHeaders()
	typ, _ := headers.Type()
	if !slices.ContainsFunc(v.cfg.Types, func(t string) bool { return strings.EqualFold(t, typ) }) {
		return Subject{}, fmt.Errorf("%w: typ %q is not one of %q", ErrInvalidGrant, typ, v.cfg.Types)
	}
	alg, _ := headers.Algorithm()
	if !slices.Contains(v.cfg.Algorithms, alg.String()) {
		return Subject{}, fmt.Errorf("%w: alg %q is not one of %q", ErrInvalidGrant, alg, v.cfg.Algorithms)
	}
	keys, err := v.cfg.Keys.Keys(ctx)
	if err != nil {
		return Subject{}, err
	}
	kid, _ := headers.KeyID()
	key, err := keyFor(keys, kid, alg)
	if err != nil {
		return Subject{}, err
	}
	opts := []jwt.ParseOption{
		jwt.WithContext(ctx), jwt.WithKey(alg, key), jwt.WithIssuer(v.cfg.Issuer),
		jwt.WithClock(jwt.ClockFunc(v.cfg.Now)), jwt.WithAcceptableSkew(v.cfg.ClockSkew),
	}
	for _, claim := range []string{claimIssuer, claimExpiry, claimAudience, claimSubject, v.cfg.ClientIDClaim, claimIssuedAt, claimJWTID} {
		opts = append(opts, jwt.WithRequiredClaim(claim))
	}
	tok, err := jwt.ParseString(token, opts...)
	if err != nil {
		return Subject{}, fmt.Errorf("%w: %w", ErrInvalidGrant, err)
	}
	return subjectOf(tok, v.cfg.ClientIDClaim)
}

// keyFor is the key of keys the header names by kid, if it is one alg
// verifies with: of alg's key type and curve, and naming no other algorithm.
// A key set may hold keys for several algorithms, and jwx would verify with
// the algorithm a key names whatever the header says; the header's alg is
// the one the configured algorithms admitted, so it is the one verified.
func keyFor(keys jwk.Set, kid string, alg jwa.SignatureAlgorithm) (jwk.Key, error) {
	if kid == "" {
		return nil, fmt.Errorf("%w: no kid", ErrInvalidGrant)
	}
	key, ok := keys.LookupKeyID(kid)
	if !ok {
		return nil, fmt.Errorf("%w: kid %q is no key of the issuer's", ErrInvalidGrant, kid)
	}
	if named, ok := key.Algorithm(); ok && named.String() != alg.String() {
		return nil, fmt.Errorf("%w: kid %q is a key for %q, not %q", ErrInvalidGrant, kid, named, alg)
	}
	if err := checkKeyKind(key, kid, alg); err != nil {
		return nil, err
	}
	return key, nil
}

// checkKeyKind refuses a key, kid, of another type than alg verifies with,
// or on another curve than alg needs.
func checkKeyKind(key jwk.Key, kid string, alg jwa.SignatureAlgorithm) error {
	kind := keyKinds[alg.String()]
	if key.KeyType() != kind.typ {
		return fmt.Errorf("%w: kid %q is a %s key, not one %q verifies with", ErrInvalidGrant, kid, key.KeyType(), alg)
	}
	if kind.curve == (jwa.EllipticCurveAlgorithm{}) {
		return nil
	}
	c, ok := key.(interface {
		Crv() (jwa.EllipticCurveAlgorithm, bool)
	})
	if !ok {
		return fmt.Errorf("%w: kid %q names no curve", ErrInvalidGrant, kid)
	}
	if crv, ok := c.Crv(); !ok || crv != kind.curve {
		return fmt.Errorf("%w: kid %q is not on %s, as %q needs", ErrInvalidGrant, kid, kind.curve, alg)
	}
	return nil
}

// subjectOf is the Subject tok, a verified token, names.
func subjectOf(tok jwt.Token, clientIDClaim string) (Subject, error) {
	s := Subject{}
	s.Issuer, _ = tok.Issuer()
	s.Subject, _ = tok.Subject()
	s.Audience, _ = tok.Audience()
	s.ID, _ = tok.JwtID()
	s.IssuedAt, _ = tok.IssuedAt()
	s.ExpiresAt, _ = tok.Expiration()
	if err := tok.Get(clientIDClaim, &s.ClientID); err != nil || s.ClientID == "" {
		return Subject{}, fmt.Errorf("%w: %s is not a non-empty string", ErrInvalidGrant, clientIDClaim)
	}
	var err error
	if s.Scopes, err = scopesOf(tok); err != nil {
		return Subject{}, err
	}
	if s.JKT, err = jktOf(tok); err != nil {
		return Subject{}, err
	}
	s.Claims = claimsOf(tok)
	return s, nil
}

// scopesOf is the token's scope claim, split; nil for none.
func scopesOf(tok jwt.Token) ([]string, error) {
	if !tok.Has(claimScope) {
		return nil, nil
	}
	var scope string
	if err := tok.Get(claimScope, &scope); err != nil {
		return nil, fmt.Errorf("%w: %s is not a string", ErrInvalidGrant, claimScope)
	}
	return strings.Fields(scope), nil
}

// jktOf is the DPoP key the token's cnf binds it to, "" for none.
func jktOf(tok jwt.Token) (string, error) {
	if !tok.Has(claimCnf) {
		return "", nil
	}
	var cnf map[string]any
	if err := tok.Get(claimCnf, &cnf); err != nil {
		return "", fmt.Errorf("%w: %s is not an object", ErrInvalidGrant, claimCnf)
	}
	// A binding this module cannot check, to a certificate (RFC 8705)
	// or anything but a DPoP key, would be dropped by a bearer
	// capability: such a token is refused.
	jkt, ok := cnf[cnfJKT].(string)
	if !ok || jkt == "" || len(cnf) != 1 {
		return "", fmt.Errorf("%w: %s binds the token otherwise than by a DPoP key", ErrInvalidGrant, claimCnf)
	}
	return jkt, nil
}

// claimsOf is every claim of the token the policy may read.
func claimsOf(tok jwt.Token) map[string]any {
	claims := map[string]any{}
	for _, key := range tok.Keys() {
		var value any
		if err := tok.Get(key, &value); err == nil {
			claims[key] = value
		}
	}
	return claims
}
