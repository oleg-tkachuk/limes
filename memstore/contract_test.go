package memstore

import (
	"context"
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
		return metertest.Env[struct{}]{
			Ctx:    context.Background(),
			Usage:  NewUsage(records),
			Tenant: tenant,
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
