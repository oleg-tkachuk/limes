package memstore

import (
	"context"
	"errors"
	"testing"
	"time"
	"uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/meter"
)

// cursorFuzzPage is the page size FuzzCursors asks for: one, so a store of
// two tenants' rows hands out a cursor for every listing.
const cursorFuzzPage = 1

// cursorFuzzAmount is what each tenant is charged and holds.
const cursorFuzzAmount = 3 * unit

// cursorFuzzTTL is how long a seeded hold lives.
const cursorFuzzTTL = time.Hour

// cursorFuzzPeriod is the period SumCharges sums: wide enough to hold
// every charge the fixture takes.
const cursorFuzzPeriod = 24 * time.Hour

// cursorFixture is two tenants' records, charges and holds in one store,
// and the readers that page them.
type cursorFixture struct {
	census *Census
	usage  *UsageStore
	ledger *Ledger
	// tenant is the tenant every scoped call names; other the one whose
	// rows it must never see.
	tenant, other uuid.UUID
	// capID is tenant's capability, which holds a reservation.
	capID uuid.UUID
	now   time.Time
}

func newCursorFixture(t testing.TB) cursorFixture {
	t.Helper()
	ctx := context.Background()
	records := newTestStore()
	usage := newTestUsage(records)
	f := cursorFixture{census: NewCensus(records), usage: usage, ledger: NewLedger(usage), tenant: uuid.New(), other: uuid.New(), now: usage.nowFn()}
	for _, tenant := range []uuid.UUID{f.tenant, f.other} {
		ceiling := limes.BudgetCeiling{Max: limes.MaxNanos, UnitCode: defaultTestUnit}
		if _, err := usage.SetTenantBudget(ctx, meter.SetTenantBudgetRequest{TenantID: tenant, Ceiling: ceiling}); err != nil {
			t.Fatal(err)
		}
		for range cursorFuzzPage + 1 {
			capID := uuid.New()
			subject := recordedSubject(ctx, t, usage, subjectSpec{capID: capID, tenant: tenant})
			if _, err := usage.Charge(ctx, meter.ChargeRequest{Subject: subject, Amount: cursorFuzzAmount}, nil); err != nil {
				t.Fatal(err)
			}
			if _, err := usage.Reserve(ctx, meter.ReserveRequest{Subject: subject, Amount: cursorFuzzAmount, TTL: cursorFuzzTTL}); err != nil {
				t.Fatal(err)
			}
			if tenant == f.tenant {
				f.capID = capID
			}
		}
	}
	return f
}

// cursorCall is one paged read, given a cursor: what it is, and the
// tenants of the rows it returned.
type cursorCall struct {
	name string
	read func(ctx context.Context, cursor string) (next string, tenants []uuid.UUID, err error)
	// scoped reads name f.tenant, and must return no row of another.
	scoped bool
}

