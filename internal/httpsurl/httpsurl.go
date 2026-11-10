// Package httpsurl is the one rule for where a key set is fetched from, as
// jwks.Remote and the exchange's jwtverify both fetch one: an https URL
// with a host, and redirects that keep to its scheme and host. Over
// anything else whoever sits on the path, or steers the server, chooses the
// keys a verifier trusts.
//
// A released nested module builds against every later limes of its major,
// so what this package exports is kept buildable for as long as one does.
package httpsurl

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Scheme is the only scheme a key set is fetched over.
const Scheme = "https"

// MaxRedirects is how many redirects a fetch follows, net/http's own
// default limit, for a client that sets no CheckRedirect of its own.
const MaxRedirects = 10

var (
	// ErrNotHTTPS is a URL that is not an https URL with a host.
	ErrNotHTTPS = errors.New("not an " + Scheme + " URL with a host")
	// ErrMalformedEscape is a URL with a %-escape url.Parse cannot read,
	// refused for that in place of url.EscapeError, which quotes the escape
	// — and the escape may lie in the password.
	ErrMalformedEscape = errors.New("malformed %-escape")
	// ErrLeavesOrigin is a redirect to another scheme or host.
	ErrLeavesOrigin = errors.New("redirect leaves the origin")
	// ErrTooManyRedirects is a fetch past MaxRedirects.
	ErrTooManyRedirects = errors.New("too many redirects")
)

// Parse reads raw as the URL a key set is fetched from, refusing one that
// is not an https URL with a host. Its error never quotes raw's password:
// an error is what reaches a log.
func Parse(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		// A *url.Error quotes the whole URL, password and all: only the
		// reason is kept.
		if ue, ok := errors.AsType[*url.Error](err); ok {
			err = ue.Err
		}
		if _, ok := errors.AsType[url.EscapeError](err); ok {
			err = ErrMalformedEscape
		}
		return nil, err
	}
	if u.Scheme != Scheme || u.Host == "" {
		// The scheme and host alone: text url.Parse read as no URL's
		// userinfo — a scheme mistyped, say — is echoed whole by
		// Redacted, password and all.
		return nil, fmt.Errorf("%w, got scheme %q and host %q", ErrNotHTTPS, u.Scheme, u.Host)
	}
	return u, nil
}

// SameOrigin is a shallow copy of c, so the caller's client is left as it
// is, whose redirects keep to the scheme and host of the first request: a
// redirect is the server's choice, and one to http or to another host would
// let it, or whoever can steer it, pick the keys. Within that, c's own
// policy decides, or net/http's default when it has none. A redirect
// refused leaves the keys unavailable, the server's failure, not the
// token's.
func SameOrigin(c *http.Client) *http.Client {
	own := c.CheckRedirect
	out := *c
	out.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		first := via[0].URL
		if req.URL.Scheme != Scheme || !strings.EqualFold(req.URL.Host, first.Host) {
			return fmt.Errorf("%w: from %s to %s", ErrLeavesOrigin, first.Redacted(), req.URL.Redacted())
		}
		if own != nil {
			return own(req, via)
		}
		if len(via) >= MaxRedirects {
			return fmt.Errorf("%w: stopped after %d", ErrTooManyRedirects, MaxRedirects)
		}
		return nil
	}
	return &out
}
