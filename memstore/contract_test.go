package memstore

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/metertest"
)

// contractTTL is how long the capabilities the contract checks record live.
const contractTTL = time.Hour

func TestMeterContract(t *testing.T) {
	metertest.Run(t, func(t *testing.T) metertest.Env[struct{}] {
		t.Helper()
		records := New[struct{}]()
		tenant := uuid.New()
		var offset atomic.Int64 // how far Advance has moved the clock
		now := func() time.Time { return time.Now().Add(time.Duration(offset.Load())) }
		return metertest.Env[struct{}]{
			Ctx:     context.Background(),
			Usage:   NewUsage(records).WithClock(now),
			Tenant:  tenant,
			Advance: func(d time.Duration) { offset.Add(int64(d)) },
			NewCapability: func(parent uuid.UUID, maxBudget limes.Nanos) (uuid.UUID, error) {
				c := limes.Capability{
					ID: uuid.New(), ParentID: parent, ExpiresAt: time.Now().Add(contractTTL),
					Subject: limes.Principal{TenantID: tenant, Subject: "agent"},
					Caveats: limes.Caveats{Ops: []limes.Op{limes.OpGet}, MaxBudgetAmount: maxBudget},
				}
				return c.ID, records.Insert(context.Background(), c, limes.Principal{Subject: "issuer"})
			},
		}
	})
}