// calls are every paged read of the store that takes a cursor a client
// hands back.
func (f cursorFixture) calls() []cursorCall {
	return []cursorCall{
		{name: "CountRecords", scoped: true, read: func(ctx context.Context, cursor string) (string, []uuid.UUID, error) {
			p, err := f.census.CountRecords(ctx, limes.CountRecordsRequest{TenantID: f.tenant, ByTenant: true, ByPrincipalType: true, Limit: cursorFuzzPage, Cursor: cursor})
			return p.Next, tenantsOf(p.Items, func(c limes.RecordCount) uuid.UUID { return c.TenantID }), err
		}},
		{name: "CountRecords of every tenant", read: func(ctx context.Context, cursor string) (string, []uuid.UUID, error) {
			p, err := f.census.CountRecords(ctx, limes.CountRecordsRequest{AllTenants: true, ByTenant: true, Limit: cursorFuzzPage, Cursor: cursor})
			return p.Next, tenantsOf(p.Items, func(c limes.RecordCount) uuid.UUID { return c.TenantID }), err
		}},
		{name: "ListRecords", scoped: true, read: func(ctx context.Context, cursor string) (string, []uuid.UUID, error) {
			p, err := f.census.ListRecords(ctx, limes.ListRecordsRequest{TenantID: f.tenant, Limit: cursorFuzzPage, Cursor: cursor})
			return p.Next, tenantsOf(p.Items, func(l limes.RecordListing) uuid.UUID { return l.Record.Capability.Subject.TenantID }), err
		}},
		{name: "SumCharges", scoped: true, read: func(ctx context.Context, cursor string) (string, []uuid.UUID, error) {
			p, err := f.ledger.SumCharges(ctx, meter.SumChargesRequest{
				TenantID: f.tenant, From: f.now.Add(-cursorFuzzPeriod), To: f.now.Add(cursorFuzzPeriod), Bucket: meter.ChargeBucketHour,
				GroupBy: []meter.ChargeDimension{meter.ChargeDimensionTenant, meter.ChargeDimensionCapability}, Limit: cursorFuzzPage, Cursor: cursor,
			})
			return p.Next, tenantsOf(p.Items, func(s meter.ChargeSum) uuid.UUID { return s.TenantID }), err
		}},
		{name: "ListTenantReservations", scoped: true, read: func(ctx context.Context, cursor string) (string, []uuid.UUID, error) {
			p, err := f.usage.ListTenantReservations(ctx, meter.ListTenantReservationsRequest{TenantID: f.tenant, Limit: cursorFuzzPage, Cursor: cursor})
			return p.Next, tenantsOf(p.Items, func(r meter.Reservation) uuid.UUID { return r.TenantID }), err
		}},
		{name: "ListReservations", scoped: true, read: func(ctx context.Context, cursor string) (string, []uuid.UUID, error) {
			p, err := f.usage.ListReservations(ctx, meter.ListReservationsRequest{CapabilityID: f.capID, Limit: cursorFuzzPage, Cursor: cursor})
			return p.Next, tenantsOf(p.Items, func(r meter.Reservation) uuid.UUID { return r.TenantID }), err
		}},
		{name: "ListTenantBudgets", read: func(ctx context.Context, cursor string) (string, []uuid.UUID, error) {
			p, err := f.usage.ListTenantBudgets(ctx, meter.ListTenantBudgetsRequest{Limit: cursorFuzzPage, Cursor: cursor})
			return p.Next, tenantsOf(p.Items, func(b meter.TenantBudget) uuid.UUID { return b.TenantID }), err
		}},
	}
}

func tenantsOf[T any](items []T, tenant func(T) uuid.UUID) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(items))
	for _, it := range items {
		out = append(out, tenant(it))
	}
	return out
}

// FuzzCursors hands every paged read a cursor the fuzzer writes, as a
// client may, and holds the store to the contract a cursor is read under:
// it is refused as limes.ErrInvalidRequest or limes.ErrCursorMismatch, a
// caller's error, never another one or a panic; and what one it accepts
// pages is still the tenant the request names, never a row of another.
func FuzzCursors(f *testing.F) {
	fx := newCursorFixture(f)
	ctx := context.Background()
	f.Add("")
	for _, c := range fx.calls() {
		next, _, err := c.read(ctx, "")
		if err != nil {
			f.Fatalf("%s: first page: %v", c.name, err)
		}
		if next != "" {
			f.Add(next)
		}
	}
	f.Fuzz(func(t *testing.T, cursor string) {
		for _, c := range fx.calls() {
			_, tenants, err := c.read(ctx, cursor)
			checkCursorAnswer(t, c.name, cursor, err)
			for _, got := range tenants {
				if c.scoped && got != fx.tenant {
					t.Fatalf("%s: cursor %q paged tenant %s's row for tenant %s", c.name, cursor, got, fx.tenant)
				}
			}
		}
	})
}

// checkCursorAnswer fails t when a read's error for cursor is not the
// caller's: a cursor is the client's input, so a store that cannot read one
// says so rather than failing as a server would.
func checkCursorAnswer(t *testing.T, call, cursor string, err error) {
	t.Helper()
	if err != nil && !errors.Is(err, limes.ErrInvalidRequest) && !errors.Is(err, limes.ErrCursorMismatch) {
		t.Fatalf("%s: cursor %q: %v, not a refusal of the cursor", call, cursor, err)
	}
}
