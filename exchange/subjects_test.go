package exchange

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jws"
)

// The JWT access token profile (RFC 9068) as the tests' identity provider
// signs it: the header type, and the claims.
const (
	typeAccessToken = "at+jwt"
	claimIssuer     = "iss"
	claimExpiry     = "exp"
	claimAudience   = "aud"
	claimSubject    = "sub"
	claimIssuedAt   = "iat"
	claimJWTID      = "jti"
	claimScope      = "scope"
	claimCnf        = "cnf"
	cnfJKT          = "jkt"
	claimClientID   = "client_id"
	claimSessionID  = "sid"
)

// testSubjects is the tests' SubjectVerifier: their identity provider's
// signature checked and its claims read, as an exchange receives a subject.
// Package exchange links no JOSE library; the verifier consumers use is
// exchange/jwtverify's, tested there, which imports this package and so
// cannot be imported by its tests.
type testSubjects struct{ idp *idp }

func (v testSubjects) VerifySubject(_ context.Context, token string) (Subject, error) {
	payload, err := jws.Verify([]byte(token), jws.WithKey(jwa.EdDSAEd25519(), v.idp.key.Public()))
	if err != nil {
		return Subject{}, fmt.Errorf("%w: %w", ErrInvalidGrant, err)
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return Subject{}, fmt.Errorf("%w: %w", ErrInvalidGrant, err)
	}
	s := Subject{Claims: claims}
	s.Issuer, _ = claims[claimIssuer].(string)
	s.Subject, _ = claims[claimSubject].(string)
	s.ClientID, _ = claims[claimClientID].(string)
	s.ID, _ = claims[claimJWTID].(string)
	switch aud := claims[claimAudience].(type) {
	case string:
		s.Audience = []string{aud}
	case []any:
		for _, a := range aud {
			if str, ok := a.(string); ok {
				s.Audience = append(s.Audience, str)
			}
		}
	}
	if scope, ok := claims[claimScope].(string); ok {
		s.Scopes = strings.Fields(scope)
	}
	if cnf, ok := claims[claimCnf].(map[string]any); ok {
		s.JKT, _ = cnf[cnfJKT].(string)
	}
	s.SessionID, _ = claims[claimSessionID].(string)
	iat, _ := claims[claimIssuedAt].(float64)
	exp, _ := claims[claimExpiry].(float64)
	s.IssuedAt, s.ExpiresAt = time.Unix(int64(iat), 0), time.Unix(int64(exp), 0)
	if s.Issuer != idpIssuer || s.Subject == "" || s.ClientID == "" || !clock().Before(s.ExpiresAt) {
		return Subject{}, fmt.Errorf("%w: not a live access token of the identity provider", ErrInvalidGrant)
	}
	return s, nil
}
