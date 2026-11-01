package pgstore

import (
	"errors"
	"testing"
	"time"
	"uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/meter"
)

// gateLeased is what the gate tests lease.
const gateLeased = limes.Nanos(10)

// leaseholderOf records a capability of a new tenant and returns it as a
// meter sees it.
func leaseholderOf(t *testing.T) limes.MeterSubject {
	t.Helper()
	tenant := newTenant(t)
	records, err := NewStore(Config{DB: app, CheckTenant: checkTenant})
	if err != nil {
		t.Fatal(err)
	}
	c := limes.Capability{
		ID: uuid.New(), ExpiresAt: time.Now().Add(time.Hour).Truncate(time.Second),
		Subject: limes.Principal{Type: limes.PrincipalTypeAgent, TenantID: tenant, Subject: "agent"},
		Caveats: limes.Caveats{Ops: []limes.Op{limes.OpGet}, Requests: limes.RequestCeiling{Unlimited: true}, Budget: limes.BudgetCeiling{Unlimited: true, UnitCode: testUnit}},
	}
	if err := records.Insert(WithTenant(t.Context(), tenant), limes.InsertRequest{Capability: c}); err != nil {
		t.Fatal(err)
	}
	return meterSubject(c.ID, tenant)
}

// A store whose Config does not enable leasing opens no lease, so a
// release that knows nothing of leases can serve beside it; one that does
// opens it.
func TestLeaseRefusedUntilLeasingIsEnabled(t *testing.T) {
	fresh(t)
	subject := leaseholderOf(t)
	ctx := WithTenant(t.Context(), subject.TenantID())
	off, err := NewUsageStore(Config{DB: app})
	if err != nil {
		t.Fatal(err)
	}
	_, err = off.Lease(ctx, meter.LeaseRequest{Subject: subject, Amount: gateLeased})
	if !errors.Is(err, meter.ErrLeasingDisabled) || !errors.Is(err, limes.ErrInvalidConfig) {
		t.Fatalf("Lease without Config.Leases = %v, want ErrLeasingDisabled", err)
	}
	if u, err := off.GetUsage(ctx, subject.CapabilityID()); err == nil && u.ReservedAmount != 0 {
		t.Errorf("a refused lease held %v", u.ReservedAmount)
	}
	on, err := NewUsageStore(Config{DB: app, Leases: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := on.Lease(ctx, meter.LeaseRequest{Subject: subject, Amount: gateLeased}); err != nil {
		t.Fatalf("Lease with Config.Leases: %v", err)
	}
}

// A store that no longer enables leasing — on the way back to a release
// that knows nothing of them — still charges under and settles the leases
// open: only opening one is refused.
func TestLeasesOpenAreSettledWithLeasingDisabled(t *testing.T) {
	fresh(t)
	subject := leaseholderOf(t)
	ctx := WithTenant(t.Context(), subject.TenantID())
	on, err := NewUsageStore(Config{DB: app, Leases: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := on.Lease(ctx, meter.LeaseRequest{Subject: subject, Amount: gateLeased}); err != nil {
		t.Fatal(err)
	}
	off, err := NewUsageStore(Config{DB: app})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := off.Charge(ctx, meter.ChargeRequest{Subject: subject, Amount: 1}, nil); err != nil {
		t.Fatalf("Charge under the open lease: %v", err)
	}
	settled, err := off.SettleLease(ctx, meter.SettleLeaseRequest{Subject: subject})
	if err != nil || settled.Spent != 1 {
		t.Fatalf("SettleLease = %+v, %v; want the lease settled with 1 spent", settled, err)
	}
}
