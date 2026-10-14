package pgstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/internal/link"
)

// rlsTTL is how long the capabilities of these tests live.
const rlsTTL = time.Hour

// tenantFixture is two stores over the app role and a capability of tenant
// mine, revoked, with a child, a charge and a hold.
type tenantFixture struct {
	records       *Store
	usage         *UsageStore
	clock         *fakeClock
	mine, theirs  uuid.UUID
	parent, child limes.Capability
}

func newTenantFixture(t *testing.T) tenantFixture {
	t.Helper()
	fresh(t)
	f := tenantFixture{clock: newFakeClock(), mine: uuid.New(), theirs: uuid.New()}
	var err error
	if f.records, err = NewStore(Config{DB: app, Now: f.clock.Now}); err != nil {
		t.Fatal(err)
	}
	if f.usage, err = NewUsageStore(Config{DB: app, Now: f.clock.Now}); err != nil {
		t.Fatal(err)
	}
	ctx := WithTenant(t.Context(), f.mine)
	expiry := f.clock.Now().Add(rlsTTL)
	f.parent = limes.Capability{
		ID: uuid.New(), ExpiresAt: expiry,
		Subject: limes.Principal{Type: limes.PrincipalAgent, TenantID: f.mine, Subject: "agent:parent"},
		Caveats: limes.Caveats{Ops: []limes.Op{limes.OpGet}, Requests: limes.RequestCeiling{Unlimited: true}, Budget: limes.BudgetCeiling{Unlimited: true}},
	}
	f.child = f.parent
	f.child.ID, f.child.ParentID, f.child.Subject.Subject = uuid.New(), f.parent.ID, "agent:child"
	for _, c := range []limes.Capability{f.parent, f.child} {
		if err := f.records.Insert(ctx, limes.InsertRequest{Capability: c}); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.records.Revoke(ctx, limes.RevokeRequest{CapabilityID: f.parent.ID}); err != nil {
		t.Fatal(err)
	}
	return f
}

// A role that does not own the tables sees and writes its own tenant's rows
// only: another tenant's records are not there for it, and neither is a
// write in their name. A call whose request names the tenant is refused as
// scoped to another one.
func TestRowSecurityKeepsTenantsApart(t *testing.T) {
	f := newTenantFixture(t)
	for name, ctx := range map[string]context.Context{
		"another tenant": WithTenant(t.Context(), f.theirs),
		"no scope":       t.Context(),
	} {
		if _, err := f.records.GetRecord(ctx, f.parent.ID); !errors.Is(err, limes.ErrNotFound) {
			t.Errorf("%s: GetRecord: err = %v, want ErrNotFound", name, err)
		}
		if err := f.records.Revoke(ctx, limes.RevokeRequest{CapabilityID: f.child.ID}); !errors.Is(err, limes.ErrNotFound) {
			t.Errorf("%s: Revoke: err = %v, want ErrNotFound", name, err)
		}
		page, err := f.records.ListByPrincipal(ctx, limes.ListByPrincipalRequest{
			TenantID: f.mine, PrincipalType: limes.PrincipalAgent, Subject: "agent:child",
		})
		if !errors.Is(err, limes.ErrScopeMismatch) || len(page.Items) != 0 {
			t.Errorf("%s: ListByPrincipal = %d items, %v; want ErrScopeMismatch", name, len(page.Items), err)
		}
		c := f.parent
		c.ID, c.ParentID = uuid.New(), uuid.Nil
		if err := f.records.Insert(ctx, limes.InsertRequest{Capability: c}); !errors.Is(err, limes.ErrScopeMismatch) {
			t.Errorf("%s: Insert of a capability of another tenant: err = %v, want ErrScopeMismatch", name, err)
		}
	}
}

// The revocation checks see every tenant whatever the call's scope: the
// verifier asks before it knows the token's tenant, and a check that missed
// an entry would let a revoked token through.
func TestRevocationChecksSeeEveryTenant(t *testing.T) {
	f := newTenantFixture(t)
	for name, ctx := range map[string]context.Context{
		"own tenant":     WithTenant(t.Context(), f.mine),
		"another tenant": WithTenant(t.Context(), f.theirs),
		"no scope":       t.Context(),
	} {
		for _, id := range []uuid.UUID{f.parent.ID, f.child.ID} {
			if revoked, err := f.records.IsRevoked(ctx, id); err != nil || !revoked {
				t.Errorf("%s: IsRevoked(%s) = %v, %v; want revoked", name, id, revoked, err)
			}
		}
	}
	copyID := []byte("rls-copy")
	if err := f.records.RevokeBiscuit(WithTenant(t.Context(), f.mine), limes.RevokeBiscuitRequest{
		Copy: biscuitCopy(t, f.child.ID, copyID),
	}); err != nil {
		t.Fatal(err)
	}
	if revoked, err := f.records.IsBiscuitRevoked(t.Context(), [][]byte{copyID}); err != nil || !revoked {
		t.Errorf("IsBiscuitRevoked with no scope = %v, %v; want revoked", revoked, err)
	}
}

// The meter of another tenant cannot count against a capability, nor read
// its counters; a call with no scope reads nothing either.
func TestRowSecurityKeepsTheMeterApart(t *testing.T) {
	f := newTenantFixture(t)
	ctx := WithTenant(t.Context(), f.mine)
	if _, err := f.usage.SetTenantBudget(ctx, limes.SetTenantBudgetRequest{
		TenantID: f.mine, Ceiling: limes.BudgetCeiling{Unlimited: true}, UnitCode: limes.DefaultUnitCode,
	}); err != nil {
		t.Fatal(err)
	}
	for name, other := range map[string]context.Context{
		"another tenant": WithTenant(t.Context(), f.theirs),
		"no scope":       t.Context(),
	} {
		if _, err := f.usage.GetTenantBudget(other, f.mine); !errors.Is(err, limes.ErrScopeMismatch) {
			t.Errorf("%s: GetTenantBudget: err = %v, want ErrScopeMismatch", name, err)
		}
		page, err := f.usage.ListTenantBudgets(other, limes.ListTenantBudgetsRequest{})
		if err != nil || len(page.Items) != 0 {
			t.Errorf("%s: ListTenantBudgets = %d rows, %v; want none", name, len(page.Items), err)
		}
	}
	page, err := f.usage.ListTenantBudgets(WithAllTenants(t.Context()), limes.ListTenantBudgetsRequest{})
	if err != nil || len(page.Items) != 1 {
		t.Errorf("ListTenantBudgets across every tenant = %d rows, %v; want this tenant's", len(page.Items), err)
	}
}

// Housekeeping with no scope removes nothing; scoped to every tenant it
// removes what is due in each.
func TestHousekeepingNeedsEveryTenant(t *testing.T) {
	f := newTenantFixture(t)
	f.clock.Advance(rlsTTL + 2*limes.MinPurgeRetention)
	if got, err := f.records.Purge(t.Context(), limes.PurgeRequest{}); err != nil || got.Purged != 0 {
		t.Errorf("Purge with no scope = %+v, %v; want nothing purged", got, err)
	}
	if got, err := f.records.Purge(WithAllTenants(t.Context()), limes.PurgeRequest{}); err != nil || got.Purged != 2 {
		t.Errorf("Purge across every tenant = %+v, %v; want both records", got, err)
	}
}

// biscuitCopy is the copy of capID under revocation id id, as only a
// verifier makes one.
func biscuitCopy(t *testing.T, capID uuid.UUID, id []byte) limes.BiscuitCopy {
	t.Helper()
	return link.NewBiscuitCopy(capID, id).(limes.BiscuitCopy)
}

// A delegated child in another tenant than its parent is refused as too
// wide under a scope that sees both tenants, where the parent is in view.
func TestInsertRefusesAChildInAnotherTenantAcrossTenants(t *testing.T) {
	f := newTenantFixture(t)
	c := f.child
	c.ID, c.ParentID, c.Subject.TenantID = uuid.New(), f.parent.ID, f.theirs
	if err := f.records.Insert(WithAllTenants(t.Context()), limes.InsertRequest{Capability: c}); !errors.Is(err, limes.ErrDelegationTooWide) {
		t.Errorf("Insert of a child in another tenant across tenants: err = %v, want ErrDelegationTooWide", err)
	}
}

// The owner is not bound by the policies, so a call of its with no scope is
// admitted: the check refuses only a scope row-level security would apply.
func TestTheOwnerNeedsNoScope(t *testing.T) {
	fresh(t)
	store, err := NewStore(Config{DB: owner})
	if err != nil {
		t.Fatal(err)
	}
	c := limes.Capability{
		ID: uuid.New(), ExpiresAt: time.Now().Add(rlsTTL),
		Subject: limes.Principal{Type: limes.PrincipalAgent, TenantID: uuid.New(), Subject: "agent:owner"},
		Caveats: limes.Caveats{Ops: []limes.Op{limes.OpGet}, Requests: limes.RequestCeiling{Unlimited: true}, Budget: limes.BudgetCeiling{Unlimited: true}},
	}
	if err := store.Insert(t.Context(), limes.InsertRequest{Capability: c}); err != nil {
		t.Errorf("Insert as the owner with no scope: %v", err)
	}
}

// A Scope of the consumer's gets no check of its own, since the store cannot
// tell what the consumer's policies admit; CheckSchemaScope checks one that
// sets the schema's settings.
func TestCheckScopeBesideAScopeOfTheConsumers(t *testing.T) {
	f := newTenantFixture(t)
	setTenant := func(tenant uuid.UUID) func(context.Context, pgx.Tx) error {
		return func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, sqlSetLocal, settingTenant, tenant.String())
			return err
		}
	}
	for name, tc := range map[string]struct {
		cfg  Config
		want error
	}{
		"no check":                 {Config{DB: app, Scope: setTenant(f.theirs)}, limes.ErrTenantBudgetNotFound},
		"CheckSchemaScope":         {Config{DB: app, Scope: setTenant(f.theirs), CheckScope: CheckSchemaScope}, limes.ErrScopeMismatch},
		"CheckSchemaScope, admits": {Config{DB: app, Scope: setTenant(f.mine), CheckScope: CheckSchemaScope}, limes.ErrTenantBudgetNotFound},
	} {
		usage, err := NewUsageStore(tc.cfg)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := usage.GetTenantBudget(t.Context(), f.mine); !errors.Is(err, tc.want) {
			t.Errorf("%s: GetTenantBudget: err = %v, want %v", name, err, tc.want)
		}
	}
}

