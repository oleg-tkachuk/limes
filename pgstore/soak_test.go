//go:build soak

package pgstore

// A soak of the store: workers charge, refund, reserve, settle, release and
// count against one chain of capabilities for a while, beside a housekeeper
// that releases expired holds, on the real clock and a pool big enough for
// all of them. At the end every counter must agree with what the workers
// did, no hold may be left, the root's ceiling must have held, and the pool
// must have every connection back.
//
// Run with the soak tag; soakDurationEnv sets how long (task
// test:pgstore:soak, the weekly benchmark workflow).

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/internal/link"
	"github.com/oleg-tkachuk/limes/meter"
)

const (
	// soakDurationEnv names how long the soak runs, as time.ParseDuration
	// reads it; unset, soakDefaultDuration.
	soakDurationEnv     = "LIMES_SOAK_DURATION"
	soakDefaultDuration = 20 * time.Second
	// soakWorkers is how many goroutines call the store at once, and
	// soakPoolConns the pool they share: one more than them, so a call
	// that needs a second connection waits rather than deadlocks.
	soakWorkers   = 6
	soakPoolConns = soakWorkers + 1
	// soakRootCeiling bounds the root's spend, the chain's together, so the
	// refusal path runs; soakMaxAmount bounds one charge or hold.
	soakRootCeiling limes.Nanos = 50_000
	soakMaxAmount   limes.Nanos = 100
	// soakHoldTTL is how long an abandoned hold lives before the
	// housekeeper may release it; soakSweepEvery, how often it looks.
	soakHoldTTL    = 2 * time.Second
	soakSweepEvery = time.Second
	// soakCapTTL outlives any soak.
	soakCapTTL = 24 * time.Hour
)

// soakOp is one thing a worker does.
type soakOp int

const (
	opCharge soakOp = iota
	opChargeReplayed
	opRefund
	opReserveSettle
	opReserveRelease
	opReserveAbandon
	opBump
)

// soakOps is how many kinds of soakOp there are.
const soakOps = int(opBump) + 1

// soakTally is what the workers did, by capability: the spend each moved
// on its own counters, and how many calls of each kind and refusals ran.
type soakTally struct {
	mu      sync.Mutex
	own     map[uuid.UUID]limes.Nanos
	calls   [soakOps]atomic.Int64
	refused atomic.Int64
}

func (t *soakTally) add(id uuid.UUID, amount limes.Nanos) {
	t.mu.Lock()
	t.own[id] += amount
	t.mu.Unlock()
}

// soakDuration is how long this soak runs.
func soakDuration(t *testing.T) time.Duration {
	v := os.Getenv(soakDurationEnv)
	if v == "" {
		return soakDefaultDuration
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		t.Fatalf("%s=%q is not a positive duration", soakDurationEnv, v)
	}
	return d
}

// soakSubject is capability id of tenant with ceiling, as a verified
// capability gives it.
func soakSubject(id, tenant uuid.UUID, ceiling limes.BudgetCeiling) limes.MeterSubject {
	return link.NewMeterSubject(link.MeterSubjectFields{
		CapabilityID: id, TenantID: tenant,
		Requests: limes.RequestCeiling{Unlimited: true}, Budget: ceiling,
	}).(limes.MeterSubject)
}

