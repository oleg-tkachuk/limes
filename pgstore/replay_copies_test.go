package pgstore

import (
	"context"
	"sync"
	"testing"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/internal/link"
	"github.com/oleg-tkachuk/limes/meter"
)

const (
	// sqlCopiesOf counts the copies recorded for capability $1.
	sqlCopiesOf = `SELECT count(*) FROM limes.copy_usage WHERE capability_id = $1`
	// replayRef is the ExternalRef both deliveries carry.
	replayRef = "call-1"
	// replayPoolConns is a connection for each of the three calls at once.
	replayPoolConns = 3
)

// A delivery of a charge that finds, once it holds the locks, that another
// delivery of the same reference committed while it waited, answers with
// that receipt and leaves nothing behind: not the counters its locking
// created, nor a copy it recorded after DeleteUsage dropped the first
// delivery's, which no copies_under would count.
func TestAChargeReplayedAfterItsLocksLeavesNothingBehind(t *testing.T) {
	f := newTenantFixture(t)
	ctx := WithTenant(t.Context(), f.mine)
	cfg := app.Config()
	cfg.MaxConns = replayPoolConns
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	usage, err := NewUsageStore(Config{DB: pool, Now: f.clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	c := f.parent
	c.ID, c.ParentID = uuid.New(), uuid.Nil()
	if err := f.records.Insert(ctx, limes.InsertRequest{Capability: c}); err != nil {
		t.Fatal(err)
	}
	subject := link.NewMeterSubject(link.MeterSubjectFields{
		CapabilityID: c.ID, TenantID: f.mine,
		Requests: limes.RequestCeiling{Unlimited: true}, Budget: limes.BudgetCeiling{Unlimited: true, UnitCode: testUnit},
		Copies:    []limes.CopyCeiling{{RevocationID: []byte(c.ID.String()), Requests: &limes.RequestCeiling{Unlimited: true}}},
		CopyQuota: limes.CopyQuota{Unlimited: true},
	}).(limes.MeterSubject)
	req := meter.ChargeRequest{Subject: subject, Amount: 1, ExternalRef: replayRef}
	// The counters are on record before the deliveries, so DeleteUsage
	// waits for the first delivery's lock on them rather than missing them.
	if _, err := usage.Bump(ctx, meter.BumpRequest{Subject: meterSubject(c.ID, f.mine)}); err != nil {
		t.Fatal(err)
	}
	waitForLockWaits := func(n int) {
		t.Helper()
		for deadline := time.Now().Add(lockWaitTimeout); ; time.Sleep(lockWaitPoll) {
			var waiting int
			if err := owner.QueryRow(ctx, sqlWaiting).Scan(&waiting); err != nil {
				t.Fatal(err)
			}
			if waiting >= n {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("%d sessions never waited on a lock", n)
			}
		}
	}

	// The first delivery holds its locks in onCharged until released.
	inCallback, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	releaseFirst := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseFirst)
	first := make(chan error, 1)
	go func() {
		_, err := usage.Charge(ctx, req, func(context.Context, meter.Charged) error {
			close(inCallback)
			<-release
			return nil
		})
		first <- err
	}()
	<-inCallback
	// DeleteUsage queues for the capability's counters, the second delivery
	// for the tenant's; once the first commits, DeleteUsage drops its copy
	// before the second records one again.
	deleted := make(chan error, 1)
	go func() { deleted <- usage.DeleteUsage(ctx, meter.DeleteUsageRequest{CapabilityID: c.ID}) }()
	waitForLockWaits(1)
	second := make(chan error, 1)
	go func() {
		_, err := usage.Charge(ctx, req, nil)
		second <- err
	}()
	waitForLockWaits(2)
	releaseFirst()
	for name, ch := range map[string]chan error{"first delivery": first, "DeleteUsage": deleted, "second delivery": second} {
		if err := <-ch; err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}

	var copies int
	if err := owner.QueryRow(ctx, sqlCopiesOf, c.ID).Scan(&copies); err != nil {
		t.Fatal(err)
	}
	if copies != 0 {
		t.Errorf("the replayed delivery left %d copies recorded after DeleteUsage dropped them, want 0", copies)
	}
}
