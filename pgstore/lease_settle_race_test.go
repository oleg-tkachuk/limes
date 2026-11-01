package pgstore

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/meter"
)

// beforeTxDB is a DB that runs hook once, before the transaction numbered
// at, counting from one: a store over it can be made to meet another's
// call between two transactions of its own.
type beforeTxDB struct {
	DB
	at    int64
	count atomic.Int64
	hook  func()
	// every runs hook before each transaction instead, at none.
	every bool
}

func (d *beforeTxDB) BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error) {
	if n := d.count.Add(1); d.every || n == d.at {
		d.hook()
	}
	return d.DB.BeginTx(ctx, opts)
}

// The transactions of a SetTenantBudget that resets, and of a DeleteUsage,
// when no lease is open: the read of the leases to settle first, then the
// call's own.
const (
	txLeasesRead = 1
	txOwnCall    = 2
)

// settleRaceCeiling is the tenant's ceiling of the race tests, and what they
// lease and charge.
const settleRaceCeiling = limes.Nanos(100)

// settleRaceFixture is a root and two children of a tenant whose budget is
// settleRaceCeiling, a store whose own transactions hook can interleave with,
// and a plain one.
type settleRaceFixture struct {
	db            *beforeTxDB
	logs          bytes.Buffer
	hooked, other *UsageStore
	ctx           context.Context
	tenant, root  uuid.UUID
	a, b          limes.MeterSubject
	version       int64
}

func newSettleRaceFixture(t *testing.T) *settleRaceFixture {
	t.Helper()
	fresh(t)
	f := &settleRaceFixture{db: &beforeTxDB{DB: app}, tenant: newTenant(t)}
	records, err := NewStore(Config{DB: app, CheckTenant: checkTenant})
	if err != nil {
		t.Fatal(err)
	}
	if f.hooked, err = NewUsageStore(Config{DB: f.db, Logger: slog.New(slog.NewTextHandler(&f.logs, nil))}); err != nil {
		t.Fatal(err)
	}
	if f.other, err = NewUsageStore(Config{DB: app}); err != nil {
		t.Fatal(err)
	}
	f.ctx = WithTenant(t.Context(), f.tenant)
	expiry := time.Now().Add(time.Hour).Truncate(time.Second)
	record := func(parent uuid.UUID) uuid.UUID {
		c := limes.Capability{
			ID: uuid.New(), ParentID: parent, ExpiresAt: expiry,
			Subject: limes.Principal{Type: limes.PrincipalTypeAgent, TenantID: f.tenant, Subject: "agent"},
			Caveats: limes.Caveats{Ops: []limes.Op{limes.OpGet}, Requests: limes.RequestCeiling{Unlimited: true}, Budget: limes.BudgetCeiling{Unlimited: true, UnitCode: testUnit}},
		}
		if err := records.Insert(f.ctx, limes.InsertRequest{Capability: c}); err != nil {
			t.Fatal(err)
		}
		return c.ID
	}
	f.root = record(uuid.Nil())
	f.a, f.b = meterSubject(record(f.root), f.tenant), meterSubject(record(f.root), f.tenant)
	b, err := f.other.SetTenantBudget(f.ctx, meter.SetTenantBudgetRequest{TenantID: f.tenant, Ceiling: limes.BudgetCeiling{Max: settleRaceCeiling, UnitCode: testUnit}})
	if err != nil {
		t.Fatal(err)
	}
	f.version = b.ResourceVersion
	return f
}

// leaseBefore has the other store lease a settleRaceCeiling just before the
// hooked store's transaction numbered at.
func (f *settleRaceFixture) leaseBefore(t *testing.T, at int64) {
	t.Helper()
	f.db.at = at
	f.db.hook = func() {
		if _, err := f.other.Lease(f.ctx, meter.LeaseRequest{Subject: f.a, Amount: settleRaceCeiling}); err != nil {
			t.Errorf("Lease in the window: %v", err)
		}
	}
}

