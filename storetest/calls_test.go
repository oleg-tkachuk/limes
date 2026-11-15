package storetest

import (
	"context"
	"testing"
	"time"
	"uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/internal/watchdog"
	"github.com/oleg-tkachuk/limes/memstore"
)

// countingStore counts the calls of limes.Store on a memstore — every
// seeding loop of the suite goes through them. The other methods are the
// embedded store's, so each interface a check asserts on Env.Store answers as
// memstore's does, and no check skips that would run on memstore itself.
type countingStore struct {
	*memstore.Store
	calls *int
}

func (s countingStore) Insert(ctx context.Context, req limes.InsertRequest) error {
	*s.calls++
	return s.Store.Insert(ctx, req)
}

func (s countingStore) IsRevoked(ctx context.Context, id uuid.UUID) (bool, error) {
	*s.calls++
	return s.Store.IsRevoked(ctx, id)
}

func (s countingStore) GetRecord(ctx context.Context, id uuid.UUID) (limes.Record, error) {
	*s.calls++
	return s.Store.GetRecord(ctx, id)
}

func (s countingStore) Revoke(ctx context.Context, req limes.RevokeRequest) (limes.RevokeResult, error) {
	*s.calls++
	return s.Store.Revoke(ctx, req)
}

func (s countingStore) Purge(ctx context.Context, req limes.PurgeRequest) (limes.PurgeResult, error) {
	*s.calls++
	return s.Store.Purge(ctx, req)
}

func (s countingStore) ListByPrincipal(ctx context.Context, req limes.ListByPrincipalRequest) (limes.Page[limes.Capability], error) {
	*s.calls++
	return s.Store.ListByPrincipal(ctx, req)
}

// Every check makes no more store calls than its limit is sized for:
// watchdog.OrdinaryCalls, or the Calls it declares. A check that grows past
// that without declaring it would be read as deadlocked on a loaded host; one
// that declares calls it never makes gets a limit that hides a real hang.
func TestEveryCheckFitsTheCallsItsLimitIsSizedFor(t *testing.T) {
	for _, c := range checks() {
		t.Run(c.Name, func(t *testing.T) {
			calls := 0
			start := time.Now().UTC()
			var offset time.Duration
			clock := func() time.Time { return start.Add(offset) }
			store, err := memstore.NewStore(memstore.StoreConfig{Now: clock})
			if err != nil {
				t.Fatal(err)
			}
			env := Env{
				Ctx: context.Background(), Store: countingStore{Store: store, calls: &calls}, Tenant: uuid.New(),
				Census: memstore.NewCensus(store), Lister: memstore.NewCensus(store), Exchanges: store, BulkRevoker: store,
				Now: clock, Advance: func(d time.Duration) { offset += d },
			}
			c.Run(t, newFixture(t, func(*testing.T) Env { return env }))
			switch {
			case c.Calls == 0 && calls > watchdog.OrdinaryCalls:
				t.Errorf("made %d store calls, past the %d an ordinary check's limit is sized for; declare them in Calls", calls, watchdog.OrdinaryCalls)
			case c.Calls != 0 && c.Calls <= watchdog.OrdinaryCalls:
				t.Errorf("declares %d calls, which an ordinary check's limit already covers; leave Calls zero", c.Calls)
			case c.Calls != 0 && calls > c.Calls:
				t.Errorf("made %d store calls, past the %d it declares", calls, c.Calls)
			case c.Calls != 0 && calls <= watchdog.OrdinaryCalls:
				t.Errorf("declares %d calls but made %d, which an ordinary check's limit covers", c.Calls, calls)
			}
			t.Logf("%d store calls", calls)
		})
	}
}