func TestSoak(t *testing.T) {
	duration := soakDuration(t)
	fresh(t)
	tenant := uuid.New()
	ctx := WithTenant(t.Context(), tenant)

	cfg, err := pgxpool.ParseConfig(ownerDSN)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.User, cfg.ConnConfig.Password = appUser, appPassword
	cfg.MaxConns = soakPoolConns
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	records, err := NewStore(Config{DB: pool})
	if err != nil {
		t.Fatal(err)
	}
	usage, err := NewUsageStore(Config{DB: pool})
	if err != nil {
		t.Fatal(err)
	}

	// The chain, root first: the root bounded, its descendants not, so
	// every charge of the chain meets the root's ceiling.
	rootCeiling := limes.BudgetCeiling{Max: soakRootCeiling, UnitCode: testUnit}
	unlimited := limes.BudgetCeiling{Unlimited: true, UnitCode: testUnit}
	expiry := time.Now().Add(soakCapTTL)
	var chain []uuid.UUID
	var subjects []limes.MeterSubject
	parent := uuid.Nil
	for i, ceiling := range []limes.BudgetCeiling{rootCeiling, unlimited, unlimited} {
		c := limes.Capability{
			ID: uuid.New(), ParentID: parent, ExpiresAt: expiry,
			Subject: limes.Principal{Type: limes.PrincipalTypeAgent, TenantID: tenant, Subject: "agent:" + strconv.Itoa(i)},
			Caveats: limes.Caveats{Ops: []limes.Op{limes.OpGet}, Requests: limes.RequestCeiling{Unlimited: true}, Budget: ceiling},
		}
		if err := records.Insert(ctx, limes.InsertRequest{Capability: c}); err != nil {
			t.Fatal(err)
		}
		chain = append(chain, c.ID)
		subjects = append(subjects, soakSubject(c.ID, tenant, ceiling))
		parent = c.ID
	}

	tally := &soakTally{own: map[uuid.UUID]limes.Nanos{}}
	deadline := time.Now().Add(duration)
	soakCtx, stop := context.WithDeadline(ctx, deadline)
	defer stop()

	// The housekeeper releases the holds workers abandon, as a deployment's
	// timer does.
	var wg sync.WaitGroup
	var swept atomic.Int64
	failures := make(chan error, soakWorkers+1)
	wg.Go(func() {
		tick := time.NewTicker(soakSweepEvery)
		defer tick.Stop()
		for {
			select {
			case <-soakCtx.Done():
				return
			case <-tick.C:
				res, err := usage.ReleaseExpired(ctx, meter.ReleaseExpiredRequest{})
				if err != nil {
					failures <- fmt.Errorf("ReleaseExpired: %w", err)
					return
				}
				swept.Add(res.Released)
			}
		}
	})
	for w := range soakWorkers {
		wg.Go(func() {
			if err := soakWorker(soakCtx, ctx, usage, chain, subjects, tally, w); err != nil {
				failures <- err
			}
		})
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}

	// What is left held expires within soakHoldTTL: wait it out, then
	// release it.
	time.Sleep(soakHoldTTL + soakSweepEvery)
	for {
		res, err := usage.ReleaseExpired(ctx, meter.ReleaseExpiredRequest{})
		if err != nil {
			t.Fatal(err)
		}
		swept.Add(res.Released)
		if res.Released == 0 {
			break
		}
	}

	checkSoak(t, ctx, usage, pool, chain, tally)
	t.Logf("soak %s: %s, %d refused by the ceiling, %d holds expired and released",
		duration, tally.summary(), tally.refused.Load(), swept.Load())
}