// A read-only scope reads every tenant's rows and writes none: a call that
// writes is refused before it begins, and the schema's policies refuse the
// writes of a Scope that sets the read-only setting itself.
func TestReadOnlyScopeReadsEveryTenantAndWritesNone(t *testing.T) {
	f := newTenantFixture(t)
	mine := WithTenant(t.Context(), f.mine)
	if _, err := f.usage.SetTenantBudget(mine, limes.SetTenantBudgetRequest{
		TenantID: f.mine, Ceiling: limes.BudgetCeiling{Unlimited: true}, UnitCode: limes.DefaultUnitCode,
	}); err != nil {
		t.Fatal(err)
	}
	ro := WithAllTenantsReadOnly(t.Context())
	if _, err := f.records.GetRecord(ro, f.child.ID); err != nil {
		t.Errorf("GetRecord under a read-only scope: %v", err)
	}
	if _, err := f.usage.GetTenantBudget(ro, f.mine); err != nil {
		t.Errorf("GetTenantBudget under a read-only scope: %v", err)
	}
	if page, err := f.usage.ListTenantBudgets(ro, limes.ListTenantBudgetsRequest{}); err != nil || len(page.Items) != 1 {
		t.Errorf("ListTenantBudgets under a read-only scope = %d rows, %v; want this tenant's", len(page.Items), err)
	}
	c := f.parent
	c.ID, c.ParentID = uuid.New(), uuid.Nil
	f.clock.Advance(rlsTTL + 2*limes.MinPurgeRetention)
	for name, write := range map[string]func() error{
		"Insert": func() error { return f.records.Insert(ro, limes.InsertRequest{Capability: c}) },
		"Revoke": func() error { return f.records.Revoke(ro, limes.RevokeRequest{CapabilityID: f.child.ID}) },
		"Purge": func() error {
			_, err := f.records.Purge(ro, limes.PurgeRequest{})
			return err
		},
		"SetTenantBudget": func() error {
			_, err := f.usage.SetTenantBudget(ro, limes.SetTenantBudgetRequest{
				TenantID: f.mine, Ceiling: limes.BudgetCeiling{Max: 1}, UnitCode: limes.DefaultUnitCode,
			})
			return err
		},
		"Charge": func() error {
			_, err := f.usage.Charge(ro, limes.ChargeRequest{Subject: meterSubject(f.child.ID, f.mine), Amount: 1}, nil)
			return err
		},
		"ReleaseExpired": func() error {
			_, err := f.usage.ReleaseExpired(ro, limes.ReleaseExpiredRequest{})
			return err
		},
		"PurgeOrphans": func() error {
			_, err := f.usage.PurgeOrphans(ro, limes.PurgeOrphansRequest{})
			return err
		},
		"EraseTenant": func() error {
			_, err := f.records.EraseTenant(ro, limes.EraseTenantRequest{TenantID: f.mine}, nil)
			return err
		},
	} {
		if err := write(); !errors.Is(err, limes.ErrScopeMismatch) {
			t.Errorf("%s under a read-only scope: err = %v, want ErrScopeMismatch", name, err)
		}
	}
	// The policies alone, with no check before them.
	readEvery := func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, sqlSetLocal, settingAllTenants, settingRead)
		return err
	}
	records, err := NewStore(Config{DB: app, Now: f.clock.Now, Scope: readEvery})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := records.GetRecord(t.Context(), f.child.ID); err != nil {
		t.Errorf("GetRecord under the read-only setting: %v", err)
	}
	if got, err := records.Purge(t.Context(), limes.PurgeRequest{}); err != nil || got.Purged != 0 {
		t.Errorf("Purge under the read-only setting = %+v, %v; want nothing purged", got, err)
	}
	if err := records.Insert(t.Context(), limes.InsertRequest{Capability: c}); err == nil {
		t.Error("Insert under the read-only setting succeeded")
	}
	if _, err := f.records.GetRecord(mine, f.child.ID); err != nil {
		t.Errorf("the record after the refused writes: %v", err)
	}
}

