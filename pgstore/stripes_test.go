package pgstore

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"sync"
	"testing"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/meter"
)

const (
	// sqlCountStripes counts tenant $1's stripes.
	sqlCountStripes = `SELECT count(*) FROM ` + schema + `.tenant_budget_stripes WHERE tenant_id = $1`
	// sqlRowSpent reads tenant $1's aggregate row alone, without its
	// stripes.
	sqlRowSpent = `SELECT spent, reserved, resource_version FROM ` + schema + `.tenant_budgets WHERE tenant_id = $1`
	// sqlUnclampedSum is tenant $1's row and stripes summed with no clamp:
	// what Decision 10 holds never negative.
	sqlUnclampedSum = `
SELECT b.spent + COALESCE((SELECT sum(x.spent) FROM ` + schema + `.tenant_budget_stripes x WHERE x.tenant_id = b.tenant_id), 0),
       b.reserved + COALESCE((SELECT sum(x.reserved) FROM ` + schema + `.tenant_budget_stripes x WHERE x.tenant_id = b.tenant_id), 0)
FROM ` + schema + `.tenant_budgets b WHERE b.tenant_id = $1`
)

// stripedCharges is how many charges the tests make on a striped tenant,
// and stripedAmount each one's amount.
const (
	stripedCharges = 3
	stripedAmount  = 7
)

// busyLockTimeout is the bound SetTenantBudget waits for in the test of a
// tenant held by a call in flight: short, so the test does not wait the
// default's seconds.
const busyLockTimeout = 50 * time.Millisecond

