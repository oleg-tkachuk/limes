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
		return meterEnv(t, Config{}, WithAllTenantsReadOnly)
	})
}

// The contract holds under a consumer's Scope that sets the schema's own
// settings — its read-only marker among them — and CheckSchemaScope.
func TestMeterContractUnderAConsumersScope(t *testing.T) {
	metertest.Run(t, func(t *testing.T) metertest.Env[pgx.Tx] {
		t.Helper()
		return meterEnv(t, Config{Scope: consumerScope, CheckScope: CheckSchemaScope}, consumerReadOnly)
	})
}

// meterEnv is the contract's Env over stores of cfg's scope, readOnly giving the
// context that scope reads as reading every tenant.
func meterEnv(t *testing.T, cfg Config, readOnly func(context.Context) context.Context) metertest.Env[pgx.Tx] {
	t.Helper()
	fresh(t)
	clock := newFakeClock()
	cfg.DB, cfg.Now = app, clock.Now
	records, err := NewStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	usage, err := NewUsageStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	tenant := uuid.New()
	// One expiry for every capability, so a child never outlives its
	// parent by the time between their inserts.
	expiry := clock.Now().Add(contractTTL)
	ledger, err := NewLedger(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return metertest.Env[pgx.Tx]{
		Ctx:           WithTenant(t.Context(), tenant),
		ScopesTenants: true,
		Usage:         usage,
		Ledger:        ledger,
		Now:           clock.Now,
		Tenant:        tenant,
		NewTenant:     func() (uuid.UUID, error) { return uuid.New(), nil },
		TenantCtx:     func(tenant uuid.UUID) context.Context { return WithTenant(t.Context(), tenant) },
		ListCtx:       WithAllTenantsReadOnly(t.Context()),
		ReadOnlyCtx:   readOnly(t.Context()),
		Advance:       clock.Advance,
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
}

func TestNewUsageStoreRequiresADB(t *testing.T) {
	if _, err := NewUsageStore(Config{}); err == nil {
		t.Fatal("NewUsageStore without a DB: no error")
	}
}
