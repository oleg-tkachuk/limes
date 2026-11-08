package exchange

import (
	"errors"
	"testing"
)

// notAToken is a subject token no verifier accepts: a resource refused
// before the token is verified is refused as the target, not the grant.
const notAToken = "a.b.c"

// badResources are resources RFC 8707 §2 refuses: not absolute, or with a
// fragment, an empty one included.
var badResources = map[string]string{
	"a relative resource": "tools",
	"a resource fragment": toolResource + "#x",
	"an empty fragment":   toolResource + "#",
}

// Exchange, called in process, refuses a resource that is no absolute URI
// without a fragment as the token endpoint does, before verifying the
// subject token.
func TestExchangeRefusesAResourceNotAnAbsoluteURI(t *testing.T) {
	f := newFixture(t, fixtureConfig{})
	for name, resource := range badResources {
		req := request(notAToken)
		req.Resources = []string{toolResource, resource}
		if _, err := f.ex.Exchange(t.Context(), req); !errors.Is(err, ErrInvalidTarget) {
			t.Errorf("%s: %v, want ErrInvalidTarget", name, err)
		}
	}
}

// Narrow, called in process, refuses such a resource as well, before
// verifying the parent.
func TestNarrowRefusesAResourceNotAnAbsoluteURI(t *testing.T) {
	f := newFixture(t, fixtureConfig{narrow: true})
	for name, resource := range badResources {
		req := narrowing(notAToken)
		req.Resources = []string{resource}
		if _, err := f.ex.Narrow(t.Context(), req); !errors.Is(err, ErrInvalidTarget) {
			t.Errorf("%s: %v, want ErrInvalidTarget", name, err)
		}
	}
}