// capabilityOf records a capability of tenant, with no ceiling of its own,
// and returns it as a meter sees it.
func capabilityOf(t *testing.T, tenant uuid.UUID) limes.MeterSubject {
	t.Helper()
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

// stripedStore is a usage store that stripes tenants with no ceiling, as
// one does by default.
func stripedStore(t *testing.T, cfg Config) *UsageStore {
	t.Helper()
	cfg.DB = app
	usage, err := NewUsageStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return usage
}

// countStripes counts tenant's stripes.
func countStripes(t *testing.T, tenant uuid.UUID) int64 {
	t.Helper()
	var n int64
	if err := owner.QueryRow(context.Background(), sqlCountStripes, tenant).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// chargeStriped charges subject stripedCharges times stripedAmount.
func chargeStriped(t *testing.T, usage *UsageStore, subject limes.MeterSubject) {
	t.Helper()
	ctx := WithTenant(t.Context(), subject.TenantID())
	for range stripedCharges {
		if _, err := usage.Charge(ctx, meter.ChargeRequest{Subject: subject, Amount: stripedAmount}, nil); err != nil {
			t.Fatal(err)
		}
	}
}

// A store of a zero Config stripes a tenant with no ceiling.
func TestStripesByDefault(t *testing.T) {
	fresh(t)
	usage, err := NewUsageStore(Config{DB: app})
	if err != nil {
		t.Fatal(err)
	}
	subject := leaseholderOf(t)
	chargeStriped(t, usage, subject)
	if n := countStripes(t, subject.TenantID()); n == 0 {
		t.Error("a store of a zero Config wrote no stripe, want the tenant's charges on stripes")
	}
}

// Disabled, stripes are written by no call: the charge goes to the row.
func TestStripesDisabledWriteNone(t *testing.T) {
	fresh(t)
	usage, err := NewUsageStore(Config{DB: app, DisableTenantStripes: true})
	if err != nil {
		t.Fatal(err)
	}
	subject := leaseholderOf(t)
	chargeStriped(t, usage, subject)
	if n := countStripes(t, subject.TenantID()); n != 0 {
		t.Errorf("stripes with DisableTenantStripes = %d, want none", n)
	}
	var spent, reserved, version int64
	if err := owner.QueryRow(t.Context(), sqlRowSpent, subject.TenantID()).Scan(&spent, &reserved, &version); err != nil {
		t.Fatal(err)
	}
	if spent != stripedCharges*stripedAmount {
		t.Errorf("row spent = %d, want %d", spent, stripedCharges*stripedAmount)
	}
}

// On, a tenant with no ceiling is charged on a stripe, with its row held
// by another transaction all along: the charge locks no row. The tenant's
// budget reads the row and its stripes summed, and the wait reported is
// the stripe's, under the call's operation.
func TestStripedChargeLocksNoRow(t *testing.T) {
	fresh(t)
	waits := &tenantWaits{}
	usage := stripedStore(t, Config{Events: meter.StoreEvents{OnTenantWait: waits.record}})
	subject := leaseholderOf(t)
	tenant := subject.TenantID()
	ctx := WithTenant(t.Context(), tenant)
	if _, err := usage.Charge(ctx, meter.ChargeRequest{Subject: subject, Amount: stripedAmount}, nil); err != nil {
		t.Fatal(err)
	}
	if got := waits.take(); len(got) != 1 || got[0].Operation != meter.StoreOperationCharge || got[0].TenantID != tenant {
		t.Fatalf("a striped Charge reported %+v, want one Charge wait of tenant %s", got, tenant)
	}

	other, err := owner.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Rollback(context.Background()) }()
	if _, err := other.Exec(context.Background(), sqlHoldTenant, tenant); err != nil {
		t.Fatal(err)
	}
	chargeCtx, cancel := context.WithTimeout(ctx, lockWaitTimeout)
	defer cancel()
	for range stripedCharges - 1 {
		if _, err := usage.Charge(chargeCtx, meter.ChargeRequest{Subject: subject, Amount: stripedAmount}, nil); err != nil {
			t.Fatalf("a striped Charge behind a held row: %v", err)
		}
	}
	if err := other.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}

	if n := countStripes(t, tenant); n == 0 {
		t.Errorf("no stripe after striped charges")
	}
	b, err := usage.GetTenantBudget(ctx, tenant)
	if err != nil {
		t.Fatal(err)
	}
	if b.SpentAmount != stripedCharges*stripedAmount {
		t.Errorf("GetTenantBudget spent = %v, want %d", b.SpentAmount, stripedCharges*stripedAmount)
	}
	page, err := usage.ListTenantBudgets(WithAllTenantsReadOnly(t.Context()), meter.ListTenantBudgetsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, listed := range page.Items {
		if listed.TenantID == tenant {
			found = true
			if listed.SpentAmount != b.SpentAmount {
				t.Errorf("ListTenantBudgets spent = %v, GetTenantBudget %v", listed.SpentAmount, b.SpentAmount)
			}
		}
	}
	if !found {
		t.Errorf("ListTenantBudgets does not list tenant %s", tenant)
	}
}

// SetTenantBudget folds the stripes into the row before it sets the
// ceiling, without bumping the version for the fold; then the tenant has a
// ceiling, and its calls lock the row and write no stripe.
func TestSetTenantBudgetFoldsStripes(t *testing.T) {
	fresh(t)
	usage := stripedStore(t, Config{})
	subject := leaseholderOf(t)
	tenant := subject.TenantID()
	ctx := WithTenant(t.Context(), tenant)
	chargeStriped(t, usage, subject)

	const ceiling = 1000
	b, err := usage.SetTenantBudget(ctx, meter.SetTenantBudgetRequest{TenantID: tenant, Ceiling: limes.BudgetCeiling{Max: ceiling, UnitCode: testUnit}})
	if err != nil {
		t.Fatal(err)
	}
	if b.SpentAmount != stripedCharges*stripedAmount || b.ResourceVersion != 1 {
		t.Errorf("SetTenantBudget = spent %v, version %d; want %d, 1", b.SpentAmount, b.ResourceVersion, stripedCharges*stripedAmount)
	}
	if n := countStripes(t, tenant); n != 0 {
		t.Errorf("stripes after SetTenantBudget = %d, want none", n)
	}
	var spent, reserved, version int64
	if err := owner.QueryRow(t.Context(), sqlRowSpent, tenant).Scan(&spent, &reserved, &version); err != nil {
		t.Fatal(err)
	}
	if spent != stripedCharges*stripedAmount || version != 1 {
		t.Errorf("row = spent %d, version %d; want %d, 1", spent, version, stripedCharges*stripedAmount)
	}

	chargeStriped(t, usage, subject)
	if n := countStripes(t, tenant); n != 0 {
		t.Errorf("stripes of a tenant with a ceiling = %d, want none", n)
	}
}

// A store with DisableTenantStripes folds the stripes another store wrote,
// as soon as a call of it locks the row.
func TestLockingFoldsStripes(t *testing.T) {
	fresh(t)
	subject := leaseholderOf(t)
	tenant := subject.TenantID()
	chargeStriped(t, stripedStore(t, Config{}), subject)
	locking, err := NewUsageStore(Config{DB: app, DisableTenantStripes: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := locking.Charge(WithTenant(t.Context(), tenant), meter.ChargeRequest{Subject: subject, Amount: stripedAmount}, nil); err != nil {
		t.Fatal(err)
	}
	if n := countStripes(t, tenant); n != 0 {
		t.Errorf("stripes after a locking charge = %d, want none", n)
	}
	var spent, reserved, version int64
	if err := owner.QueryRow(t.Context(), sqlRowSpent, tenant).Scan(&spent, &reserved, &version); err != nil {
		t.Fatal(err)
	}
	if want := int64(stripedCharges+1) * stripedAmount; spent != want {
		t.Errorf("row spent = %d, want %d", spent, want)
	}
}

// SetTenantBudget waits a bounded time for the tenant's calls in flight;
// past it, it changes nothing, logs why and refuses with a
// TenantBudgetBusyError, which the caller may send again. The error says
// who held the tenant: a holder of the store's own role by how long it ran
// and whether it was idle in its transaction, one of another role — the
// schema's owner here — counted alone, since PostgreSQL shows the store
// none of its session.
func TestSetTenantBudgetBusy(t *testing.T) {
	for _, tc := range []struct {
		name    string
		holder  func() (pgx.Tx, error)
		visible bool
	}{
		{"a holder of the store's role", func() (pgx.Tx, error) { return app.Begin(context.Background()) }, true},
		{"a holder of another role", func() (pgx.Tx, error) { return owner.Begin(context.Background()) }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fresh(t)
			logged := &logRecords{}
			usage := stripedStore(t, Config{Logger: slog.New(logged), TenantBudgetLockTimeout: busyLockTimeout})
			subject := leaseholderOf(t)
			tenant := subject.TenantID()
			ctx := WithTenant(t.Context(), tenant)
			chargeStriped(t, usage, subject)

			inFlight, err := tc.holder()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = inFlight.Rollback(context.Background()) }()
			if _, err := inFlight.Exec(context.Background(), sqlShareTenantLock, eraseLockClass, tenant); err != nil {
				t.Fatal(err)
			}
			req := meter.SetTenantBudgetRequest{TenantID: tenant, Ceiling: limes.BudgetCeiling{Max: limes.MaxNanos, UnitCode: testUnit}}
			_, err = usage.SetTenantBudget(ctx, req)
			if !errors.Is(err, meter.ErrTenantBudgetBusy) || errors.Is(err, limes.ErrConflict) || errors.Is(err, limes.ErrInternal) {
				t.Fatalf("SetTenantBudget behind a call in flight = %v, want ErrTenantBudgetBusy alone", err)
			}
			var busy *meter.TenantBudgetBusyError
			if !errors.As(err, &busy) {
				t.Fatalf("SetTenantBudget behind a call in flight = %T, want a TenantBudgetBusyError", err)
			}
			if busy.TenantID != tenant || busy.Waited < busyLockTimeout || busy.Holders != 1 {
				t.Errorf("busy = tenant %s, waited %s, %d holders; want %s, at least %s, 1", busy.TenantID, busy.Waited, busy.Holders, tenant, busyLockTimeout)
			}
			switch {
			case !tc.visible && busy.Oldest != nil:
				t.Errorf("a holder of another role seen as %+v, want none", *busy.Oldest)
			case tc.visible && busy.Oldest == nil:
				t.Error("a holder of the store's role not seen")
			case tc.visible && (!busy.Oldest.IdleInTransaction || busy.Oldest.Running < busyLockTimeout):
				t.Errorf("the holder seen as %+v, want idle in its transaction, running at least %s", *busy.Oldest, busyLockTimeout)
			}
			if n := logged.waitWarnings(); n != 1 {
				t.Errorf("busy SetTenantBudget logged %d wait warnings, want 1", n)
			}
			if n := countStripes(t, tenant); n == 0 {
				t.Errorf("a busy SetTenantBudget folded the stripes")
			}
			if err := inFlight.Rollback(context.Background()); err != nil {
				t.Fatal(err)
			}
			if _, err := usage.SetTenantBudget(ctx, req); err != nil {
				t.Fatalf("SetTenantBudget once the call ended: %v", err)
			}
		})
	}
}

// TenantBudgetLockTimeout of zero is the default; one PostgreSQL's
// lock_timeout cannot hold, below a millisecond and so set as no bound, is
// refused, as is a negative one.
func TestTenantBudgetLockTimeoutConfig(t *testing.T) {
	for _, tc := range []struct {
		timeout time.Duration
		want    time.Duration
		refused bool
	}{
		{0, defaultTenantBudgetLockTimeout, false},
		{lockTimeoutResolution, lockTimeoutResolution, false},
		{time.Minute, time.Minute, false},
		{lockTimeoutResolution - time.Microsecond, 0, true},
		{-time.Second, 0, true},
	} {
		usage, err := NewUsageStore(Config{DB: app, TenantBudgetLockTimeout: tc.timeout})
		switch {
		case tc.refused && !errors.Is(err, limes.ErrInvalidConfig):
			t.Errorf("TenantBudgetLockTimeout %s: %v, want ErrInvalidConfig", tc.timeout, err)
		case !tc.refused && err != nil:
			t.Errorf("TenantBudgetLockTimeout %s: %v", tc.timeout, err)
		case !tc.refused && usage.tenantBudgetLockTimeout != tc.want:
			t.Errorf("TenantBudgetLockTimeout %s is %s, want %s", tc.timeout, usage.tenantBudgetLockTimeout, tc.want)
		}
	}
}

// lockKeyHalfSQL is one 32-bit half of a key, every bit set.
func TestLockKeyHalf(t *testing.T) {
	if lockKeyHalfSQL != strconv.FormatUint(math.MaxUint32, 10) {
		t.Errorf("lockKeyHalfSQL = %s, want %d", lockKeyHalfSQL, uint64(math.MaxUint32))
	}
}

// An erase of the tenant's usage removes its stripes with its row.
func TestEraseRemovesStripes(t *testing.T) {
	fresh(t)
	usage := stripedStore(t, Config{})
	subject := leaseholderOf(t)
	tenant := subject.TenantID()
	chargeStriped(t, usage, subject)
	if _, err := usage.EraseTenantUsage(WithTenant(t.Context(), tenant), meter.EraseTenantUsageRequest{TenantID: tenant}); err != nil {
		t.Fatal(err)
	}
	if n := countStripes(t, tenant); n != 0 {
		t.Errorf("stripes after the erase = %d, want none", n)
	}
}

// The differential test's shape: how many workers run each round on each
// tenant, how many rounds, and the round SetTenantBudget runs in.
const (
	twinWorkers   = 6
	twinRounds    = 4
	twinSetRound  = 1
	twinHold      = 5
	twinSettled   = 2
	twinReleased  = 3
	twinRefunded  = 1
	twinCallsEach = 5
)

// twinTenant is one side of the differential test: its capabilities, one
// per worker.
type twinTenant struct {
	id       uuid.UUID
	subjects []limes.MeterSubject
}

// twinRound runs one worker's round on t: a charge, a hold settled for
// less, a hold released, and a refund of part of the charge.
func twinRound(ctx context.Context, usage *UsageStore, subject limes.MeterSubject, amount limes.Nanos) error {
	charged, err := usage.Charge(ctx, meter.ChargeRequest{Subject: subject, Amount: amount}, nil)
	if err != nil {
		return fmt.Errorf("charge: %w", err)
	}
	held, err := usage.Reserve(ctx, meter.ReserveRequest{Subject: subject, Amount: twinHold})
	if err != nil {
		return fmt.Errorf("reserve: %w", err)
	}
	if _, err := usage.Settle(ctx, meter.SettleRequest{Subject: subject, ReservationID: held.ID, Amount: twinSettled}, nil); err != nil {
		return fmt.Errorf("settle: %w", err)
	}
	released, err := usage.Reserve(ctx, meter.ReserveRequest{Subject: subject, Amount: twinReleased})
	if err != nil {
		return fmt.Errorf("reserve: %w", err)
	}
	if err := usage.Release(ctx, meter.ReleaseRequest{Subject: subject, ReservationID: released.ID}); err != nil {
		return fmt.Errorf("release: %w", err)
	}
	if _, err := usage.Refund(ctx, meter.RefundRequest{ChargeID: charged.ChargeID, Amount: twinRefunded}, nil); err != nil {
		return fmt.Errorf("refund: %w", err)
	}
	return nil
}

// setAgain sets t's ceiling to what it is, reading its version first: a
// SetTenantBudget that changes nothing but folds.
func setAgain(ctx context.Context, usage *UsageStore, tenant uuid.UUID, ceiling limes.BudgetCeiling) error {
	b, err := usage.GetTenantBudget(ctx, tenant)
	if err != nil {
		return err
	}
	_, err = usage.SetTenantBudget(ctx, meter.SetTenantBudgetRequest{TenantID: tenant, Ceiling: ceiling, ExpectedVersion: b.ResourceVersion})
	return err
}

// Decision 10 of docs/adr/0022-unlimited-tenant-aggregate.md: one sequence
// of Charge, Reserve, Settle, Release and Refund from several connections
// on a striped tenant and on its twin, which takes the row's path under a
// ceiling of limes.MaxNanos, with a SetTenantBudget on each while calls
// are in flight. After every round the two tenants' budgets agree, and the
// striped tenant's row and stripes summed unclamped are never negative.
func TestStripesAgreeWithTheRow(t *testing.T) {
	fresh(t)
	usage := stripedStore(t, Config{})
	striped, twin := twinTenant{id: newTenant(t)}, twinTenant{id: newTenant(t)}
	for _, side := range []*twinTenant{&striped, &twin} {
		for range twinWorkers {
			side.subjects = append(side.subjects, capabilityOf(t, side.id))
		}
	}
	twinCeiling := limes.BudgetCeiling{Max: limes.MaxNanos, UnitCode: testUnit}
	stripedCeiling := limes.BudgetCeiling{Unlimited: true, UnitCode: testUnit}
	// The striped tenant's aggregate starts as a first charge creates it;
	// the twin's with its ceiling.
	if _, err := usage.Charge(WithTenant(t.Context(), striped.id), meter.ChargeRequest{Subject: striped.subjects[0], Amount: twinRefunded}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := usage.Charge(WithTenant(t.Context(), twin.id), meter.ChargeRequest{Subject: twin.subjects[0], Amount: twinRefunded}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := usage.SetTenantBudget(WithTenant(t.Context(), twin.id), meter.SetTenantBudgetRequest{TenantID: twin.id, Ceiling: twinCeiling}); err != nil {
		t.Fatal(err)
	}

	for round := range twinRounds {
		var wg sync.WaitGroup
		errs := make(chan error, twinWorkers*twinCallsEach)
		for w := range twinWorkers {
			for _, side := range []twinTenant{striped, twin} {
				wg.Go(func() {
					ctx := WithTenant(context.Background(), side.id)
					if err := twinRound(ctx, usage, side.subjects[w], limes.Nanos(w+twinCallsEach)); err != nil {
						errs <- fmt.Errorf("tenant %s, worker %d: %w", side.id, w, err)
					}
				})
			}
		}
		if round == twinSetRound {
			wg.Go(func() {
				if err := setAgain(WithTenant(context.Background(), striped.id), usage, striped.id, stripedCeiling); err != nil {
					errs <- fmt.Errorf("SetTenantBudget of the striped tenant: %w", err)
				}
			})
			wg.Go(func() {
				if err := setAgain(WithTenant(context.Background(), twin.id), usage, twin.id, twinCeiling); err != nil {
					errs <- fmt.Errorf("SetTenantBudget of the twin: %w", err)
				}
			})
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Fatalf("round %d: %v", round, err)
		}

		s, err := usage.GetTenantBudget(WithTenant(t.Context(), striped.id), striped.id)
		if err != nil {
			t.Fatal(err)
		}
		w, err := usage.GetTenantBudget(WithTenant(t.Context(), twin.id), twin.id)
		if err != nil {
			t.Fatal(err)
		}
		if s.SpentAmount != w.SpentAmount || s.ReservedAmount != w.ReservedAmount {
			t.Fatalf("round %d: striped spent %v, reserved %v; twin %v, %v", round, s.SpentAmount, s.ReservedAmount, w.SpentAmount, w.ReservedAmount)
		}
		var spent, reserved int64
		if err := owner.QueryRow(t.Context(), sqlUnclampedSum, striped.id).Scan(&spent, &reserved); err != nil {
			t.Fatal(err)
		}
		if spent < 0 || reserved < 0 || limes.Nanos(spent) != s.SpentAmount || limes.Nanos(reserved) != s.ReservedAmount {
			t.Fatalf("round %d: unclamped sum = spent %d, reserved %d; budget %v, %v", round, spent, reserved, s.SpentAmount, s.ReservedAmount)
		}
	}
	if n := countStripes(t, striped.id); n == 0 {
		t.Errorf("the striped tenant has no stripe: the test did not stripe")
	}
}