// A lease opened while a reset settles the tenant's leases does not stay
// open into the period the reset opens: it is settled into the one it
// closes, so the new period's charges are held to the ceiling.
func TestResetSettlesALeaseOpenedWhileItSettles(t *testing.T) {
	f := newSettleRaceFixture(t)
	f.leaseBefore(t, txOwnCall)
	if _, err := f.hooked.SetTenantBudget(f.ctx, meter.SetTenantBudgetRequest{
		TenantID: f.tenant, Ceiling: limes.BudgetCeiling{Max: settleRaceCeiling, UnitCode: testUnit}, ResetSpend: true, ExpectedVersion: f.version,
	}); err != nil {
		t.Fatalf("SetTenantBudget: %v", err)
	}
	if _, err := f.other.GetLease(f.ctx, f.a.CapabilityID()); !errors.Is(err, meter.ErrLeaseNotFound) {
		t.Fatalf("the lease opened during the reset is open past it: %v", err)
	}
	if _, err := f.other.Charge(f.ctx, meter.ChargeRequest{Subject: f.a, Amount: settleRaceCeiling}, nil); err != nil {
		t.Fatalf("the first charge of the new period: %v", err)
	}
	if _, err := f.other.Charge(f.ctx, meter.ChargeRequest{Subject: f.b, Amount: settleRaceCeiling}, nil); !errors.Is(err, limes.ErrTenantBudgetExceeded) {
		t.Errorf("a charge past the new period's ceiling: %v, want ErrTenantBudgetExceeded", err)
	}
}

// A lease opened while DeleteUsage settles the capability's lease is not
// dropped with its counters: it is settled first, so no reservation is
// left that nothing could release.
func TestDeleteUsageSettlesALeaseOpenedWhileItSettles(t *testing.T) {
	f := newSettleRaceFixture(t)
	f.leaseBefore(t, txOwnCall)
	if err := f.hooked.DeleteUsage(f.ctx, meter.DeleteUsageRequest{CapabilityID: f.a.CapabilityID()}); err != nil {
		t.Fatalf("DeleteUsage: %v", err)
	}
	root, err := f.other.GetUsage(f.ctx, f.root)
	if err != nil || root.ReservedAmount != 0 {
		t.Errorf("the root after DeleteUsage = %+v, %v; want nothing held", root, err)
	}
	tenant, err := f.other.GetTenantBudget(f.ctx, f.tenant)
	if err != nil || tenant.ReservedAmount != 0 {
		t.Errorf("the tenant after DeleteUsage = %+v, %v; want nothing held", tenant, err)
	}
}

// A lease opened again before each of a reset's own transactions makes it
// settle again, a bounded number of times, each logged; then the reset is
// refused as a conflict, having reset nothing.
func TestResetGivesUpOnLeasesThatKeepOpening(t *testing.T) {
	f := newSettleRaceFixture(t)
	f.db.every = true
	f.db.hook = func() {
		if _, err := f.other.Lease(f.ctx, meter.LeaseRequest{Subject: f.a, Amount: 1}); err != nil {
			t.Errorf("Lease before a transaction: %v", err)
		}
	}
	_, err := f.hooked.SetTenantBudget(f.ctx, meter.SetTenantBudgetRequest{
		TenantID: f.tenant, Ceiling: limes.BudgetCeiling{Max: settleRaceCeiling, UnitCode: testUnit}, ResetSpend: true, ExpectedVersion: f.version,
	})
	if !errors.Is(err, limes.ErrConflict) {
		t.Fatalf("SetTenantBudget under leases that keep opening: %v, want ErrConflict", err)
	}
	logs := f.logs.String()
	if strings.Count(logs, "level=WARN") != settleAttempts-1 || !strings.Contains(logs, "level=ERROR") || !strings.Contains(logs, settledReset) {
		t.Errorf("logs = %q, want a warning for each attempt settled again and an error naming the call", logs)
	}
	f.db.every = false
	b, err := f.other.GetTenantBudget(f.ctx, f.tenant)
	if err != nil || b.ResourceVersion != f.version {
		t.Errorf("the tenant after the refused reset = %+v, %v; want version %d kept", b, err, f.version)
	}
}