// scopedTables is every table the schema's policies bind.
var scopedTables = []string{
	"capabilities", "revocations", "biscuit_revocations", "usage",
	"copy_usage", "tenant_budgets", "charges", "reservations",
}

// Under the read-only setting each table's policies let its rows be read
// and none be changed or removed, statement by statement.
func TestReadOnlyPoliciesOfEveryTable(t *testing.T) {
	f := newTenantFixture(t)
	if _, err := f.usage.Charge(WithTenant(t.Context(), f.mine), limes.ChargeRequest{Subject: meterSubject(f.child.ID, f.mine), Amount: 1}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := f.usage.Reserve(WithTenant(t.Context(), f.mine), limes.ReserveRequest{Subject: meterSubject(f.child.ID, f.mine), Amount: 1}); err != nil {
		t.Fatal(err)
	}
	err := pgx.BeginFunc(t.Context(), app, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), sqlSetLocal, settingAllTenants, settingRead); err != nil {
			return err
		}
		for _, table := range scopedTables {
			name := pgx.Identifier{schema, table}.Sanitize()
			var rows int64
			if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM `+name).Scan(&rows); err != nil {
				return err
			}
			if rows == 0 && table != "biscuit_revocations" && table != "copy_usage" {
				t.Errorf("%s: no row read under the read-only setting", table)
			}
			for _, write := range []string{`UPDATE ` + name + ` SET tenant_id = tenant_id`, `DELETE FROM ` + name} {
				tag, err := tx.Exec(t.Context(), write)
				if err != nil {
					return err
				}
				if tag.RowsAffected() != 0 {
					t.Errorf("%s under the read-only setting changed %d rows", write, tag.RowsAffected())
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
