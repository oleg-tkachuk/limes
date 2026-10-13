package pgstore

import (
	"cmp"
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/metertest"
)

// contractTTL is how long the capabilities the meter checks record live.
const contractTTL = time.Hour

// deleteRecord removes a capability's record, as the owner. metertest also
// removes an ancestor whose children stay, which Purge never does and the
// parent_id foreign key refuses; the children are unhooked first, so their
// lineage stops where it would at an ancestor not on record.
const deleteRecord = `
WITH unhooked AS (
    UPDATE ` + schema + `.capabilities SET parent_id = NULL WHERE parent_id = $1
)
DELETE FROM ` + schema + `.capabilities WHERE id = $1`

func TestMeterContract(t *testing.T) {
	metertest.Run(t, func(t *testing.T) metertest.Env[pgx.Tx] {
		t.Helper()
		fresh(t)
		clock := newFakeClock()
		records, err := NewStore(Config{DB: app, Now: clock.Now})
		if err != nil {
			t.Fatal(err)
		}
		usage, err := NewUsageStore(Config{DB: app, Now: clock.Now})
		if err != nil {
			t.Fatal(err)
		}
		tenant := uuid.New()
		// One expiry for every capability, so a child never outlives its
		// parent by the time between their inserts.
		expiry := clock.Now().Add(contractTTL)
		return metertest.Env[pgx.Tx]{
			Ctx:       WithTenant(t.Context(), tenant),
			Usage:     usage,
			Tenant:    tenant,
			NewTenant: func() (uuid.UUID, error) { return uuid.New(), nil },
			TenantCtx: func(tenant uuid.UUID) context.Context { return WithTenant(t.Context(), tenant) },
			ListCtx:   WithAllTenants(t.Context()),
			Advance:   clock.Advance,
			DeleteCapability: func(id uuid.UUID) error {
				_, err := owner.Exec(context.Background(), deleteRecord, id)
				return err
			},
			NewCapability: func(spec metertest.CapabilitySpec) (uuid.UUID, error) {
				c := limes.Capability{
					ID: uuid.New(), ParentID: spec.Parent, ExpiresAt: expiry,
					Subject: limes.Principal{Type: limes.PrincipalAgent, TenantID: cmp.Or(spec.Tenant, tenant), Subject: "agent"},
					Caveats: limes.Caveats{Ops: []limes.Op{limes.OpGet}, Requests: spec.Requests, Budget: spec.Budget},
				}
				return c.ID, records.Insert(WithTenant(t.Context(), c.Subject.TenantID), limes.InsertRequest{Capability: c, IssuedBy: limes.Principal{Subject: "issuer"}})
			},
		}
	})
}

func TestNewUsageStoreRequiresADB(t *testing.T) {
	if _, err := NewUsageStore(Config{}); err == nil {
		t.Fatal("NewUsageStore without a DB: no error")
	}
}
