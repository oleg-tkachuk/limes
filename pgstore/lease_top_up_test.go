package pgstore

import (
	"log/slog"
	"testing"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/meter"
)

// The amounts the top-up check leases, charges and tops up.
const (
	topUpLeased  limes.Nanos = 10
	topUpCharged limes.Nanos = 4
	topUpAdded   limes.Nanos = 3
)

// A top-up of an open lease settles it in the lease's own transaction,
// opens a new one of what it had left and the top-up, and says so: what the
// old one spent, which reached the chain, and what it carried over.
func TestALeaseTopUpSettlesAndLogsIt(t *testing.T) {
	fresh(t)
	subject := leaseholderOf(t)
	ctx := WithTenant(t.Context(), subject.TenantID())
	logged := &logRecords{}
	usage, err := NewUsageStore(Config{DB: app, Logger: slog.New(logged)})
	if err != nil {
		t.Fatal(err)
	}
	first, err := usage.Lease(ctx, meter.LeaseRequest{Subject: subject, Amount: topUpLeased})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := usage.Charge(ctx, meter.ChargeRequest{Subject: subject, Amount: topUpCharged}, nil); err != nil {
		t.Fatal(err)
	}
	topped, err := usage.Lease(ctx, meter.LeaseRequest{Subject: subject, Amount: topUpAdded})
	if err != nil {
		t.Fatal(err)
	}
	carried := topUpLeased - topUpCharged
	if topped.ID == first.ID || topped.Amount != carried+topUpAdded || topped.Spent != 0 {
		t.Errorf("a top-up = %+v, want a new lease of %v, nothing spent", topped, carried+topUpAdded)
	}
	logged.mu.Lock()
	defer logged.mu.Unlock()
	var found bool
	for _, r := range logged.records {
		attrs := map[string]slog.Value{}
		r.Attrs(func(a slog.Attr) bool {
			attrs[a.Key] = a.Value
			return true
		})
		if r.Level != slog.LevelDebug || attrs[logKeyOp].String() != retriedLease {
			continue
		}
		found = true
		if attrs[logKeyCapability].String() != subject.CapabilityID().String() ||
			attrs[logKeySettledSpent].Int64() != int64(topUpCharged) || attrs[logKeyCarried].Int64() != int64(carried) {
			t.Errorf("the top-up's line = %v, want the capability, %v settled and %v carried", attrs, topUpCharged, carried)
		}
	}
	if !found {
		t.Error("the top-up logged no line")
	}
}
