package jwks_test

import (
	"errors"
	"testing"
	"time"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/jwks"
)

// remoteURL is a valid key set URL for the cases that break something else.
const remoteURL = "https://issuer.example.com/jwks"

// Every RemoteConfig NewRemote refuses is limes.ErrInvalidConfig, a URL that
// does not parse among them, as exchange/jwtverify refuses one.
func TestNewRemoteRefusalsAreInvalidConfig(t *testing.T) {
	for name, cfg := range map[string]jwks.RemoteConfig{
		"no URL":                     {},
		"a URL that does not parse":  {URL: "https://issuer.example/%zz"},
		"a URL with a control byte":  {URL: "https://issuer.example/\x7f"},
		"a URL that is not https":    {URL: "http://issuer.example/jwks"},
		"a URL with no host":         {URL: "https:///jwks"},
		"a negative interval":        {URL: remoteURL, RefreshInterval: -time.Second},
		"intervals that do not nest": {URL: remoteURL, RefreshInterval: time.Hour, MaxStale: time.Minute},
	} {
		_, err := jwks.NewRemote(cfg)
		if !errors.Is(err, limes.ErrInvalidConfig) {
			t.Errorf("%s: NewRemote = %v, want limes.ErrInvalidConfig", name, err)
		}
	}
}
