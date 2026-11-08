package jwtverify

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/internal/errs"
	"github.com/oleg-tkachuk/limes/jwks"
)

// The rule below is jwks.Remote's (jwks/remote.go: parseRemoteURL and
// sameOrigin), kept as a copy rather than shared through limes's
// internal/httpx: a released exchange builds against every later limes of
// its major, so an internal symbol it uses is frozen for v1, and one added
// now would need a limes release cut beside every exchange release that
// first uses it. A change to one copy belongs in the other.

// schemeHTTPS is the only scheme the identity provider's keys are fetched
// over: over anything else whoever sits on the path chooses the keys the
// exchange trusts, and so the subject tokens it mints capabilities for.
const schemeHTTPS = "https"

// maxRedirects is how many redirects a fetch follows, net/http's own default
// limit, for a client that sets no CheckRedirect of its own.
const maxRedirects = 10

// parseKeysURL reads the identity provider's JWKS URL, refusing one that is
// not an https URL with a host. Its error never quotes rawURL, which may
// carry a password: an error is what reaches a log.
func parseKeysURL(rawURL string) (*url.URL, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		// A *url.Error quotes the whole URL, password and all: only the
		// reason is kept.
		if ue, ok := errors.AsType[*url.Error](err); ok {
			err = ue.Err
		}
		return nil, errs.Errorf("%w: jwtverify: RemoteKeys: %w", limes.ErrInvalidConfig, err)
	}
	if u.Scheme != schemeHTTPS || u.Host == "" {
		return nil, fmt.Errorf("%w: jwtverify: RemoteKeys: url must be an %s URL with a host, got %q", limes.ErrInvalidConfig, schemeHTTPS, u.Redacted())
	}
	return u, nil
}

// sameOrigin is a shallow copy of c, so the caller's client is left as it
// is, whose redirects keep to the scheme and host of the first request: a
// redirect is the server's choice, and one to http or to another host would
// let it, or whoever can steer it, pick the keys. Within that, c's own policy
// decides, or net/http's default when it has none. A redirect refused leaves
// the keys unavailable, the server's failure, not the token's.
func sameOrigin(c *http.Client) *http.Client {
	own := c.CheckRedirect
	out := *c
	out.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		first := via[0].URL
		if req.URL.Scheme != schemeHTTPS || !strings.EqualFold(req.URL.Host, first.Host) {
			return fmt.Errorf("%w: jwtverify: redirect from %s to %s leaves the origin", jwks.ErrUnavailable, first.Redacted(), req.URL.Redacted())
		}
		if own != nil {
			return own(req, via)
		}
		if len(via) >= maxRedirects {
			return fmt.Errorf("%w: jwtverify: stopped after %d redirects", jwks.ErrUnavailable, maxRedirects)
		}
		return nil
	}
	return &out
}
