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
	// fired counts the times hook ran, so a check knows it met the call.
	fired atomic.Int64
}

func (d *beforeTxDB) BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error) {
	if n := d.count.Add(1); d.every || n == d.at {
		d.fired.Add(1)
		d.hook()
	}
	return d.DB.BeginTx(ctx, opts)
}

// txReset is the one transaction of a SetTenantBudget that resets: it
// settles the tenant's leases itself.
const txReset = 1

// The transactions of a DeleteUsage: its own, which settles the leases from
// the capability down, and whatever it runs once its counters are gone.
const (
	txDeleteUsage = 1
	txAfterDrop   = 2
)

// mustHaveMet fails unless the hook ran: a check whose lease never opened
// proves nothing.
func (f *settleRaceFixture) mustHaveMet(t *testing.T) {
	t.Helper()
	if f.db.fired.Load() == 0 {
		t.Fatal("the hook never ran: the call ran fewer transactions than the check assumes")
	}
}

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
	if f.hooked, err = NewUsageStore(Config{DB: f.db, Leases: true, Logger: slog.New(slog.NewTextHandler(&f.logs, nil))}); err != nil {
		t.Fatal(err)
	}
	if f.other, err = NewUsageStore(Config{DB: app, Leases: true}); err != nil {
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

// A lease opened just before a reset does not stay open into the period
// the reset opens: it is settled into the one it closes, so the new
// period's charges are held to the ceiling.
func TestResetSettlesALeaseOpenedBeforeIt(t *testing.T) {
	f := newSettleRaceFixture(t)
	f.leaseBefore(t, txReset)
	if _, err := f.hooked.SetTenantBudget(f.ctx, meter.SetTenantBudgetRequest{
		TenantID: f.tenant, Ceiling: limes.BudgetCeiling{Max: settleRaceCeiling, UnitCode: testUnit}, ResetSpend: true, ExpectedVersion: f.version,
	}); err != nil {
		t.Fatalf("SetTenantBudget: %v", err)
	}
	f.mustHaveMet(t)
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

// A lease opened just before DeleteUsage is not dropped with its counters:
// it is settled first, so no reservation is left that nothing could release.
func TestDeleteUsageSettlesALeaseOpenedBeforeIt(t *testing.T) {
	f := newSettleRaceFixture(t)
	f.leaseBefore(t, txDeleteUsage)
	if err := f.hooked.DeleteUsage(f.ctx, meter.DeleteUsageRequest{CapabilityID: f.a.CapabilityID()}); err != nil {
		t.Fatalf("DeleteUsage: %v", err)
	}
	f.mustHaveMet(t)
	root, err := f.other.GetUsage(f.ctx, f.root)
	if err != nil || root.ReservedAmount != 0 {
		t.Errorf("the root after DeleteUsage = %+v, %v; want nothing held", root, err)
	}
	tenant, err := f.other.GetTenantBudget(f.ctx, f.tenant)
	if err != nil || tenant.ReservedAmount != 0 {
		t.Errorf("the tenant after DeleteUsage = %+v, %v; want nothing held", tenant, err)
	}
}

// A lease opened before every transaction of a reset does not keep it from
// the period's close, even at a tenant spent to its ceiling, where a lease of
// requests alone still opens: the reset settles the tenant's leases in its
// own transaction, which every lease opening meanwhile waits for.
func TestResetSettlesLeasesThatKeepOpening(t *testing.T) {
	f := newSettleRaceFixture(t)
	if _, err := f.other.Charge(f.ctx, meter.ChargeRequest{Subject: f.b, Amount: settleRaceCeiling}, nil); err != nil {
		t.Fatalf("Charge to the ceiling: %v", err)
	}
	f.db.every = true
	f.db.hook = func() {
		if _, err := f.other.Lease(f.ctx, meter.LeaseRequest{Subject: f.a, Requests: 1}); err != nil {
			t.Errorf("Lease of requests before a transaction: %v", err)
		}
	}
	b, err := f.hooked.SetTenantBudget(f.ctx, meter.SetTenantBudgetRequest{
		TenantID: f.tenant, Ceiling: limes.BudgetCeiling{Max: settleRaceCeiling, UnitCode: testUnit}, ResetSpend: true, ExpectedVersion: f.version,
	})
	f.db.every = false
	f.mustHaveMet(t)
	if err != nil || b.SpentAmount != 0 || b.ResourceVersion != f.version+1 {
		t.Fatalf("SetTenantBudget under leases that keep opening = %+v, %v; want the period reset", b, err)
	}
	if logs := f.logs.String(); strings.Contains(logs, "level=WARN") || strings.Contains(logs, "level=ERROR") {
		t.Errorf("logs = %q, want the reset to settle and act at once", logs)
	}
	if _, err := f.other.GetLease(f.ctx, f.a.CapabilityID()); !errors.Is(err, meter.ErrLeaseNotFound) {
		t.Errorf("the lease opened before the reset is open past it: %v", err)
	}
}

// A lease opened below a capability before every transaction of
// DeleteUsage does not keep its counters from going: the drop settles the
// leases from the capability down in its own transaction.
func TestDeleteUsageSettlesLeasesThatKeepOpening(t *testing.T) {
	f := newSettleRaceFixture(t)
	f.db.every = true
	f.db.hook = func() {
		if _, err := f.other.Lease(f.ctx, meter.LeaseRequest{Subject: f.a, Requests: 1}); err != nil {
			t.Errorf("Lease of requests before a transaction: %v", err)
		}
	}
	err := f.hooked.DeleteUsage(f.ctx, meter.DeleteUsageRequest{CapabilityID: f.root})
	f.db.every = false
	f.mustHaveMet(t)
	if err != nil {
		t.Fatalf("DeleteUsage under leases that keep opening: %v, want the counters dropped", err)
	}
	if logs := f.logs.String(); strings.Contains(logs, "level=WARN") || strings.Contains(logs, "level=ERROR") {
		t.Errorf("logs = %q, want the drop to settle and act at once", logs)
	}
	if _, err := f.other.GetLease(f.ctx, f.a.CapabilityID()); !errors.Is(err, meter.ErrLeaseNotFound) {
		t.Errorf("the lease opened below before the drop is open past it: %v", err)
	}
}

// settleRaceRequests is what the requests-only lease of the reset race
// holds.
const settleRaceRequests = 5

// A lease of requests alone opened just before a reset does not carry the
// closed period into the next: topped up with an
// amount, what it spends is the new period's, so the tenant's ceiling holds.
func TestResetKeepsARequestsLeaseOutOfTheNextPeriod(t *testing.T) {
	f := newSettleRaceFixture(t)
	f.db.at = txReset
	f.db.hook = func() {
		if _, err := f.other.Lease(f.ctx, meter.LeaseRequest{Subject: f.a, Requests: settleRaceRequests}); err != nil {
			t.Errorf("Lease of requests in the window: %v", err)
		}
	}
	if _, err := f.hooked.SetTenantBudget(f.ctx, meter.SetTenantBudgetRequest{
		TenantID: f.tenant, Ceiling: limes.BudgetCeiling{Max: settleRaceCeiling, UnitCode: testUnit}, ResetSpend: true, ExpectedVersion: f.version,
	}); err != nil {
		t.Fatalf("SetTenantBudget: %v", err)
	}
	f.mustHaveMet(t)
	if _, err := f.other.Lease(f.ctx, meter.LeaseRequest{Subject: f.a, Amount: settleRaceCeiling}); err != nil {
		t.Fatalf("Lease of an amount after the reset: %v", err)
	}
	if _, err := f.other.Charge(f.ctx, meter.ChargeRequest{Subject: f.a, Amount: settleRaceCeiling}, nil); err != nil {
		t.Fatalf("Charge under the lease: %v", err)
	}
	if _, err := f.other.SettleLease(f.ctx, meter.SettleLeaseRequest{Subject: f.a}); err != nil {
		t.Fatalf("SettleLease: %v", err)
	}
	if _, err := f.other.Charge(f.ctx, meter.ChargeRequest{Subject: f.b, Amount: settleRaceCeiling}, nil); !errors.Is(err, limes.ErrTenantBudgetExceeded) {
		t.Errorf("a charge past the new period's ceiling: %v, want ErrTenantBudgetExceeded", err)
	}
}

// settleRaceSpent is what the lease opened past DeleteUsage spends.
const settleRaceSpent = settleRaceCeiling / 2

// A lease opened right after DeleteUsage dropped an ancestor's counters
// holds against the counters restarted for it, and is settled against them:
// the root holds nothing of it once settled and is charged what it spent.
func TestALeaseOpenedPastDeleteUsageSettlesOnTheRestartedCounters(t *testing.T) {
	f := newSettleRaceFixture(t)
	opened := false
	f.db.at = txAfterDrop
	f.db.hook = func() {
		opened = true
		if _, err := f.other.Lease(f.ctx, meter.LeaseRequest{Subject: f.a, Amount: settleRaceCeiling}); err != nil {
			t.Errorf("Lease past the drop: %v", err)
		}
	}
	if err := f.hooked.DeleteUsage(f.ctx, meter.DeleteUsageRequest{CapabilityID: f.root}); err != nil {
		t.Fatalf("DeleteUsage: %v", err)
	}
	if !opened {
		// DeleteUsage ran no transaction past its drop: the lease opens
		// as soon as it returns.
		f.db.hook()
	}
	if _, err := f.other.Charge(f.ctx, meter.ChargeRequest{Subject: f.a, Amount: settleRaceSpent}, nil); err != nil {
		t.Fatalf("Charge under the lease: %v", err)
	}
	if _, err := f.other.SettleLease(f.ctx, meter.SettleLeaseRequest{Subject: f.a}); err != nil {
		t.Fatalf("SettleLease: %v", err)
	}
	root, err := f.other.GetUsage(f.ctx, f.root)
	if err != nil || root.ReservedAmount != 0 || root.SpentAmount != settleRaceSpent {
		t.Errorf("the root once the lease settled = %+v, %v; want %v spent and nothing held", root, err, settleRaceSpent)
	}
}
