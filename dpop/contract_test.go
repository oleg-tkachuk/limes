package dpop_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/oleg-tkachuk/limes/dpop"
	"github.com/oleg-tkachuk/limes/dpop/dpoptest"
)

// contractEpoch is where the clock of these tests starts.
var contractEpoch = time.Unix(1_800_000_000, 0)

// contractClock is a clock a test moves by hand.
type contractClock struct{ now time.Time }

func (c *contractClock) Now() time.Time          { return c.now }
func (c *contractClock) Advance(d time.Duration) { c.now = c.now.Add(d) }

func TestMemoryReplayCacheContract(t *testing.T) {
	// entriesPerHolder is small, so the check of a key's share fills it fast.
	const entriesPerHolder = 4
	dpoptest.RunReplayCache(t, func(t *testing.T) dpoptest.ReplayEnv {
		t.Helper()
		clock := &contractClock{now: contractEpoch}
		cache, err := dpop.NewMemoryReplayCache(dpop.MemoryReplayCacheConfig{EntriesPerHolder: entriesPerHolder, Now: clock.Now})
		if err != nil {
			t.Fatal(err)
		}
		return dpoptest.ReplayEnv{
			Ctx: context.Background(), Cache: cache,
			Now: clock.Now, Advance: clock.Advance, EntriesPerHolder: entriesPerHolder,
		}
	})
}

func TestHMACNoncesContract(t *testing.T) {
	dpoptest.RunNonces(t, func(t *testing.T) dpoptest.NoncesEnv {
		t.Helper()
		clock := &contractClock{now: contractEpoch}
		nonces, err := dpop.NewHMACNonces(dpop.HMACNoncesConfig{Keys: [][]byte{bytes.Repeat([]byte{1}, dpop.HMACNonceKeyBytes)}, Now: clock.Now})
		if err != nil {
			t.Fatal(err)
		}
		return dpoptest.NoncesEnv{
			Ctx: context.Background(), Nonces: nonces,
			Lifetime: dpop.DefaultNonceLifetime, Advance: clock.Advance,
		}
	})
}
