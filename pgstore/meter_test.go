package pgstore

import (
	"cmp"
	"context"
	"errors"
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

// contractCopyQuota is the quota of copies per capability the contract's
// usage store is built with: above every check's own copies.
const contractCopyQuota = 16

// meterEnv is the contract's Env over stores of cfg's scope, readOnly giving the
// context that scope reads as reading every tenant.
func meterEnv(t *testing.T, cfg Config, readOnly func(context.Context) context.Context) metertest.Env[pgx.Tx] {
	t.Helper()
	fresh(t)
	clock := newFakeClock()
	cfg.DB, cfg.Now, cfg.MaxCopiesPerCapability = app, clock.Now, contractCopyQuota
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
		MaxCopiesPerCapability: contractCopyQuota,
		Ctx:                    WithTenant(t.Context(), tenant),
		ScopesTenants:          true,
		Usage:                  usage,
		Ledger:                 ledger,
		Now:                    clock.Now,
		Tenant:                 tenant,
		NewTenant:              func() (uuid.UUID, error) { return uuid.New(), nil },
		TenantCtx:              func(tenant uuid.UUID) context.Context { return WithTenant(t.Context(), tenant) },
		ListCtx:                WithAllTenantsReadOnly(t.Context()),
		ReadOnlyCtx:            readOnly(t.Context()),
		Advance:                clock.Advance,
		DeleteCapability: func(id uuid.UUID) error {
			_, err := owner.Exec(context.Background(), deleteRecord, id)
			return err
		},
		NewCapability: func(spec metertest.CapabilitySpec) (uuid.UUID, error) {
			c := limes.Capability{
				ID: uuid.New(), ParentID: spec.Parent, ExpiresAt: expiry,
				Subject: limes.Principal{Type: limes.PrincipalTypeAgent, TenantID: cmp.Or(spec.Tenant, tenant), Subject: "agent"},
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

// A counter row of the same id under another tenant — which pgstore never
// writes, ids being random or signed — is invisible to the call and left in
// place by its ON CONFLICT DO NOTHING. The call is refused before any ceiling
// is read as zero or onCharged runs, never a nil dereference.
func TestMeterRefusesACounterRowOfAnotherTenant(t *testing.T) {
	f := newTenantFixture(t)
	ctx := WithTenant(t.Context(), f.mine)
	if _, err := owner.Exec(t.Context(), `INSERT INTO `+schema+`.usage (capability_id, tenant_id) VALUES ($1, $2)`, f.child.ID, f.theirs); err != nil {
		t.Fatal(err)
	}
	subject := meterSubject(f.child.ID, f.mine)
	if _, err := f.usage.Bump(ctx, limes.BumpRequest{Subject: subject}); !errors.Is(err, errCounterHidden) {
		t.Errorf("Bump over another tenant's row: err = %v, want errCounterHidden", err)
	}
	called := false
	onCharged := func(context.Context, limes.Charged[pgx.Tx]) error { called = true; return nil }
	if _, err := f.usage.Charge(ctx, limes.ChargeRequest{Subject: subject, Amount: 1}, onCharged); !errors.Is(err, errCounterHidden) {
		t.Errorf("Charge over another tenant's row: err = %v, want errCounterHidden", err)
	}
	if called {
		t.Error("onCharged ran for a charge refused over another tenant's row")
	}
}

// A negative quota of copies is refused rather than read as none.
func TestNewUsageStoreRefusesANegativeCopyQuota(t *testing.T) {
	if _, err := NewUsageStore(Config{DB: app, MaxCopiesPerCapability: -1}); err == nil {
		t.Fatal("a negative MaxCopiesPerCapability was accepted")
	}
}
