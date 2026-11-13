package jwtverify

import (
	"testing"

	"github.com/oleg-tkachuk/limes/jwks"
)

// With no client of its own, RemoteKeys fetches as jwks.Remote does: within
// jwks.DefaultFetchTimeout, over the default transport, which honours the
// deployment's proxy settings.
func TestRemoteKeysDefaultClientIsJWKSRemotes(t *testing.T) {
	c := defaultKeysClient()
	if c.Timeout != jwks.DefaultFetchTimeout || c.Transport != nil || c.CheckRedirect != nil {
		t.Fatalf("default client = timeout %v, transport %v, redirect policy set %v; want %v over the default transport", c.Timeout, c.Transport, c.CheckRedirect != nil, jwks.DefaultFetchTimeout)
	}
}