// soakWorker calls the store until soakCtx ends, each call one of soakOp
// on a capability of the chain picked at random.
func soakWorker(soakCtx, ctx context.Context, usage *UsageStore, chain []uuid.UUID, subjects []limes.MeterSubject, tally *soakTally, w int) error {
	rng := rand.New(rand.NewPCG(uint64(w), uint64(time.Now().UnixNano())))
	// The charges this worker made, with the capability, amount and
	// reference each was made with.
	var charges []meter.ChargeReceipt
	var chargedBy []uuid.UUID
	var amounts []limes.Nanos
	var refs []string
	n := 0
	for soakCtx.Err() == nil {
		n++
		i := rng.IntN(len(chain))
		id, subject := chain[i], subjects[i]
		amount := limes.Nanos(rng.Int64N(int64(soakMaxAmount))) + 1
		op := soakOp(rng.IntN(soakOps))
		tally.calls[op].Add(1)
		switch op {
		case opCharge:
			ref := fmt.Sprintf("w%d-%d", w, n)
			r, err := usage.Charge(ctx, meter.ChargeRequest{Subject: subject, Amount: amount, ExternalRef: ref}, nil)
			if refused(err, tally) {
				continue
			}
			if err != nil {
				return fmt.Errorf("Charge: %w", err)
			}
			tally.add(id, amount)
			charges, chargedBy, amounts, refs = append(charges, r), append(chargedBy, id), append(amounts, amount), append(refs, ref)
		case opChargeReplayed:
			if len(charges) == 0 {
				continue
			}
			// A charge of this worker, delivered again: it moves nothing.
			k := rng.IntN(len(charges))
			r, err := usage.Charge(ctx, meter.ChargeRequest{Subject: subjectOf(chargedBy[k], chain, subjects), Amount: amounts[k], ExternalRef: refs[k]}, nil)
			if err != nil {
				return fmt.Errorf("Charge replayed: %w", err)
			}
			if !r.Replayed || r.ChargeID != charges[k].ChargeID {
				return fmt.Errorf("charge %s delivered again was not a replay of %s: %+v", refs[k], charges[k].ChargeID, r)
			}
		case opRefund:
			if len(charges) == 0 {
				continue
			}
			k := rng.IntN(len(charges))
			res, err := usage.Refund(ctx, meter.RefundRequest{ChargeID: charges[k].ChargeID, Remaining: true})
			if err != nil {
				return fmt.Errorf("Refund: %w", err)
			}
			tally.add(chargedBy[k], -res.Amount)
		case opReserveSettle, opReserveRelease, opReserveAbandon:
			ttl := time.Duration(0)
			if op == opReserveAbandon {
				ttl = soakHoldTTL
			}
			hold, err := usage.Reserve(ctx, meter.ReserveRequest{Subject: subject, Amount: amount, TTL: ttl})
			if refused(err, tally) {
				continue
			}
			if err != nil {
				return fmt.Errorf("Reserve: %w", err)
			}
			// An abandoned hold is left to expire and the housekeeper.
			if op == opReserveSettle {
				settled := limes.Nanos(rng.Int64N(int64(amount))) + 1
				_, err := usage.Settle(ctx, meter.SettleRequest{Subject: subject, ReservationID: hold.ID, Amount: settled}, nil)
				if refused(err, tally) {
					continue
				}
				if err != nil {
					return fmt.Errorf("Settle: %w", err)
				}
				tally.add(id, settled)
			} else if op == opReserveRelease {
				if err := usage.Release(ctx, meter.ReleaseRequest{Subject: subject, ReservationID: hold.ID}); err != nil {
					return fmt.Errorf("Release: %w", err)
				}
			}
		case opBump:
			if _, err := usage.Bump(ctx, meter.BumpRequest{Subject: subject}); err != nil {
				return fmt.Errorf("Bump: %w", err)
			}
		}
	}
	return nil
}

// subjectOf is the subject of capability id of the chain.
func subjectOf(id uuid.UUID, chain []uuid.UUID, subjects []limes.MeterSubject) limes.MeterSubject {
	for i, c := range chain {
		if c == id {
			return subjects[i]
		}
	}
	panic("not of the chain")
}

// refused reports whether err is a refusal by a ceiling, which the soak
// expects and counts.
func refused(err error, tally *soakTally) bool {
	if errors.Is(err, limes.ErrBudgetExceeded) {
		tally.refused.Add(1)
		return true
	}
	return false
}

// summary is how many calls of each kind ran.
func (t *soakTally) summary() string {
	names := [soakOps]string{"charge", "replayed", "refund", "reserve+settle", "reserve+release", "reserve+abandon", "bump"}
	s := ""
	for i, name := range names {
		s += fmt.Sprintf("%s %d, ", name, t.calls[i].Load())
	}
	return s[:len(s)-len(", ")]
}

// checkSoak holds the store to what the workers did.
func checkSoak(t *testing.T, ctx context.Context, usage *UsageStore, pool *pgxpool.Pool, chain []uuid.UUID, tally *soakTally) {
	t.Helper()
	// A capability's spend is its subtree's: its own and every descendant's.
	var subtree limes.Nanos
	for i := len(chain) - 1; i >= 0; i-- {
		subtree += tally.own[chain[i]]
		u, err := usage.GetUsage(ctx, chain[i])
		if err != nil {
			t.Fatal(err)
		}
		if u.SpentAmount != subtree {
			t.Errorf("capability %d of the chain spent %d, want %d", i, u.SpentAmount, subtree)
		}
		if u.ReservedAmount != 0 {
			t.Errorf("capability %d of the chain holds %d after every hold was settled, released or expired", i, u.ReservedAmount)
		}
	}
	if subtree > soakRootCeiling {
		t.Errorf("the chain spent %d past the root's ceiling %d", subtree, soakRootCeiling)
	}
	page, err := usage.ListReservations(ctx, meter.ListReservationsRequest{CapabilityID: chain[0]})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 0 {
		t.Errorf("%d holds left on the root", len(page.Items))
	}
	if s := pool.Stat(); s.AcquiredConns() != 0 || s.TotalConns() > soakPoolConns {
		t.Errorf("pool: %d connections still acquired, %d open of %d", s.AcquiredConns(), s.TotalConns(), soakPoolConns)
	}
}
