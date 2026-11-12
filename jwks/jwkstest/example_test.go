package jwkstest_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	"github.com/oleg-tkachuk/limes/jwks"
	"github.com/oleg-tkachuk/limes/jwks/jwkstest"
)

// exampleKID is the kid the resolver under test knows.
const exampleKID = "key-1"

// A key resolver's own test file runs the suite against a fresh resolver per
// check; jwks.Static stands in here for the resolver under test. Run takes
// the test's *testing.T, so the example shows the test function rather than
// running it.
func ExampleRun() {
	_ = func(t *testing.T) {
		jwkstest.Run(t, func(t *testing.T) jwkstest.Env {
			t.Helper()
			pub, _, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			resolver, err := jwks.NewStatic(jwks.StaticConfig{Keys: map[string]ed25519.PublicKey{exampleKID: pub}})
			if err != nil {
				t.Fatal(err)
			}
			return jwkstest.Env{Ctx: context.Background(), Resolver: resolver, KID: exampleKID, Key: pub}
		})
	}
}
