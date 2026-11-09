package pgstore

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/oleg-tkachuk/limes/meter"
)

// sqlHoldTenant stands in for another call of the tenant holding its
// aggregate.
const sqlHoldTenant = `SELECT 1 FROM ` + schema + `.tenant_budgets WHERE tenant_id = $1 FOR UPDATE`

// tenantHeldFor is how long the other call holds the aggregate: past
// defaultTenantWaitWarnAfter, so the wait is logged.
const tenantHeldFor = 2 * defaultTenantWaitWarnAfter

// tenantWaits keeps the events a store reports.
type tenantWaits struct {
	mu     sync.Mutex
	events []meter.TenantWaitEvent
}

func (w *tenantWaits) record(_ context.Context, e meter.TenantWaitEvent) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.events = append(w.events, e)
}

// take returns the events reported since the last take.
func (w *tenantWaits) take() []meter.TenantWaitEvent {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := w.events
	w.events = nil
	return out
}

// warnings counts the WARN lines about a wait for the tenant's aggregate.
func (h *logRecords) waitWarnings() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, r := range h.records {
		if r.Level != slog.LevelWarn {
			continue
		}
		r.Attrs(func(a slog.Attr) bool {
			if a.Key == logKeyWait {
				n++
			}
			return true
		})
	}
	return n
}

// A metered call reports every wait for its tenant's aggregate through
// Config.Events, under its own operation and however short; a wait past
// the threshold is logged at WARN too. A Bump locks no aggregate and
// reports nothing.
func TestTenantWaitIsReported(t *testing.T) {
	fresh(t)
	waits := &tenantWaits{}
	logged := &logRecords{}
	usage, err := NewUsageStore(Config{DB: app, Logger: slog.New(logged), Events: meter.StoreEvents{OnTenantWait: waits.record}})
	if err != nil {
		t.Fatal(err)
	}
	subject := leaseholderOf(t)
	tenant := subject.TenantID()
	ctx := WithTenant(t.Context(), tenant)

	if _, err := usage.Charge(ctx, meter.ChargeRequest{Subject: subject, Amount: 1}, nil); err != nil {
		t.Fatal(err)
	}
	got := waits.take()
	if len(got) != 1 || got[0].Operation != meter.StoreOperationCharge || got[0].TenantID != tenant || got[0].Wait < 0 {
		t.Fatalf("an uncontended Charge reported %+v, want one Charge wait of tenant %s", got, tenant)
	}
	if n := logged.waitWarnings(); n != 0 {
		t.Fatalf("an uncontended Charge logged %d wait warnings, want none", n)
	}

	if _, err := usage.Bump(ctx, meter.BumpRequest{Subject: subject}); err != nil {
		t.Fatal(err)
	}
	if got := waits.take(); len(got) != 0 {
		t.Fatalf("Bump reported %+v, want nothing: it locks no aggregate", got)
	}

	other, err := owner.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Rollback(context.Background()) }()
	if _, err := other.Exec(context.Background(), sqlHoldTenant, tenant); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := usage.Charge(ctx, meter.ChargeRequest{Subject: subject, Amount: 1}, nil)
		done <- err
	}()
	waitForLockWaiter(t)
	time.Sleep(tenantHeldFor)
	if err := other.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	got = waits.take()
	if len(got) != 1 || got[0].Operation != meter.StoreOperationCharge || got[0].Wait < tenantHeldFor {
		t.Fatalf("a Charge behind a held aggregate reported %+v, want one Charge wait of at least %v", got, tenantHeldFor)
	}
	if n := logged.waitWarnings(); n != 1 {
		t.Fatalf("a Charge behind a held aggregate logged %d wait warnings, want 1", n)
	}
}

// waitForLockWaiter returns once some session waits on a lock.
func waitForLockWaiter(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(lockWaitTimeout)
	for {
		var waiting int
		if err := owner.QueryRow(context.Background(), sqlWaiting).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the call never waited for the held aggregate")
		}
		time.Sleep(lockWaitPoll)
	}
}
