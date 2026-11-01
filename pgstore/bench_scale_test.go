package pgstore

// Benchmarks of how the hot paths scale with a delegation chain's depth and
// with the round trip to the database, which bench_test.go holds fixed: a
// chain of benchChainDepth, and a database on the same host.
//
// They run only with LIMES_BENCH_SCALE set (task bench:pgstore:scale): each
// latency step waits on every statement, so they take minutes, and the
// weekly comparison with the last release (bench.ts) has nothing to set them
// beside, as the release has no such file.

import (
	"context"
	"fmt"
	"net"
	"os"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/meter"
)

// scaleBenchEnv turns the scale benchmarks on.
const scaleBenchEnv = "LIMES_BENCH_SCALE"

// scaleDepths are the chain depths the depth benchmarks run at: a root
// alone, a few delegations, many, and limes.MaxDelegationDepth.
var scaleDepths = []int{1, 8, 32, limes.MaxDelegationDepth}

// scaleRTTs are the round trips to the database the latency benchmarks add:
// none, a database in the same zone, and one a zone away.
var scaleRTTs = []time.Duration{0, time.Millisecond, 2 * time.Millisecond}

// skipUnlessScale skips a scale benchmark unless scaleBenchEnv is set.
func skipUnlessScale(b *testing.B) {
	b.Helper()
	if os.Getenv(scaleBenchEnv) == "" {
		b.Skipf("%s not set", scaleBenchEnv)
	}
}

// chainOfDepth records a chain of depth capabilities of f's tenant, root
// first, and returns its leaf as a meter sees it, and the leaf's id.
func (f benchFixture) chainOfDepth(b *testing.B, depth int) (limes.MeterSubject, uuid.UUID) {
	b.Helper()
	ctx := WithTenant(b.Context(), f.tenant)
	expiry := time.Now().Add(benchTTL)
	parent := uuid.Nil()
	for i := range depth {
		c := limes.Capability{
			ID: uuid.New(), ParentID: parent, ExpiresAt: expiry,
			Subject: limes.Principal{Type: limes.PrincipalTypeAgent, TenantID: f.tenant, Subject: "agent:" + strconv.Itoa(i)},
			Caveats: limes.Caveats{Ops: []limes.Op{limes.OpGet}, Requests: limes.RequestCeiling{Unlimited: true}, Budget: limes.BudgetCeiling{Unlimited: true, UnitCode: testUnit}},
		}
		if err := f.records.Insert(ctx, limes.InsertRequest{Capability: c}); err != nil {
			b.Fatal(err)
		}
		parent = c.ID
	}
	return meterSubject(parent, f.tenant), parent
}

