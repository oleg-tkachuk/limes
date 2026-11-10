package pgstore

import (
	"context"
	"errors"
	"testing"
	"time"
	"uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/meter"
)

// cursorFuzzPage is the page size FuzzCursors asks for: one, so two rows
// of each tenant hand out a cursor for every listing.
const cursorFuzzPage = 1

// cursorFuzzRows is how many capabilities, charges and holds each tenant
// gets: one more than a page.
const cursorFuzzRows = cursorFuzzPage + 1

// cursorFuzzAmount is what each capability is charged and holds.
const cursorFuzzAmount limes.Nanos = 3

// cursorFuzzTTL is how long a seeded capability and hold live.
const cursorFuzzTTL = time.Hour

// cursorFuzzPeriod is the period SumCharges sums, each side of now: wide
// enough to hold every charge the fixture takes.
const cursorFuzzPeriod = 24 * time.Hour

// cursorFuzzNUL is the one character a JSON string holds and PostgreSQL
// text does not.
const cursorFuzzNUL = "\x00"

// cursorFixture is two tenants' records, charges and holds, and the readers
// that page them, as the app role reads them.
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

// newCursorFixture writes its rows under tenants of its own, so it needs no
// empty tables and leaves the other tests' rows alone.
func newCursorFixture(t testing.TB) cursorFixture {
	t.Helper()
	ctx := context.Background()
	clock := newFakeClock()
	f := cursorFixture{tenant: uuid.New(), other: uuid.New(), now: clock.Now()}
	var err error
	if f.census, err = NewCensus(Config{DB: app, Now: clock.Now}); err != nil {
		t.Fatal(err)
	}
	records, err := NewStore(Config{DB: app, Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	if f.usage, err = NewUsageStore(Config{DB: app, Now: clock.Now}); err != nil {
		t.Fatal(err)
	}
	if f.ledger, err = NewLedger(Config{DB: app, Now: clock.Now}); err != nil {
		t.Fatal(err)
	}
	for _, tenant := range []uuid.UUID{f.tenant, f.other} {
		scoped := WithTenant(ctx, tenant)
		ceiling := limes.BudgetCeiling{Max: limes.MaxNanos, UnitCode: testUnit}
		if _, err := f.usage.SetTenantBudget(scoped, meter.SetTenantBudgetRequest{TenantID: tenant, Ceiling: ceiling}); err != nil {
			t.Fatal(err)
		}
		for range cursorFuzzRows {
			c := limes.Capability{
				ID: uuid.New(), ExpiresAt: f.now.Add(cursorFuzzTTL),
				Subject: limes.Principal{Type: limes.PrincipalTypeAgent, TenantID: tenant, Subject: "agent"},
				Caveats: limes.Caveats{Ops: []limes.Op{limes.OpGet}, Requests: limes.RequestCeiling{Unlimited: true}, Budget: limes.BudgetCeiling{Unlimited: true, UnitCode: testUnit}},
			}
			if err := records.Insert(scoped, limes.InsertRequest{Capability: c}); err != nil {
				t.Fatal(err)
			}
			subject := meterSubject(c.ID, tenant)
			if _, err := f.usage.Charge(scoped, meter.ChargeRequest{Subject: subject, Amount: cursorFuzzAmount}, nil); err != nil {
				t.Fatal(err)
			}
			if _, err := f.usage.Reserve(scoped, meter.ReserveRequest{Subject: subject, Amount: cursorFuzzAmount, TTL: cursorFuzzTTL}); err != nil {
				t.Fatal(err)
			}
			if tenant == f.tenant {
				f.capID = c.ID
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
// hands back, each under the scope it needs.
func (f cursorFixture) calls() []cursorCall {
	mine := func(ctx context.Context) context.Context { return WithTenant(ctx, f.tenant) }
	return []cursorCall{
		{name: "CountRecords", scoped: true, read: func(ctx context.Context, cursor string) (string, []uuid.UUID, error) {
			p, err := f.census.CountRecords(mine(ctx), limes.CountRecordsRequest{TenantID: f.tenant, ByTenant: true, ByPrincipalType: true, Limit: cursorFuzzPage, Cursor: cursor})
			return p.Next, tenantsOf(p.Items, func(c limes.RecordCount) uuid.UUID { return c.TenantID }), err
		}},
		{name: "CountRecords of every tenant", read: func(ctx context.Context, cursor string) (string, []uuid.UUID, error) {
			p, err := f.census.CountRecords(WithAllTenantsReadOnly(ctx), limes.CountRecordsRequest{AllTenants: true, ByTenant: true, Limit: cursorFuzzPage, Cursor: cursor})
			return p.Next, tenantsOf(p.Items, func(c limes.RecordCount) uuid.UUID { return c.TenantID }), err
		}},
		{name: "ListRecords", scoped: true, read: func(ctx context.Context, cursor string) (string, []uuid.UUID, error) {
			p, err := f.census.ListRecords(mine(ctx), limes.ListRecordsRequest{TenantID: f.tenant, Limit: cursorFuzzPage, Cursor: cursor})
			return p.Next, tenantsOf(p.Items, func(l limes.RecordListing) uuid.UUID { return l.Record.Capability.Subject.TenantID }), err
		}},
		{name: "SumCharges", scoped: true, read: func(ctx context.Context, cursor string) (string, []uuid.UUID, error) {
			p, err := f.ledger.SumCharges(mine(ctx), meter.SumChargesRequest{
				TenantID: f.tenant, From: f.now.Add(-cursorFuzzPeriod), To: f.now.Add(cursorFuzzPeriod), Bucket: meter.ChargeBucketHour,
				GroupBy: []meter.ChargeDimension{meter.ChargeDimensionTenant, meter.ChargeDimensionCapability}, Limit: cursorFuzzPage, Cursor: cursor,
			})
			return p.Next, tenantsOf(p.Items, func(s meter.ChargeSum) uuid.UUID { return s.TenantID }), err
		}},
		{name: "ListTenantReservations", scoped: true, read: func(ctx context.Context, cursor string) (string, []uuid.UUID, error) {
			p, err := f.usage.ListTenantReservations(mine(ctx), meter.ListTenantReservationsRequest{TenantID: f.tenant, Limit: cursorFuzzPage, Cursor: cursor})
			return p.Next, tenantsOf(p.Items, func(r meter.Reservation) uuid.UUID { return r.TenantID }), err
		}},
		{name: "ListReservations", scoped: true, read: func(ctx context.Context, cursor string) (string, []uuid.UUID, error) {
			p, err := f.usage.ListReservations(mine(ctx), meter.ListReservationsRequest{CapabilityID: f.capID, Limit: cursorFuzzPage, Cursor: cursor})
			return p.Next, tenantsOf(p.Items, func(r meter.Reservation) uuid.UUID { return r.TenantID }), err
		}},
		{name: "ListTenantBudgets", read: func(ctx context.Context, cursor string) (string, []uuid.UUID, error) {
			p, err := f.usage.ListTenantBudgets(WithAllTenantsReadOnly(ctx), meter.ListTenantBudgetsRequest{Limit: cursorFuzzPage, Cursor: cursor})
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
// caller's error, never one PostgreSQL raises over a value it cannot store
// or a panic; and what one it accepts pages is still the tenant the request
// names, never a row of another.
func FuzzCursors(f *testing.F) {
	fx := newCursorFixture(f)
	ctx := context.Background()
	f.Add("")
	// A NUL is text a JSON cursor can carry and a PostgreSQL text column
	// cannot: once passed to SQL as a parameter, it failed the statement.
	f.Add(countCursor{Tenant: fx.tenant, Type: cursorFuzzNUL}.encode())
	f.Add(sumCursor{Tenant: fx.tenant, Unit: cursorFuzzNUL, Op: cursorFuzzNUL, Actor: cursorFuzzNUL}.encode())
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
			if err != nil && !errors.Is(err, limes.ErrInvalidRequest) && !errors.Is(err, limes.ErrCursorMismatch) {
				t.Fatalf("%s: cursor %q: %v, not a refusal of the cursor", c.name, cursor, err)
			}
			for _, got := range tenants {
				if c.scoped && got != fx.tenant {
					t.Fatalf("%s: cursor %q paged tenant %s's row for tenant %s", c.name, cursor, got, fx.tenant)
				}
			}
		}
	})
}
