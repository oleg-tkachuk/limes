package pgstore

import (
	"context"
	"errors"
	"fmt"
	"maps"
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

// A read-only scope reads every tenant's rows and writes none: whether the
// call's context says so or a Scope of the consumer's sets the read-only
// setting, every call that writes is refused as out of scope, runs none of
// the consumer's code, and changes no row.
func TestReadOnlyScopeReadsEveryTenantAndWritesNone(t *testing.T) {
	f := newTenantFixture(t)
	mine := WithTenant(t.Context(), f.mine)
	if _, err := f.usage.SetTenantBudget(mine, limes.SetTenantBudgetRequest{
		TenantID: f.mine, Ceiling: limes.BudgetCeiling{Unlimited: true}, UnitCode: limes.DefaultUnitCode,
	}); err != nil {
		t.Fatal(err)
	}
	charged, err := f.usage.Charge(mine, limes.ChargeRequest{Subject: meterSubject(f.child.ID, f.mine), Amount: 1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	held, err := f.usage.Reserve(mine, limes.ReserveRequest{Subject: meterSubject(f.child.ID, f.mine), Amount: 1})
	if err != nil {
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
	// Past the hold's expiry and the records' retention, so housekeeping
	// let through would have rows to take.
	f.clock.Advance(rlsTTL + 2*limes.MinPurgeRetention)

	// A consumer's Scope that maps a marker of its own to the read-only
	// setting: the store never sees WithAllTenantsReadOnly.
	readEvery := func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, sqlSetLocal, settingAllTenants, settingRead)
		return err
	}
	records, err := NewStore(Config{DB: app, Now: f.clock.Now, Scope: readEvery})
	if err != nil {
		t.Fatal(err)
	}
	usage, err := NewUsageStore(Config{DB: app, Now: f.clock.Now, Scope: readEvery})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := records.GetRecord(t.Context(), f.child.ID); err != nil {
		t.Errorf("GetRecord under the consumer's read-only Scope: %v", err)
	}

	before := tableDigests(t)
	for scope, s := range map[string]struct {
		ctx     context.Context
		records *Store
		usage   *UsageStore
	}{
		"WithAllTenantsReadOnly":         {ro, f.records, f.usage},
		"the consumer's read-only Scope": {t.Context(), records, usage},
	} {
		ran := false
		consumer := func(context.Context, pgx.Tx) error {
			ran = true
			return nil
		}
		c := f.parent
		c.ID, c.ParentID = uuid.New(), uuid.Nil
		subject := meterSubject(f.child.ID, f.mine)
		for name, write := range map[string]func() error{
			"Insert": func() error { return s.records.Insert(s.ctx, limes.InsertRequest{Capability: c}) },
			"Revoke": func() error { return s.records.Revoke(s.ctx, limes.RevokeRequest{CapabilityID: f.child.ID}) },
			"RevokeBiscuit": func() error {
				return s.records.RevokeBiscuit(s.ctx, limes.RevokeBiscuitRequest{Copy: biscuitCopy(t, f.child.ID, []byte("read-only"))})
			},
			"Purge": func() error {
				_, err := s.records.Purge(s.ctx, limes.PurgeRequest{})
				return err
			},
			"EraseTenantRecords": func() error {
				_, err := s.records.EraseTenantRecords(s.ctx, limes.EraseTenantRequest{TenantID: f.mine})
				return err
			},
			"EraseTenant": func() error {
				_, err := s.records.EraseTenant(s.ctx, limes.EraseTenantRequest{TenantID: f.mine}, consumer)
				return err
			},
			"Bump": func() error {
				_, err := s.usage.Bump(s.ctx, limes.BumpRequest{Subject: subject})
				return err
			},
			"Charge": func() error {
				_, err := s.usage.Charge(s.ctx, limes.ChargeRequest{Subject: subject, Amount: 1}, consumer)
				return err
			},
			"Reserve": func() error {
				_, err := s.usage.Reserve(s.ctx, limes.ReserveRequest{Subject: subject, Amount: 1})
				return err
			},
			"Settle": func() error {
				_, err := s.usage.Settle(s.ctx, limes.SettleRequest{Subject: subject, ReservationID: held.ID, Amount: 1}, consumer)
				return err
			},
			"Release": func() error {
				return s.usage.Release(s.ctx, limes.ReleaseRequest{Subject: subject, ReservationID: held.ID})
			},
			"Refund": func() error {
				_, err := s.usage.Refund(s.ctx, limes.RefundRequest{ChargeID: charged.ChargeID, Remaining: true})
				return err
			},
			"SetTenantBudget": func() error {
				_, err := s.usage.SetTenantBudget(s.ctx, limes.SetTenantBudgetRequest{
					TenantID: f.mine, Ceiling: limes.BudgetCeiling{Max: 1}, UnitCode: limes.DefaultUnitCode,
				})
				return err
			},
			"ReleaseExpired": func() error {
				_, err := s.usage.ReleaseExpired(s.ctx, limes.ReleaseExpiredRequest{})
				return err
			},
			"PurgeOrphans": func() error {
				_, err := s.usage.PurgeOrphans(s.ctx, limes.PurgeOrphansRequest{})
				return err
			},
			"DeleteUsage": func() error { return s.usage.DeleteUsage(s.ctx, limes.DeleteUsageRequest{CapabilityID: f.child.ID}) },
			"EraseTenantUsage": func() error {
				return s.usage.EraseTenantUsage(s.ctx, limes.EraseTenantUsageRequest{TenantID: f.mine})
			},
		} {
			if err := write(); !errors.Is(err, limes.ErrScopeMismatch) {
				t.Errorf("%s under %s: err = %v, want ErrScopeMismatch", name, scope, err)
			}
		}
		if ran {
			t.Errorf("a write under %s ran the consumer's code", scope)
		}
	}
	if after := tableDigests(t); !maps.Equal(after, before) {
		t.Errorf("the tables changed under a read-only scope: %v, want %v", after, before)
	}
}

// sqlTableDigest is a digest of every row of a table, as the owner reads it.
const sqlTableDigest = `SELECT coalesce(md5(string_agg(t::text, ',' ORDER BY t::text)), '') FROM %s t`

// tableDigests is a digest of each table the schema's policies bind, so a
// test can tell that a call changed no row of any.
func tableDigests(t *testing.T) map[string]string {
	t.Helper()
	digests := make(map[string]string, len(scopedTables))
	for _, table := range scopedTables {
		var digest string
		query := fmt.Sprintf(sqlTableDigest, pgx.Identifier{schema, table}.Sanitize())
		if err := owner.QueryRow(t.Context(), query).Scan(&digest); err != nil {
			t.Fatalf("digest of %s: %v", table, err)
		}
		digests[table] = digest
	}
	return digests
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