// Charge, Bump and IsRevoked on the leaf of a chain of each depth: Charge
// and Bump lock and write a counter for every capability of the chain, and
// IsRevoked walks it.
func BenchmarkChainDepth(b *testing.B) {
	skipUnlessScale(b)
	for _, depth := range scaleDepths {
		if _, err := owner.Exec(b.Context(), emptyTables); err != nil {
			b.Fatalf("empty the tables: %v", err)
		}
		pool, sent := latencyPool(b, 0)
		f := newBenchFixtureOver(b, pool).leasing(b, pool)
		leaf, id := f.chainOfDepth(b, depth)
		ctx := WithTenant(b.Context(), f.tenant)
		b.Run(fmt.Sprintf("op=Charge/depth=%d", depth), func(b *testing.B) {
			n := 0
			defer reportRoundTrips(b, sent)()
			for b.Loop() {
				n++
				if _, err := f.usage.Charge(ctx, meter.ChargeRequest{Subject: leaf, Amount: 1, ExternalRef: strconv.Itoa(n)}, nil); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(fmt.Sprintf("op=Bump/depth=%d", depth), func(b *testing.B) {
			defer reportRoundTrips(b, sent)()
			for b.Loop() {
				if _, err := f.usage.Bump(ctx, meter.BumpRequest{Subject: leaf}); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(fmt.Sprintf("op=IsRevoked/depth=%d", depth), func(b *testing.B) {
			defer reportRoundTrips(b, sent)()
			for b.Loop() {
				if _, err := f.records.IsRevoked(ctx, id); err != nil {
					b.Fatal(err)
				}
			}
		})
		// Under a lease of its own the leaf counts only itself, at any depth.
		f.leaseAll(b, leaf)
		b.Run(fmt.Sprintf("op=ChargeLeased/depth=%d", depth), func(b *testing.B) {
			n := 0
			defer reportRoundTrips(b, sent)()
			for b.Loop() {
				n++
				if _, err := f.usage.Charge(ctx, meter.ChargeRequest{Subject: leaf, Amount: 1, ExternalRef: strconv.Itoa(n)}, nil); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(fmt.Sprintf("op=BumpLeased/depth=%d", depth), func(b *testing.B) {
			defer reportRoundTrips(b, sent)()
			for b.Loop() {
				if _, err := f.usage.Bump(ctx, meter.BumpRequest{Subject: leaf}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// benchSubAgents is how many sub-agents of one root charge at once in
// BenchmarkOneRoot: goroutines, each waiting on its own transaction, so they
// contend on the database whatever GOMAXPROCS is.
const benchSubAgents = 8

// Charge and Bump from benchSubAgents sub-agents at once, each a child of
// its own under one root, as an orchestrator's are: without leases every
// call locks the root's counters and they queue there; with each child
// under a lease of its own none does (docs/adr/0017-budget-leases.md). Over
// each of scaleRTTs.
func BenchmarkOneRoot(b *testing.B) {
	skipUnlessScale(b)
	for _, rtt := range scaleRTTs {
		for _, leased := range []bool{false, true} {
			pool, _ := latencyPoolOf(b, rtt, benchSubAgents+1)
			f := newBenchFixtureOver(b, pool).leasing(b, pool)
			children := f.childrenOfOneRoot(b, benchSubAgents)
			if leased {
				f.leaseAll(b, children...)
			}
			var seq atomic.Int64
			b.Run(fmt.Sprintf("op=Charge/leased=%t/rtt=%s", leased, rtt), func(b *testing.B) {
				concurrently(b, children, func(leaf limes.MeterSubject) error {
					return chargeLeaf(b, f, leaf, int(seq.Add(1)))
				})
			})
			b.Run(fmt.Sprintf("op=Bump/leased=%t/rtt=%s", leased, rtt), func(b *testing.B) {
				concurrently(b, children, func(leaf limes.MeterSubject) error {
					_, err := f.usage.Bump(WithTenant(b.Context(), f.tenant), meter.BumpRequest{Subject: leaf})
					return err
				})
			})
		}
	}
}

// childrenOfOneRoot records a root of f's tenant and n children of it, and
// returns the children as a meter sees them.
func (f benchFixture) childrenOfOneRoot(b *testing.B, n int) []limes.MeterSubject {
	b.Helper()
	ctx := WithTenant(b.Context(), f.tenant)
	expiry := time.Now().Add(benchTTL)
	caveats := limes.Caveats{Ops: []limes.Op{limes.OpGet}, Requests: limes.RequestCeiling{Unlimited: true}, Budget: limes.BudgetCeiling{Unlimited: true, UnitCode: testUnit}}
	root := limes.Capability{
		ID: uuid.New(), ExpiresAt: expiry, Caveats: caveats,
		Subject: limes.Principal{Type: limes.PrincipalTypeAgent, TenantID: f.tenant, Subject: "agent:root"},
	}
	if err := f.records.Insert(ctx, limes.InsertRequest{Capability: root}); err != nil {
		b.Fatal(err)
	}
	children := make([]limes.MeterSubject, n)
	for i := range children {
		c := limes.Capability{
			ID: uuid.New(), ParentID: root.ID, ExpiresAt: expiry, Caveats: caveats,
			Subject: limes.Principal{Type: limes.PrincipalTypeAgent, TenantID: f.tenant, Subject: "agent:" + strconv.Itoa(i)},
		}
		if err := f.records.Insert(ctx, limes.InsertRequest{Capability: c}); err != nil {
			b.Fatal(err)
		}
		children[i] = meterSubject(c.ID, f.tenant)
	}
	return children
}

// concurrently runs op b.N times in all, from a goroutine for each of
// leaves, each on its own leaf, reporting the first error.
func concurrently(b *testing.B, leaves []limes.MeterSubject, op func(leaf limes.MeterSubject) error) {
	b.Helper()
	var left atomic.Int64
	left.Store(int64(b.N))
	var wg sync.WaitGroup
	b.ResetTimer()
	for _, leaf := range leaves {
		wg.Go(func() {
			for left.Add(-1) >= 0 {
				if err := op(leaf); err != nil {
					b.Error(err)
					return
				}
			}
		})
	}
	wg.Wait()
}

// benchLeaseRequests is what each leaf of the leased benchmarks leases in
// requests: past any run's bumps.
const benchLeaseRequests = int64(1) << 50

// leaseAll leases each of leaves benchLease and benchLeaseRequests, a lease
// of its own.
func (f benchFixture) leaseAll(b *testing.B, leaves ...limes.MeterSubject) {
	b.Helper()
	ctx := WithTenant(b.Context(), f.tenant)
	for _, leaf := range leaves {
		if _, err := f.usage.Lease(ctx, meter.LeaseRequest{Subject: leaf, Amount: benchLease, Requests: benchLeaseRequests, TTL: benchTTL}); err != nil {
			b.Fatal(err)
		}
	}
}

// Charge one at a time, Charge from every processor on chains of one
// tenant — which queue on the tenant's aggregate — and IsRevoked, each over
// a database whose round trip is each of scaleRTTs longer than the host's.
func BenchmarkRoundTrip(b *testing.B) {
	skipUnlessScale(b)
	for _, rtt := range scaleRTTs {
		pool, sent := latencyPool(b, rtt)
		f := newBenchFixtureOver(b, pool).leasing(b, pool)
		ctx := WithTenant(b.Context(), f.tenant)
		b.Run(fmt.Sprintf("op=Charge/rtt=%s", rtt), func(b *testing.B) {
			n := 0
			defer reportRoundTrips(b, sent)()
			for b.Loop() {
				n++
				if _, err := f.usage.Charge(ctx, meter.ChargeRequest{Subject: f.leaf(), Amount: 1, ExternalRef: strconv.Itoa(n)}, nil); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(fmt.Sprintf("op=ChargeParallel/rtt=%s", rtt), func(b *testing.B) {
			parallelLeaves(b, f, false, func(leaf limes.MeterSubject, n int) error { return chargeLeaf(b, f, leaf, n) })
		})
		b.Run(fmt.Sprintf("op=ChargeLeasedParallel/rtt=%s", rtt), func(b *testing.B) {
			parallelLeasedLeaves(b, f)
		})
		b.Run(fmt.Sprintf("op=IsRevoked/rtt=%s", rtt), func(b *testing.B) {
			id := f.chain[len(f.chain)-1].ID
			defer reportRoundTrips(b, sent)()
			for b.Loop() {
				if _, err := f.records.IsRevoked(ctx, id); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// reportRoundTrips starts counting what the client sends through the
// proxy, and returns the func that reports it per operation, as
// roundtrips/op: each chunk the client sends waits for an answer, so a
// chunk is a round trip.
func reportRoundTrips(b *testing.B, sent *atomic.Int64) func() {
	start := sent.Load()
	return func() { b.ReportMetric(float64(sent.Load()-start)/float64(b.N), "roundtrips/op") }
}

// latencyPool is benchPool through a proxy that holds each chunk of the
// conversation for half of rtt each way, so every round trip to the
// database is rtt longer, and the count of chunks the client has sent.
func latencyPool(b testing.TB, rtt time.Duration) (*pgxpool.Pool, *atomic.Int64) {
	b.Helper()
	return latencyPoolOf(b, rtt, int32(runtime.GOMAXPROCS(0)+1))
}

// latencyPoolOf is latencyPool with at most conns connections.
func latencyPoolOf(b testing.TB, rtt time.Duration, conns int32) (*pgxpool.Pool, *atomic.Int64) {
	b.Helper()
	cfg, err := pgxpool.ParseConfig(ownerDSN)
	if err != nil {
		b.Fatal(err)
	}
	cfg.ConnConfig.User, cfg.ConnConfig.Password = appUser, appPassword
	cfg.MaxConns = conns
	sent := new(atomic.Int64)
	addr := delayProxy(b, net.JoinHostPort(cfg.ConnConfig.Host, strconv.Itoa(int(cfg.ConnConfig.Port))), rtt/2, sent)
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		b.Fatal(err)
	}
	p, err := strconv.ParseUint(port, 10, 16)
	if err != nil {
		b.Fatal(err)
	}
	cfg.ConnConfig.Host, cfg.ConnConfig.Port = host, uint16(p)
	pool, err := pgxpool.NewWithConfig(b.Context(), cfg)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(pool.Close)
	return pool, sent
}

// delayProxy listens on the loopback and forwards each connection to
// target, holding every chunk read for oneWay before writing it on, in each
// direction, and counting in sent the chunks clients send; it stops when the
// benchmark ends.
func delayProxy(b testing.TB, target string, oneWay time.Duration, sent *atomic.Int64) string {
	b.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		cancel()
		b.Fatal(err)
	}
	var wg sync.WaitGroup
	var conns sync.Map
	var nextConn atomic.Int64
	b.Cleanup(func() {
		cancel()
		_ = ln.Close()
		conns.Range(func(_, c any) bool { _ = c.(net.Conn).Close(); return true })
		wg.Wait()
	})
	wg.Go(func() {
		for {
			client, err := ln.Accept()
			if err != nil {
				return
			}
			var d net.Dialer
			server, err := d.DialContext(ctx, "tcp", target)
			if err != nil {
				_ = client.Close()
				continue
			}
			id := nextConn.Add(1)
			conns.Store(id*2, client)
			conns.Store(id*2+1, server)
			wg.Go(func() { delayCopy(server, client, oneWay, sent) })
			wg.Go(func() { delayCopy(client, server, oneWay, nil) })
		}
	})
	return ln.Addr().String()
}

// proxyChunk is the most a proxy reads at once, and proxyInFlight how many
// chunks a direction holds while they wait out the delay.
const (
	proxyChunk    = 32 << 10
	proxyInFlight = 64
)

// delayedChunk is bytes read, and when they are due at the other end.
type delayedChunk struct {
	data []byte
	due  time.Time
}

// delayCopy copies src to dst, each chunk delivered oneWay after it was
// read, as a link of that latency delivers it: chunks read close together
// arrive close together, not each a delay after the one before. It counts
// each chunk in read, when not nil, and ends, closing both, when either end
// closes.
func delayCopy(dst, src net.Conn, oneWay time.Duration, read *atomic.Int64) {
	defer func() { _ = dst.Close(); _ = src.Close() }()
	chunks := make(chan delayedChunk, proxyInFlight)
	go func() {
		defer close(chunks)
		for {
			buf := make([]byte, proxyChunk)
			n, err := src.Read(buf)
			if n > 0 {
				if read != nil {
					read.Add(1)
				}
				chunks <- delayedChunk{data: buf[:n], due: time.Now().Add(oneWay)}
			}
			if err != nil {
				return
			}
		}
	}()
	for c := range chunks {
		if wait := time.Until(c.due); wait > 0 {
			time.Sleep(wait)
		}
		if _, err := dst.Write(c.data); err != nil {
			return
		}
	}
}

// leasing is f with a usage store over db that leases (Config.Leases):
// bench_test.go's fixture keeps to the Config the newest release has.
func (f benchFixture) leasing(b *testing.B, db *pgxpool.Pool) benchFixture {
	b.Helper()
	var err error
	if f.usage, err = NewUsageStore(Config{DB: db, Leases: true}); err != nil {
		b.Fatal(err)
	}
	return f
}

// benchLease is what each leaf of the leased benchmark leases: past any
// run's charges.
const benchLease = limes.Nanos(1) << 50

// parallelLeasedLeaves is parallelLeaves's charges of one tenant, each leaf
// under a lease of its own: they queue on no row they share
// (docs/adr/0017-budget-leases.md).
func parallelLeasedLeaves(b *testing.B, f benchFixture) {
	b.Helper()
	leaves := make([]limes.MeterSubject, runtime.GOMAXPROCS(0))
	ctx := WithTenant(b.Context(), f.tenant)
	for i := range leaves {
		chain := f.newChainOf(b, f.tenant)
		leaves[i] = meterSubject(chain[len(chain)-1].ID, f.tenant)
		if _, err := f.usage.Lease(ctx, meter.LeaseRequest{Subject: leaves[i], Amount: benchLease, TTL: benchTTL}); err != nil {
			b.Fatal(err)
		}
	}
	var next atomic.Int64
	b.RunParallel(func(pb *testing.PB) {
		leaf := leaves[int(next.Add(1)-1)%len(leaves)]
		n := 0
		for pb.Next() {
			n++
			if err := chargeLeaf(b, f, leaf, n); err != nil {
				b.Error(err)
				return
			}
		}
	})
}
