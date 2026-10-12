// Package jwkstest checks a limes.KeyResolver against the module's contract,
// the way storetest checks a limes.Store. Run it from the resolver's own
// tests:
//
//	func TestKeyResolverContract(t *testing.T) {
//		jwkstest.Run(t, func(t *testing.T) jwkstest.Env { … })
//	}
//
// The checks cover what the verifier relies on: a kid the resolver knows
// gives its key, one it does not is jwks.ErrUnknownKID — the token's fault,
// not the resolver's — and concurrent lookups agree.
package jwkstest

import (
	"context"
	"crypto/ed25519"
	"errors"
	"sync"
	"testing"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/jwks"
)

// Env is one resolver under test, fresh for each check, that knows Key under
// KID and no other kid the checks ask for.
type Env struct {
	// Ctx is the context every call runs under.
	Ctx context.Context
	// Resolver is the resolver under test.
	Resolver limes.KeyResolver
	// KID is a kid the resolver knows.
	KID string
	// Key is the public key the resolver holds under KID.
	Key ed25519.PublicKey
}

const (
	// unknownKID is a kid no resolver under test knows.
	unknownKID = "jwkstest-unknown-kid"
	// concurrentLookups is how many lookups of one kid run at once.
	concurrentLookups = 20
)

// Run runs every check as a subtest, each on a fresh Env from setup.
func Run(t *testing.T, setup func(t *testing.T) Env) {
	t.Helper()
	checks := []struct {
		name string
		run  func(*testing.T, Env)
	}{
		{"AKnownKIDGivesItsKey", checkKnown},
		{"AnUnknownKIDIsErrUnknownKID", checkUnknown},
		{"ConcurrentLookupsAgree", checkConcurrent},
	}
	for _, c := range checks {
		t.Run(c.name, func(t *testing.T) { c.run(t, setup(t)) })
	}
}

func checkKnown(t *testing.T, e Env) {
	got, err := e.Resolver.PublicKey(e.Ctx, e.KID)
	if err != nil {
		t.Fatalf("PublicKey(%q): %v", e.KID, err)
	}
	if !got.Equal(e.Key) {
		t.Errorf("PublicKey(%q) gave another key", e.KID)
	}
}

func checkUnknown(t *testing.T, e Env) {
	for _, kid := range []string{unknownKID, ""} {
		key, err := e.Resolver.PublicKey(e.Ctx, kid)
		if !errors.Is(err, jwks.ErrUnknownKID) {
			t.Errorf("PublicKey(%q): err = %v, want jwks.ErrUnknownKID", kid, err)
		}
		if key != nil {
			t.Errorf("PublicKey(%q) gave a key with its error", kid)
		}
	}
}

func checkConcurrent(t *testing.T, e Env) {
	var wg sync.WaitGroup
	for range concurrentLookups {
		wg.Go(func() {
			got, err := e.Resolver.PublicKey(e.Ctx, e.KID)
			if err != nil || !got.Equal(e.Key) {
				t.Errorf("concurrent PublicKey(%q) = %v, want the known key", e.KID, err)
			}
		})
	}
	wg.Wait()
}
