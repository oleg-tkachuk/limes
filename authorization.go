package limes

import (
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
	// Authorization header — a gRPC metadata entry, a message field.
	// VerifyRequest checks no scheme for it; a key-bound token still needs
	// its DPoP proof.
	AuthSchemeUnspecified AuthScheme = ""
	// AuthSchemeBearer presents a bearer token (RFC 6750).
	AuthSchemeBearer AuthScheme = "Bearer"
	// AuthSchemeDPoP presents a key-bound token (RFC 9449 §7.1).
	AuthSchemeDPoP AuthScheme = "DPoP"
)

// ErrNoCredentials — the request carries no credential this module reads: no
// Authorization header, or one under a scheme other than Bearer or DPoP. RFC
// 6750 §3.1 answers it with a challenge and no error code: the client has
// not tried, so nothing it sent was wrong.
var ErrNoCredentials error = &familyError{"limes: no credentials", ErrUnauthenticated}

// ErrMalformedAuthorization — an Authorization header that is not one
// scheme this module reads followed by one token68 credential. It is a
// malformed request (RFC 6750 §3.1 invalid_request), not a bad token, so it
// matches ErrInvalidRequest; ErrorResponse still answers it 400, as the
// client's fault, where the rest of that family is the server's own call.
var ErrMalformedAuthorization error = &familyError{"limes: malformed Authorization header", ErrInvalidRequest}

// ErrAuthSchemeMismatch — the scheme the token came under is not the one its
// binding calls for: a key-bound token as Bearer, which RFC 9449 §7.2 treats
// as a downgrade, or an unbound one as DPoP. Matches ErrProofOfPossession: the
// binding, not the token, is what the scheme fails.
var ErrAuthSchemeMismatch error = &familyError{"limes: token presented under the wrong scheme", ErrProofOfPossession}

// ParseAuthorization reads an Authorization header value: the scheme, matched
// without regard to case (RFC 9110 §11.1), and its token68 credential. With
// ErrMalformedAuthorization it still returns the scheme it recognised, so
// ErrorResponse challenges under the scheme the client used.
func ParseAuthorization(header string) (AuthScheme, string, error) {
	if header == "" {
		return "", "", ErrNoCredentials
	}
	name, credential, separated := strings.Cut(header, " ")
	var scheme AuthScheme
	switch {
	case strings.EqualFold(name, string(AuthSchemeBearer)):
		scheme = AuthSchemeBearer
	case strings.EqualFold(name, string(AuthSchemeDPoP)):
		scheme = AuthSchemeDPoP
	default:
		// Another scheme's credentials are none of ours to judge. Its name
		// goes in the error only when it can be no credential: a space must
		// follow it, for a header with none is a bare credential — an API
		// key of letters and digits reads as a scheme name too.
		if !separated || !isSchemeName(name) {
			return "", "", ErrNoCredentials
		}
		return "", "", fmt.Errorf("%w: scheme %q", ErrNoCredentials, name)
	}
	credential = strings.TrimLeft(credential, " ")
	if !isToken68(credential) {
		return scheme, "", fmt.Errorf("%w: the credential is not a token68", ErrMalformedAuthorization)
	}
	return scheme, credential, nil
}

// ParseAuthorizationValues reads every Authorization field a request
// carries — r.Header.Values(AuthorizationHeader), or a gRPC metadata entry's
// values — as ParseAuthorization reads one. More than one is
// ErrMalformedAuthorization, with no scheme: RFC 9449 §7.2 refuses a request
// that uses more than one authentication method, and RFC 6750 §2 one that
// presents a token more than once, so none of them is chosen. None is
// ErrNoCredentials.
func ParseAuthorizationValues(values []string) (AuthScheme, string, error) {
	switch len(values) {
	case 0:
		return ParseAuthorization("")
	case 1:
		return ParseAuthorization(values[0])
	default:
		return AuthSchemeUnspecified, "", fmt.Errorf("%w: %d %s fields", ErrMalformedAuthorization, len(values), AuthorizationHeader)
	}
}

// maxSchemeNameBytes bounds a scheme name an error repeats: longer than any
// registered authentication scheme ("AWS4-HMAC-SHA256" is 16), shorter than
// any credential this module reads.
const maxSchemeNameBytes = 32

// isSchemeName reports whether s reads as a registered authentication scheme
// name — letters, digits and "-", at most maxSchemeNameBytes — and so can be
// repeated in an error without repeating a credential, which carries ".",
// "_", "=", "+", "/" or more bytes.
func isSchemeName(s string) bool {
	if s == "" || len(s) > maxSchemeNameBytes {
		return false
	}
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9', c == '-':
		default:
			return false
		}
	}
	return true
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

// Validate refuses a scheme other than the ones ParseAuthorization returns,
// or AuthSchemeUnspecified, as ErrInvalidRequest.
func (s AuthScheme) Validate() error {
	switch s {
	case AuthSchemeUnspecified, AuthSchemeBearer, AuthSchemeDPoP:
		return nil
	}
	return invalidRequest("unknown authorization scheme %q", s)
}

// checkScheme holds a token's scheme to its binding (RFC 9449 §7.1, §7.2).
// A scheme other than the ones ParseAuthorization returns is the caller's
// error, ErrInvalidRequest.
func checkScheme(scheme AuthScheme, bound bool) error {
	if err := scheme.Validate(); err != nil {
		return err
	}
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
