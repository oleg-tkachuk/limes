package memstore

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes/storetest"
)

func TestStoreContract(t *testing.T) {
	storetest.Run(t, func(t *testing.T) storetest.Env {
		t.Helper()
		// The store's clock stands still, as storetest.Env.Now asks.
		now := time.Now().UTC()
		clock := func() time.Time { return now }
		store, err := NewStore(StoreConfig{Now: clock})
		if err != nil {
			t.Fatal(err)
		}
		return storetest.Env{Ctx: context.Background(), Store: store, Tenant: uuid.New(), Now: clock}
	})
}
