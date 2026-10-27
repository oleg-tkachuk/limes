package pgstore

// Benchmarks of the store's hot paths against the package's PostgreSQL.
//
// The weekly benchmark workflow runs this file on HEAD and, copied in, on the
// newest pgstore release, and compares the two with benchstat. It uses
// nothing but what the package's tests have held since that release: owner,
// app, ownerDSN, appUser, appPassword, emptyTables, meterSubject and
// testUnit. Each benchmark reports its allocations; the Parallel ones call
// the store from every processor at once over a pool big enough for all of
// them, on chains of their own or on one chain, whose row locks they then
// queue on. A helper added here instead
// would leave the release unable to compile it.

import (
	"runtime"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5/pgxpool"

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
	return newBenchFixtureOver(b, app)
}

// newBenchFixtureOver is a fixture whose stores use db.
func newBenchFixtureOver(b *testing.B, db *pgxpool.Pool) benchFixture {
	b.Helper()
	b.ReportAllocs()
	f := benchFixture{tenant: uuid.New()}
	var err error
	if f.records, err = NewStore(Config{DB: db}); err != nil {
		b.Fatal(err)
	}
	if f.usage, err = NewUsageStore(Config{DB: db}); err != nil {
		b.Fatal(err)
	}
	f.chain = f.newChain(b)
	return f
}

// newChain records a chain of benchChainDepth capabilities of f's tenant,
// root first.
func (f benchFixture) newChain(b *testing.B) []limes.Capability {
	b.Helper()
	return f.newChainOf(b, f.tenant)
}

// newChainOf records a chain of benchChainDepth capabilities of tenant,
// root first.
func (f benchFixture) newChainOf(b *testing.B, tenant uuid.UUID) []limes.Capability {
	b.Helper()
	ctx := WithTenant(b.Context(), tenant)
	// One instant for the whole chain: a child may not outlive its parent.
	expiry := time.Now().Add(benchTTL)
	var chain []limes.Capability
	parent := uuid.Nil()
	for i := range benchChainDepth {
		c := limes.Capability{
			ID: uuid.New(), ParentID: parent, ExpiresAt: expiry,
			Subject: limes.Principal{Type: limes.PrincipalTypeAgent, TenantID: tenant, Subject: "agent:" + strconv.Itoa(i)},
			Caveats: limes.Caveats{Ops: []limes.Op{limes.OpGet}, Requests: limes.RequestCeiling{Unlimited: true}, Budget: limes.BudgetCeiling{Unlimited: true, UnitCode: testUnit}},
		}
		if err := f.records.Insert(ctx, limes.InsertRequest{Capability: c}); err != nil {
			b.Fatal(err)
		}
		chain = append(chain, c)
		parent = c.ID
	}
	return chain
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

// benchPool is a pool as the app role with a connection for every processor
// and one to spare, for the Parallel benchmarks: the package's own app pool
// has two.
func benchPool(b *testing.B) *pgxpool.Pool {
	b.Helper()
	cfg, err := pgxpool.ParseConfig(ownerDSN)
	if err != nil {
		b.Fatal(err)
	}
	cfg.ConnConfig.User, cfg.ConnConfig.Password = appUser, appPassword
	cfg.MaxConns = int32(runtime.GOMAXPROCS(0) + 1)
	pool, err := pgxpool.NewWithConfig(b.Context(), cfg)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(pool.Close)
	return pool
}

// parallelLeaves runs op from every processor at once until the benchmark
// ends, each goroutine on the leaf of a chain of its own — of f's tenant, or
// with ownTenants of a tenant of its own — reporting the first error.
func parallelLeaves(b *testing.B, f benchFixture, ownTenants bool, op func(leaf limes.MeterSubject, n int) error) {
	b.Helper()
	leaves := make([]limes.MeterSubject, runtime.GOMAXPROCS(0))
	for i := range leaves {
		tenant := f.tenant
		if ownTenants {
			tenant = uuid.New()
		}
		chain := f.newChainOf(b, tenant)
		leaves[i] = meterSubject(chain[len(chain)-1].ID, tenant)
	}
	var next atomic.Int64
	b.RunParallel(func(pb *testing.PB) {
		leaf := leaves[int(next.Add(1)-1)%len(leaves)]
		n := 0
		for pb.Next() {
			n++
			if err := op(leaf, n); err != nil {
				b.Error(err)
				return
			}
		}
	})
}

// chargeLeaf charges leaf once, under its tenant, with a reference of its
// own.
func chargeLeaf(b *testing.B, f benchFixture, leaf limes.MeterSubject, n int) error {
	ctx := WithTenant(b.Context(), leaf.TenantID())
	_, err := f.usage.Charge(ctx, meter.ChargeRequest{Subject: leaf, Amount: 1, ExternalRef: leaf.CapabilityID().String() + "-" + strconv.Itoa(n)}, nil)
	return err
}

// Charges of one tenant on chains of their own: each still waits for the
// one before it, on the tenant's budget aggregate every charge locks.
func BenchmarkChargeParallel(b *testing.B) {
	f := newBenchFixtureOver(b, benchPool(b))
	parallelLeaves(b, f, false, func(leaf limes.MeterSubject, n int) error { return chargeLeaf(b, f, leaf, n) })
}

// Charges of tenants of their own: nothing is shared but the database.
func BenchmarkChargeParallelTenants(b *testing.B) {
	f := newBenchFixtureOver(b, benchPool(b))
	parallelLeaves(b, f, true, func(leaf limes.MeterSubject, n int) error { return chargeLeaf(b, f, leaf, n) })
}

// Charges on one chain: each waits for the one before it to commit, on the
// chain's counters.
func BenchmarkChargeParallelOneChain(b *testing.B) {
	f := newBenchFixtureOver(b, benchPool(b))
	ctx := WithTenant(b.Context(), f.tenant)
	var n atomic.Int64
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, err := f.usage.Charge(ctx, meter.ChargeRequest{Subject: f.leaf(), Amount: 1, ExternalRef: strconv.FormatInt(n.Add(1), 10)}, nil); err != nil {
				b.Error(err)
				return
			}
		}
	})
}

// Revocation checks read and take no lock: they scale with the pool.
func BenchmarkIsRevokedParallel(b *testing.B) {
	f := newBenchFixtureOver(b, benchPool(b))
	ctx := WithTenant(b.Context(), f.tenant)
	id := f.chain[len(f.chain)-1].ID
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, err := f.records.IsRevoked(ctx, id); err != nil {
				b.Error(err)
				return
			}
		}
	})
}
