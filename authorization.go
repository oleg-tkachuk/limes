package limes

import (
	"errors"
	"fmt"
	"strings"

	"github.com/oleg-tkachuk/limes/internal/rfc3986"
)

// The Authorization request header, after RFC 9110 §11.6.2 and RFC 9449
// §7.1: `Authorization: DPoP <token>` for a key-bound capability,
// `Authorization: Bearer <token>` (RFC 6750 §2.1) for one that is not.

// AuthorizationHeader is the request header a token travels in.
const AuthorizationHeader = "Authorization"

// AuthScheme is the authentication scheme a token was presented under.
type AuthScheme string

const (
	// AuthSchemeUnspecified is a token that did not come from an
	// Authorization header; VerifyRequest checks no scheme for it.
	AuthSchemeUnspecified AuthScheme = ""
	// AuthSchemeBearer presents a bearer token (RFC 6750).
	AuthSchemeBearer AuthScheme = "Bearer"
	// AuthSchemeDPoP presents a key-bound token (RFC 9449 §7.1).
	AuthSchemeDPoP AuthScheme = "DPoP"
)

// ErrMalformedAuthorization — an Authorization header that is not one
// scheme this module reads followed by one token68 credential. It is a
// malformed request (RFC 6750 §3.1 invalid_request), not a bad token.
var ErrMalformedAuthorization = errors.New("limes: malformed Authorization header")

// ErrAuthSchemeMismatch — the scheme the token came under is not the one its
// binding calls for: a key-bound token as Bearer, which RFC 9449 §7.2 treats
// as a downgrade, or an unbound one as DPoP. Matches ErrProofOfPossession: the
// binding, not the token, is what the scheme fails.
var ErrAuthSchemeMismatch error = &familyError{"limes: token presented under the wrong scheme", ErrProofOfPossession}

// ParseAuthorization reads an Authorization header value: the scheme, matched
// without regard to case (RFC 9110 §11.1), and its token68 credential.
func ParseAuthorization(header string) (AuthScheme, string, error) {
	name, credential, ok := strings.Cut(header, " ")
	if !ok {
		return "", "", fmt.Errorf("%w: no credential", ErrMalformedAuthorization)
	}
	var scheme AuthScheme
	switch {
	case strings.EqualFold(name, string(AuthSchemeBearer)):
		scheme = AuthSchemeBearer
	case strings.EqualFold(name, string(AuthSchemeDPoP)):
		scheme = AuthSchemeDPoP
	default:
		return "", "", fmt.Errorf("%w: scheme %q", ErrMalformedAuthorization, name)
	}
	credential = strings.TrimLeft(credential, " ")
	if !isToken68(credential) {
		return "", "", fmt.Errorf("%w: the credential is not a token68", ErrMalformedAuthorization)
	}
	return scheme, credential, nil
}

// isToken68 reports whether s is an RFC 9110 §11.2 token68:
// 1*( ALPHA / DIGIT / "-" / "." / "_" / "~" / "+" / "/" ) *"=".
func isToken68(s string) bool {
	body := strings.TrimRight(s, "=")
	if body == "" {
		return false
	}
	for i := 0; i < len(body); i++ {
		c := body[i]
		if !rfc3986.IsUnreserved(c) && c != '+' && c != '/' {
			return false
		}
	}
	return true
}

// checkScheme holds a token's scheme to its binding (RFC 9449 §7.1, §7.2).
func checkScheme(scheme AuthScheme, bound bool) error {
	switch {
	case scheme == AuthSchemeUnspecified:
		return nil
	case bound && scheme != AuthSchemeDPoP:
		return fmt.Errorf("%w: a key-bound token came as %s, not %s", ErrAuthSchemeMismatch, scheme, AuthSchemeDPoP)
	case !bound && scheme == AuthSchemeDPoP:
		return fmt.Errorf("%w: a bearer token came as %s", ErrAuthSchemeMismatch, AuthSchemeDPoP)
	}
	return nil
}
