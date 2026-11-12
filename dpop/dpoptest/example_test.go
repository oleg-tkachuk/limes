package dpoptest_test

import (
	"context"
	"crypto/rand"
	"testing"
	"time"

	"github.com/oleg-tkachuk/limes/dpop"
	"github.com/oleg-tkachuk/limes/dpop/dpoptest"
)

// exampleEntriesPerHolder is the share of the cache one holder may fill;
// small, so the check of that share fills it fast.
const exampleEntriesPerHolder = 4

// A replay cache's own test file runs the suite against a fresh, empty
// cache per check; dpop's in-memory cache stands in here for the cache under
// test. RunReplayCache takes the test's *testing.T, so the example shows the
// test function rather than running it.
func ExampleRunReplayCache() {
	_ = func(t *testing.T) {
		dpoptest.RunReplayCache(t, func(t *testing.T) dpoptest.ReplayEnv {
			t.Helper()
			now := time.Now()
			clock := func() time.Time { return now }
			cache, err := dpop.NewMemoryReplayCache(dpop.MemoryReplayCacheConfig{
				EntriesPerHolder: exampleEntriesPerHolder, Now: clock,
			})
			if err != nil {
				t.Fatal(err)
			}
			return dpoptest.ReplayEnv{
				Ctx: context.Background(), Cache: cache, EntriesPerHolder: exampleEntriesPerHolder,
				Now: clock, Advance: func(d time.Duration) { now = now.Add(d) },
			}
		})
	}
}

// A nonce source's own test file runs the suite the same way.
func ExampleRunNonces() {
	_ = func(t *testing.T) {
		dpoptest.RunNonces(t, func(t *testing.T) dpoptest.NoncesEnv {
			t.Helper()
			now := time.Now()
			key := make([]byte, dpop.HMACNonceKeyBytes)
			if _, err := rand.Read(key); err != nil {
				t.Fatal(err)
			}
			nonces, err := dpop.NewHMACNonces(dpop.HMACNoncesConfig{
				Keys: dpop.SealNonceKeys(key), Now: func() time.Time { return now },
			})
			if err != nil {
				t.Fatal(err)
			}
			return dpoptest.NoncesEnv{
				Ctx: context.Background(), Nonces: nonces,
				Lifetime: dpop.DefaultNonceLifetime, Advance: func(d time.Duration) { now = now.Add(d) },
			}
		})
	}
}
