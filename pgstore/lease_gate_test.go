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

// A store whose Config disables leasing opens no lease, so a release that
// knows nothing of leases can serve beside it; by default a store opens it.
func TestLeaseRefusedWhileLeasingIsDisabled(t *testing.T) {
	fresh(t)
	subject := leaseholderOf(t)
	ctx := WithTenant(t.Context(), subject.TenantID())
	off, err := NewUsageStore(Config{DB: app, DisableLeases: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = off.Lease(ctx, meter.LeaseRequest{Subject: subject, Amount: gateLeased})
	if !errors.Is(err, meter.ErrLeasingDisabled) || !errors.Is(err, limes.ErrInvalidConfig) {
		t.Fatalf("Lease with Config.DisableLeases = %v, want ErrLeasingDisabled", err)
	}
	if u, err := off.GetUsage(ctx, subject.CapabilityID()); err == nil && u.ReservedAmount != 0 {
		t.Errorf("a refused lease held %v", u.ReservedAmount)
	}
	if _, err := meter.As[meter.Leaser](off); !errors.Is(err, limes.ErrInvalidConfig) {
		t.Errorf("As[Leaser] of a store with leases off = %v, want ErrInvalidConfig", err)
	}
	on, err := NewUsageStore(Config{DB: app})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := on.Lease(ctx, meter.LeaseRequest{Subject: subject, Amount: gateLeased}); err != nil {
		t.Fatalf("Lease of a store by default: %v", err)
	}
	if _, err := meter.As[meter.Leaser](on); err != nil {
		t.Errorf("As[Leaser] of a store by default = %v", err)
	}
}

// unknownFeature is a feature package meter does not define.
const unknownFeature meter.Feature = "no_such_feature"

// The store says leases are off exactly when Config.DisableLeases is set,
// every other feature of meter on, and one meter does not define off.
func TestFeatureEnabledFollowsDisableLeases(t *testing.T) {
	for _, leases := range []bool{true, false} {
		s := &UsageStore{conn{leases: leases}}
		for _, f := range meter.AllFeatures() {
			want := f != meter.FeatureLeases || leases
			if got := s.FeatureEnabled(f); got != want {
				t.Errorf("leases %v: FeatureEnabled(%s) = %v, want %v", leases, f, got, want)
			}
		}
		if s.FeatureEnabled(unknownFeature) {
			t.Errorf("leases %v: FeatureEnabled(%s) = true, want false", leases, unknownFeature)
		}
	}
}

// A store that no longer enables leasing — on the way back to a release
// that knows nothing of them — still charges under and settles the leases
// open: only opening one is refused.
func TestLeasesOpenAreSettledWithLeasingDisabled(t *testing.T) {
	fresh(t)
	subject := leaseholderOf(t)
	ctx := WithTenant(t.Context(), subject.TenantID())
	on, err := NewUsageStore(Config{DB: app})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := on.Lease(ctx, meter.LeaseRequest{Subject: subject, Amount: gateLeased}); err != nil {
		t.Fatal(err)
	}
	off, err := NewUsageStore(Config{DB: app, DisableLeases: true})
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
