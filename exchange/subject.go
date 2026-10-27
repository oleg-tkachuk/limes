package exchange

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

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

// JWTVerifierConfig configures a JWTVerifier.
type JWTVerifierConfig struct {
	// Issuer is the identity provider's issuer identifier, which a token's
	// iss must equal. Required.
	Issuer string
	// Keys are the identity provider's public keys: a jwk.CachedSet over
	// its JWKS URL keeps them fresh, under the lifetime of the jwk.Cache
	// behind it. Required.
	Keys jwk.Set
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
// token it refuses is ErrInvalidGrant.
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
	opts := []jwt.ParseOption{
		jwt.WithContext(ctx), jwt.WithKeySet(v.cfg.Keys), jwt.WithIssuer(v.cfg.Issuer),
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
	if tok.Has(claimScope) {
		var scope string
		if err := tok.Get(claimScope, &scope); err != nil {
			return Subject{}, fmt.Errorf("%w: %s is not a string", ErrInvalidGrant, claimScope)
		}
		s.Scopes = strings.Fields(scope)
	}
	if tok.Has(claimCnf) {
		var cnf map[string]any
		if err := tok.Get(claimCnf, &cnf); err != nil {
			return Subject{}, fmt.Errorf("%w: %s is not an object", ErrInvalidGrant, claimCnf)
		}
		// A binding this module cannot check, to a certificate (RFC 8705)
		// or anything but a DPoP key, would be dropped by a bearer
		// capability: such a token is refused.
		jkt, ok := cnf[cnfJKT].(string)
		if !ok || jkt == "" || len(cnf) != 1 {
			return Subject{}, fmt.Errorf("%w: %s binds the token otherwise than by a DPoP key", ErrInvalidGrant, claimCnf)
		}
		s.JKT = jkt
	}
	s.Claims = map[string]any{}
	for _, key := range tok.Keys() {
		var value any
		if err := tok.Get(key, &value); err == nil {
			s.Claims[key] = value
		}
	}
	return s, nil
}
