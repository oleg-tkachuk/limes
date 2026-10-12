package limes_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/internal/link"
	"github.com/oleg-tkachuk/limes/memstore"
)

// Instrument names and attributes as an exporter reads them.
const (
	metricChargeAmount    = "limes.charge.amount"
	metricChargeDecisions = "limes.charge.decisions"
	metricRequestBumps    = "limes.request.bumps"
	metricRefundAmount    = "limes.refund.amount"
	metricReservations    = "limes.reservation.decisions"
	attrOutcome           = "outcome"
	attrUnit              = "unit_code"
	attrTenant            = "tenant_id"
)

// meteredStore is a metered memstore whose measurements reader collects.
func meteredStore(t *testing.T, opts ...limes.InstrumentOption) (limes.UsageStore[struct{}], *sdkmetric.ManualReader, *memstore.Store) {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	opts = append(opts, limes.WithMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))))
	records, err := memstore.NewStore(memstore.StoreConfig{})
	if err != nil {
		t.Fatal(err)
	}
	inner, err := memstore.NewUsageStore(memstore.UsageStoreConfig[struct{}]{Records: records})
	if err != nil {
		t.Fatal(err)
	}
	usage, err := limes.Instrument[struct{}](inner, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return usage, reader, records
}

// point is one data point of a sum: its value and attributes.
type point struct {
	value float64
	attrs attribute.Set
}

// points collects every sum data point of the named instrument.
func points(t *testing.T, reader *sdkmetric.ManualReader, name string) []point {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	var out []point
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			switch data := m.Data.(type) {
			case metricdata.Sum[int64]:
				for _, dp := range data.DataPoints {
					out = append(out, point{float64(dp.Value), dp.Attributes})
				}
			case metricdata.Sum[float64]:
				for _, dp := range data.DataPoints {
					out = append(out, point{dp.Value, dp.Attributes})
				}
			}
		}
	}
	return out
}

// valueAt is the value of the point whose attribute key is value, or 0.
func valueAt(ps []point, key, value string) float64 {
	for _, p := range ps {
		if v, ok := p.attrs.Value(attribute.Key(key)); ok && v.AsString() == value {
			return p.value
		}
	}
	return 0
}

// A replayed charge or settle moved nothing, so the decorator counts nothing:
// counting it again would report spend twice.
func TestMeteringCountsNoReplay(t *testing.T) {
	usage, reader, records := meteredStore(t)
	ctx := context.Background()
	tenant, capID := uuid.New(), uuid.New()
	charge := limes.ChargeRequest{Subject: recordedSubject(ctx, t, records, subjectSpec{capID: capID, tenant: tenant}), Amount: limes.MustParseAmount("1"), ExternalRef: "call-1"}
	for range 2 {
		if _, err := usage.Charge(ctx, charge, nil); err != nil {
			t.Fatal(err)
		}
	}
	res, err := usage.Reserve(ctx, limes.ReserveRequest{Subject: recordedSubject(ctx, t, records, subjectSpec{capID: capID, tenant: tenant}), Amount: limes.MustParseAmount("1")})
	if err != nil {
		t.Fatal(err)
	}
	settle := limes.SettleRequest{Subject: recordedSubject(ctx, t, records, subjectSpec{capID: capID, tenant: tenant}), ReservationID: res.ID, Amount: limes.MustParseAmount("1")}
	for range 2 {
		if _, err := usage.Settle(ctx, settle, nil); err != nil {
			t.Fatal(err)
		}
	}
	if got := valueAt(points(t, reader, metricChargeDecisions), attrOutcome, "allowed"); got != 2 {
		t.Errorf("%v charges counted, want 2: one charge and one settle, no replay", got)
	}
}

