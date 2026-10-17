package pgstore

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes"
)

// sqlLedgerCharge writes a charge straight into the ledger, as the owner:
// sums past MaxNanos take more charges than any check should make.
const sqlLedgerCharge = `
INSERT INTO ` + schema + `.charges
    (id, capability_id, tenant_id, amount, unit_code, op, actor, at, ancestors, copies, overrun, tenant_period)
VALUES ($1, $2, $3, $4, $5, '', '', $6, '{}', '{}', false, 0)`

// A sum past MaxNanos is refused as ErrCounterOverflow, not wrapped round or
// rounded: the database sums exactly, and so must what reads it.
func TestSumChargesRefusesASumPastMaxNanos(t *testing.T) {
	fresh(t)
	tenant, capID := uuid.New(), uuid.New()
	at := time.Now().UTC().Truncate(timeResolution)
	for range 2 {
		if _, err := owner.Exec(t.Context(), sqlLedgerCharge, uuid.New(), capID, tenant, int64(limes.MaxNanos), limes.DefaultUnitCode, at); err != nil {
			t.Fatal(err)
		}
	}
	ledger, err := NewLedger(Config{DB: app})
	if err != nil {
		t.Fatal(err)
	}
	_, err = ledger.SumCharges(WithTenant(t.Context(), tenant), limes.SumChargesRequest{
		TenantID: tenant, From: at.Add(-time.Hour), To: at.Add(time.Hour),
	})
	if !errors.Is(err, limes.ErrCounterOverflow) {
		t.Errorf("SumCharges past MaxNanos: err = %v, want ErrCounterOverflow", err)
	}
}

// A ledger reader needs a database, as the stores do.
func TestNewLedgerRequiresADB(t *testing.T) {
	if _, err := NewLedger(Config{}); !errors.Is(err, errNoDB) {
		t.Errorf("NewLedger without a DB: err = %v, want errNoDB", err)
	}
}

// A test backdates charges through the store's clock, never through the
// charges table: built on a clock the test sets, the stores write each
// charge at the clock's time, and the ledger sums it in that time's bucket.
func TestChargesLandWhereTheStoresClockSays(t *testing.T) {
	fresh(t)
	start := time.Date(2026, time.March, 2, 9, 0, 0, 0, time.UTC) // a Monday
	at := start
	cfg := Config{DB: app, Now: func() time.Time { return at }}
	records, err := NewStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	usage, err := NewUsageStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := NewLedger(cfg)
	if err != nil {
		t.Fatal(err)
	}
	const (
		period = 14 * 24 * time.Hour
		amount = limes.Nanos(limes.NanosPerUnit)
	)
	tenant := uuid.New()
	ctx := WithTenant(t.Context(), tenant)
	c := limes.Capability{
		ID: uuid.New(), ExpiresAt: start.Add(period),
		Subject: limes.Principal{Type: limes.PrincipalTypeService, TenantID: tenant, Subject: "svc:reporting"},
		Caveats: limes.Caveats{Requests: limes.RequestCeiling{Unlimited: true}, Budget: limes.BudgetCeiling{Unlimited: true}},
	}
	if err := records.Insert(ctx, limes.InsertRequest{Capability: c}); err != nil {
		t.Fatal(err)
	}
	subject, err := limes.StoredMeterSubject(ctx, records, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	days := []int{0, 1, 8} // Monday, Tuesday, and Tuesday a week on
	for _, day := range days {
		at = start.AddDate(0, 0, day)
		receipt, err := usage.Charge(ctx, limes.ChargeRequest{Subject: subject, Amount: amount}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got, err := usage.GetCharge(ctx, receipt.ChargeID); err != nil || !got.At.Equal(at) {
			t.Errorf("the charge made on day %d is at %s, %v; want %s", day, got.At, err, at)
		}
	}
	page, err := ledger.SumCharges(ctx, limes.SumChargesRequest{TenantID: tenant, From: start, To: start.Add(period), Bucket: limes.ChargeBucketDay})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != len(days) {
		t.Fatalf("SumCharges by day = %+v, want one bucket for each of days %v", page.Items, days)
	}
	for i, day := range days {
		want := time.Date(2026, time.March, 2+day, 0, 0, 0, 0, time.UTC)
		if got := page.Items[i]; !got.BucketStart.Equal(want) || got.Charged != amount {
			t.Errorf("bucket %d = %s, %s; want %s, %s", i, got.BucketStart, got.Charged, want, amount)
		}
	}
}
