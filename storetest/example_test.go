package storetest_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes/memstore"
	"github.com/oleg-tkachuk/limes/storetest"
)

// A store's own test file runs the suite against a fresh store per check;
// memstore stands in here for the store under test. Run takes the test's
// *testing.T, so the example shows the test function rather than running it.
func ExampleRun() {
	_ = func(t *testing.T) {
		storetest.Run(t, func(t *testing.T) storetest.Env {
			t.Helper()
			// The clock stands still but for Advance, as Env.Now asks.
			start := time.Now().UTC()
			var offset time.Duration
			clock := func() time.Time { return start.Add(offset) }
			store, err := memstore.NewStore(memstore.StoreConfig{Now: clock})
			if err != nil {
				t.Fatal(err)
			}
			return storetest.Env{
				Ctx: context.Background(), Store: store, Tenant: uuid.New(),
				Census: memstore.NewCensus(store), Lister: memstore.NewCensus(store),
				Now: clock, Advance: func(d time.Duration) { offset += d },
			}
		})
	}
}