// Every amount carries its unit, the settle's included, though its request
// names only the reservation; and every refused or failed attempt is
// counted under its outcome.
func TestMeteringLabelsAmountsAndOutcomes(t *testing.T) {
	usage, reader, records := meteredStore(t)
	ctx := context.Background()
	tenant, capID := uuid.New(), uuid.New()
	eur := func(s string) limes.ChargeRequest {
		return limes.ChargeRequest{Subject: recordedSubject(ctx, t, records, subjectSpec{capID: capID, tenant: tenant, budget: budgetOf(limes.MustParseAmount("5")), unit: "EUR"}), Amount: limes.MustParseAmount(s)}
	}
	r, err := usage.Charge(ctx, eur("2"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := usage.Charge(ctx, eur("9"), nil); !errors.Is(err, limes.ErrBudgetExceeded) {
		t.Fatalf("charge past the ceiling: %v", err)
	}
	over := eur("9")
	over.Overrun = limes.OverrunRecord
	if _, err := usage.Charge(ctx, over, nil); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("side effect failed")
	failing := eur("0.1")
	// A capability with room, so the callback runs.
	failing.Subject = recordedSubject(ctx, t, records, subjectSpec{capID: uuid.New(), tenant: tenant, budget: budgetOf(limes.MustParseAmount("5")), unit: "EUR"})
	if _, err := usage.Charge(ctx, failing, func(context.Context, struct{}) error { return boom }); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	held := recordedSubject(ctx, t, records, subjectSpec{capID: uuid.New(), tenant: tenant, unit: "EUR"})
	res, err := usage.Reserve(ctx, limes.ReserveRequest{Subject: held, Amount: limes.MustParseAmount("1")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := usage.Settle(ctx, limes.SettleRequest{Subject: held, ReservationID: res.ID, Amount: limes.MustParseAmount("1")}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := usage.Refund(ctx, limes.RefundRequest{ChargeID: r.ChargeID, Remaining: true}); err != nil {
		t.Fatal(err)
	}

	decisions := points(t, reader, metricChargeDecisions)
	for outcome, want := range map[string]float64{"allowed": 2, "budget_exceeded": 1, "overrun": 1, "error": 1} {
		if got := valueAt(decisions, attrOutcome, outcome); got != want {
			t.Errorf("%s charges = %v, want %v", outcome, got, want)
		}
	}
	// 2 charged, 9 recorded past the ceiling, 1 settled: all in EUR.
	if got := valueAt(points(t, reader, metricChargeAmount), attrUnit, "EUR"); got != 12 {
		t.Errorf("EUR charged = %v, want 12", got)
	}
	if got := valueAt(points(t, reader, metricRefundAmount), attrUnit, "EUR"); got != 2 {
		t.Errorf("EUR refunded = %v, want 2", got)
	}
	for _, name := range []string{metricChargeDecisions, metricChargeAmount, metricRefundAmount} {
		for _, p := range points(t, reader, name) {
			if p.attrs.HasValue(attribute.Key(attrTenant)) {
				t.Errorf("%s carries tenant_id without WithTenantAttribute", name)
			}
		}
	}
}

// WithTenantAttribute labels measurements with the tenant.
func TestMeteringTenantAttributeIsOptIn(t *testing.T) {
	usage, reader, records := meteredStore(t, limes.WithTenantAttribute())
	ctx := context.Background()
	tenant := uuid.New()
	if _, err := usage.Bump(ctx, limes.BumpRequest{Subject: recordedSubject(ctx, t, records, subjectSpec{capID: uuid.New(), tenant: tenant, requests: requestsOf(1)})}); err != nil {
		t.Fatal(err)
	}
	ps := points(t, reader, metricRequestBumps)
	if got := valueAt(ps, attrTenant, tenant.String()); got != 1 {
		t.Errorf("bumps for tenant %s = %v, want 1", tenant, got)
	}
}

// hiddenCopies is a UsageStore that reads no copy counters: memstore's
// usage store with CopyUsage out of reach.
type hiddenCopies struct {
	limes.UsageStore[struct{}]
}

// The decorator reads copy counters exactly when the store it wraps does.
func TestInstrumentKeepsCopyUsage(t *testing.T) {
	if _, err := limes.Instrument[struct{}](nil); err == nil {
		t.Fatal("Instrument(nil) wrapped nothing without an error")
	}
	records := newMemstoreRecords(t)
	inner, err := memstore.NewUsageStore(memstore.UsageStoreConfig[struct{}]{Records: records})
	if err != nil {
		t.Fatal(err)
	}
	wrap := func(u limes.UsageStore[struct{}]) limes.UsageStore[struct{}] {
		t.Helper()
		m, err := limes.Instrument(u)
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	if _, ok := wrap(inner).(limes.CopyUsageReader); !ok {
		t.Error("wrapping a store that reads copy counters hid them")
	}
	if _, ok := wrap(hiddenCopies{inner}).(limes.CopyUsageReader); ok {
		t.Error("wrapping a store that reads no copy counters claimed to read them")
	}

	ctx := context.Background()
	usage := wrap(inner)
	copyID := []byte("copy-1")
	copies := []limes.CopyCeiling{{RevocationID: copyID, Requests: limes.RequestCeiling{Max: 10}}}
	if _, err := usage.Bump(ctx, limes.BumpRequest{Subject: recordedSubject(ctx, t, records, subjectSpec{capID: uuid.New(), tenant: uuid.New(), copies: copies})}); err != nil {
		t.Fatal(err)
	}
	got, err := usage.(limes.CopyUsageReader).CopyUsage(ctx, [][]byte{copyID})
	if err != nil || len(got) != 1 || got[0].RequestCount != 1 {
		t.Errorf("CopyUsage through the decorator = %+v, %v; want one request counted", got, err)
	}
}

// Every method reaches the store the decorator wraps, and the ones that move
// a counter are measured.
func TestMeteringStorePassesEveryCallThrough(t *testing.T) {
	usage, reader, records := meteredStore(t)
	ctx := context.Background()
	tenant, capID := uuid.New(), uuid.New()
	// Expired long enough ago that Purge removes it, so its usage ends an
	// orphan; until then the Meter reads its lineage like any other.
	expired := limes.Capability{
		ID: capID, Subject: limes.Principal{Type: limes.PrincipalAgent, TenantID: tenant, Subject: "agent"},
		Caveats: limes.Caveats{
			Ops: []limes.Op{limes.OpGet}, Requests: limes.RequestCeiling{Unlimited: true}, Budget: limes.BudgetCeiling{Unlimited: true},
		},
		ExpiresAt: time.Now().Add(-limes.MinPurgeRetention - time.Hour),
	}
	if err := records.Insert(ctx, limes.InsertRequest{Capability: expired, IssuedBy: limes.Principal{Subject: "issuer"}}); err != nil {
		t.Fatal(err)
	}
	measured := func(name, metric string, call func() error) {
		t.Helper()
		before := len(points(t, reader, metric))
		if err := call(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(points(t, reader, metric)) == before && before == 0 {
			t.Errorf("%s took no measurement", name)
		}
	}

	if _, err := usage.SetTenantBudget(ctx, limes.SetTenantBudgetRequest{TenantID: tenant, Ceiling: limes.BudgetCeiling{Max: limes.MustParseAmount("10")}}); err != nil {
		t.Fatal(err)
	}
	measured("Bump", metricRequestBumps, func() error {
		_, err := usage.Bump(ctx, limes.BumpRequest{Subject: recordedSubject(ctx, t, records, subjectSpec{capID: capID, tenant: tenant})})
		return err
	})
	var receipt limes.ChargeReceipt
	measured("Charge", metricChargeAmount, func() (err error) {
		receipt, err = usage.Charge(ctx, limes.ChargeRequest{Subject: recordedSubject(ctx, t, records, subjectSpec{capID: capID, tenant: tenant}), Amount: limes.MustParseAmount("2"), ExternalRef: "r"}, nil)
		return err
	})
	measured("Refund", metricRefundAmount, func() error {
		_, err := usage.Refund(ctx, limes.RefundRequest{ChargeID: receipt.ChargeID, Amount: limes.MustParseAmount("1")})
		return err
	})
	var res limes.Reservation
	measured("Reserve", metricReservations, func() (err error) {
		res, err = usage.Reserve(ctx, limes.ReserveRequest{Subject: recordedSubject(ctx, t, records, subjectSpec{capID: capID, tenant: tenant}), Amount: limes.MustParseAmount("1")})
		return err
	})

	if got, err := usage.GetReservation(ctx, res.ID); err != nil || got.ID != res.ID {
		t.Errorf("GetReservation = %+v, %v", got, err)
	}
	if got, err := usage.ListReservations(ctx, limes.ListReservationsRequest{CapabilityID: capID}); err != nil || len(got.Items) != 1 || got.Items[0].ID != res.ID {
		t.Errorf("ListReservations = %+v, %v", got, err)
	}
	if err := usage.Release(ctx, limes.ReleaseRequest{ReservationID: res.ID}); err != nil {
		t.Errorf("Release: %v", err)
	}
	if u, err := usage.GetUsage(ctx, capID); err != nil || u.RequestCount != 1 || u.SpentAmount != limes.NanosPerUnit {
		t.Errorf("GetUsage = %+v, %v", u, err)
	}
	if b, err := usage.GetTenantBudget(ctx, tenant); err != nil || b.SpentAmount != limes.NanosPerUnit {
		t.Errorf("GetTenantBudget = %+v, %v", b, err)
	}
	if lPage, err := usage.ListTenantBudgets(ctx, limes.ListTenantBudgetsRequest{}); err != nil || len(lPage.Items) != 1 {
		t.Errorf("ListTenantBudgets = %+v, %v", lPage, err)
	}
	if c, err := usage.GetCharge(ctx, receipt.ChargeID); err != nil || c.RefundedAmount != limes.NanosPerUnit {
		t.Errorf("GetCharge = %+v, %v", c, err)
	}
	if c, err := usage.ChargeByRef(ctx, limes.ChargeByRefRequest{CapabilityID: capID, ExternalRef: "r"}); err != nil || c.ID != receipt.ChargeID {
		t.Errorf("ChargeByRef = %+v, %v", c, err)
	}
	if n, err := usage.ReleaseExpired(ctx, limes.ReleaseExpiredRequest{}); err != nil || n.Released != 0 {
		t.Errorf("ReleaseExpired = %d, %v", n, err)
	}
	if err := usage.DeleteUsage(ctx, limes.DeleteUsageRequest{CapabilityID: uuid.New()}); err != nil {
		t.Errorf("Delete of a row never written: %v", err)
	}
	if _, err := records.Purge(ctx, limes.PurgeRequest{}); err != nil {
		t.Fatal(err)
	}
	// The capability is no longer on record, so its usage is an orphan.
	if n, err := usage.PurgeOrphans(ctx, limes.PurgeOrphansRequest{}); err != nil || n.Purged != 1 {
		t.Errorf("PurgeOrphans = %d, %v; want the one orphan", n, err)
	}
	if _, err := usage.GetUsage(ctx, capID); err == nil {
		t.Error("GetUsage found a purged row")
	}
}

// A refused settle is counted under its tenant, as a refused charge is: the
// receipt of a refusal still names the tenant and the unit.
func TestMeteringLabelsARefusedSettleWithItsTenant(t *testing.T) {
	usage, reader, records := meteredStore(t, limes.WithTenantAttribute())
	ctx := context.Background()
	tenant, capID := uuid.New(), uuid.New()
	res, err := usage.Reserve(ctx, limes.ReserveRequest{Subject: recordedSubject(ctx, t, records, subjectSpec{capID: capID, tenant: tenant, budget: budgetOf(limes.MustParseAmount("2"))}), Amount: limes.MustParseAmount("1")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := usage.Settle(ctx, limes.SettleRequest{Subject: recordedSubject(ctx, t, records, subjectSpec{capID: capID, tenant: tenant, budget: budgetOf(limes.MustParseAmount("2"))}), ReservationID: res.ID, Amount: limes.MustParseAmount("5")}, nil); !errors.Is(err, limes.ErrBudgetExceeded) {
		t.Fatalf("settle past the ceiling: %v", err)
	}
	if got := valueAt(points(t, reader, metricChargeDecisions), attrTenant, tenant.String()); got != 1 {
		t.Errorf("refused settles under tenant %s = %v, want 1", tenant, got)
	}
}

// subjectSpec is a meter subject's parts.
type subjectSpec struct {
	capID  uuid.UUID
	tenant uuid.UUID
	// requests and budget are the subject's ceilings; nil, a check that
	// sets none, is Unlimited.
	requests *limes.RequestCeiling
	budget   *limes.BudgetCeiling
	unit     string
	copies   []limes.CopyCeiling
}

// recordedSubject records spec's capability as a root in records, unless it
// is already there, and returns its subject: the Meter meters only a
// capability on record.
func recordedSubject(ctx context.Context, t *testing.T, records *memstore.Store, spec subjectSpec) limes.MeterSubject {
	t.Helper()
	c := limes.Capability{
		ID: spec.capID, Subject: limes.Principal{Type: limes.PrincipalAgent, TenantID: spec.tenant, Subject: "agent"},
		Caveats: limes.Caveats{
			Ops: []limes.Op{limes.OpGet}, Requests: limes.RequestCeiling{Unlimited: true}, Budget: limes.BudgetCeiling{Unlimited: true},
		},
		ExpiresAt: time.Now().Add(time.Hour),
	}
	err := records.Insert(ctx, limes.InsertRequest{Capability: c, IssuedBy: limes.Principal{Subject: "issuer"}})
	if err != nil && !errors.Is(err, limes.ErrAlreadyExists) {
		t.Fatal(err)
	}
	return newSubject(spec)
}

// newSubject is the MeterSubject a verified capability with spec's parts
// would give, so a test can meter against ceilings no issuer signed.
func newSubject(spec subjectSpec) limes.MeterSubject {
	return link.NewMeterSubject(link.MeterSubjectFields{
		CapabilityID: spec.capID, TenantID: spec.tenant,
		Requests: spec.requestCeiling(), Budget: spec.budgetCeiling(), UnitCode: spec.unit, Copies: spec.copies,
	}).(limes.MeterSubject)
}

func (spec subjectSpec) requestCeiling() limes.RequestCeiling {
	if spec.requests == nil {
		return limes.RequestCeiling{Unlimited: true}
	}
	return *spec.requests
}

func (spec subjectSpec) budgetCeiling() limes.BudgetCeiling {
	if spec.budget == nil {
		return limes.BudgetCeiling{Unlimited: true}
	}
	return *spec.budget
}

// budgetOf and requestsOf are ceilings of n, for a spec to set.
func budgetOf(n limes.Nanos) *limes.BudgetCeiling { return &limes.BudgetCeiling{Max: n} }
func requestsOf(n int64) *limes.RequestCeiling    { return &limes.RequestCeiling{Max: n} }
