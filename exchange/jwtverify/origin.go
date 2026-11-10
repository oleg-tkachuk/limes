package jwtverify

import (
	"net/url"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/internal/errs"
	"github.com/oleg-tkachuk/limes/internal/httpsurl"
)

// parseKeysURL reads the identity provider's JWKS URL by jwks.Remote's rule
// (httpsurl.Parse): an https URL with a host. Over anything else whoever
// sits on the path chooses the keys the exchange trusts, and so the subject
// tokens it mints capabilities for. Its error never quotes rawURL's
// password: an error is what reaches a log.
func parseKeysURL(rawURL string) (*url.URL, error) {
	u, err := httpsurl.Parse(rawURL)
	if err != nil {
		return nil, errs.Errorf("%w: jwtverify: RemoteKeys: %w", limes.ErrInvalidConfig, err)
	}
	return u, nil
}
