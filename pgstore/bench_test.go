package pgstore

// Benchmarks of the store's hot paths against the package's PostgreSQL.
//
// The weekly benchmark workflow runs this file on HEAD and, copied in, on the
// newest pgstore release, and compares the two with benchstat. It uses
// nothing but what the package's tests have held since that release: owner,
// app, emptyTables, meterSubject and testUnit. A helper added here instead
// would leave the release unable to compile it.

import (
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/meter"
)

// benchTTL is how long the benchmarks' capabilities live: past any run.
const benchTTL = 24 * time.Hour

// benchChainDepth is how deep the chain a revocation check walks is.
const benchChainDepth = 3

// benchFixture is the stores over the app role and a chain of capabilities
// of one tenant, root first, the leaf last.
type benchFixture struct {
	records *Store
	usage   *UsageStore
	tenant  uuid.UUID
	chain   []limes.Capability
}

func newBenchFixture(b *testing.B) benchFixture {
	b.Helper()
	if _, err := owner.Exec(b.Context(), emptyTables); err != nil {
		b.Fatalf("empty the tables: %v", err)
	}
	f := benchFixture{tenant: uuid.New()}
	var err error
	if f.records, err = NewStore(Config{DB: app}); err != nil {
		b.Fatal(err)
	}
	if f.usage, err = NewUsageStore(Config{DB: app}); err != nil {
		b.Fatal(err)
	}
	ctx := WithTenant(b.Context(), f.tenant)
	// One instant for the whole chain: a child may not outlive its parent.
	expiry := time.Now().Add(benchTTL)
	parent := uuid.Nil
	for i := range benchChainDepth {
		c := limes.Capability{
			ID: uuid.New(), ParentID: parent, ExpiresAt: expiry,
			Subject: limes.Principal{Type: limes.PrincipalTypeAgent, TenantID: f.tenant, Subject: "agent:" + strconv.Itoa(i)},
			Caveats: limes.Caveats{Ops: []limes.Op{limes.OpGet}, Requests: limes.RequestCeiling{Unlimited: true}, Budget: limes.BudgetCeiling{Unlimited: true, UnitCode: testUnit}},
		}
		if err := f.records.Insert(ctx, limes.InsertRequest{Capability: c}); err != nil {
			b.Fatal(err)
		}
		f.chain = append(f.chain, c)
		parent = c.ID
	}
	return f
}

// leaf is the deepest capability of the chain, as a meter sees it.
func (f benchFixture) leaf() limes.MeterSubject {
	return meterSubject(f.chain[len(f.chain)-1].ID, f.tenant)
}

func BenchmarkCharge(b *testing.B) {
	f := newBenchFixture(b)
	ctx := WithTenant(b.Context(), f.tenant)
	n := 0
	for b.Loop() {
		n++
		if _, err := f.usage.Charge(ctx, meter.ChargeRequest{Subject: f.leaf(), Amount: 1, ExternalRef: strconv.Itoa(n)}, nil); err != nil {
			b.Fatal(err)
		}
	}
}

// A delivery of a charge already on record: the replay path.
func BenchmarkChargeReplayed(b *testing.B) {
	f := newBenchFixture(b)
	ctx := WithTenant(b.Context(), f.tenant)
	req := meter.ChargeRequest{Subject: f.leaf(), Amount: 1, ExternalRef: "replayed"}
	if _, err := f.usage.Charge(ctx, req, nil); err != nil {
		b.Fatal(err)
	}
	for b.Loop() {
		if _, err := f.usage.Charge(ctx, req, nil); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkReserveSettle(b *testing.B) {
	f := newBenchFixture(b)
	ctx := WithTenant(b.Context(), f.tenant)
	for b.Loop() {
		r, err := f.usage.Reserve(ctx, meter.ReserveRequest{Subject: f.leaf(), Amount: 1})
		if err != nil {
			b.Fatal(err)
		}
		if _, err := f.usage.Settle(ctx, meter.SettleRequest{Subject: f.leaf(), ReservationID: r.ID, Amount: 1}, nil); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkBump(b *testing.B) {
	f := newBenchFixture(b)
	ctx := WithTenant(b.Context(), f.tenant)
	for b.Loop() {
		if _, err := f.usage.Bump(ctx, meter.BumpRequest{Subject: f.leaf()}); err != nil {
			b.Fatal(err)
		}
	}
}

// A revocation check of the leaf walks the whole chain.
func BenchmarkIsRevoked(b *testing.B) {
	f := newBenchFixture(b)
	ctx := WithTenant(b.Context(), f.tenant)
	id := f.chain[len(f.chain)-1].ID
	for b.Loop() {
		if _, err := f.records.IsRevoked(ctx, id); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkGetUsage(b *testing.B) {
	f := newBenchFixture(b)
	ctx := WithTenant(b.Context(), f.tenant)
	if _, err := f.usage.Charge(ctx, meter.ChargeRequest{Subject: f.leaf(), Amount: 1}, nil); err != nil {
		b.Fatal(err)
	}
	id := f.chain[len(f.chain)-1].ID
	for b.Loop() {
		if _, err := f.usage.GetUsage(ctx, id); err != nil {
			b.Fatal(err)
		}
	}
}
