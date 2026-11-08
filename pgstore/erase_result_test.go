package pgstore

import (
	"errors"
	"log/slog"
	"testing"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/meter"
)

// erasedChargesLogged is how many charges the logging check takes before
// it erases them.
const erasedChargesLogged = 2

// EraseTenantUsage logs what it removed, the counts it returns, and logs
// a call refused for another tenant's scope as a warning.
func TestEraseTenantUsageLogsWhatItRemoved(t *testing.T) {
	f := newTenantFixture(t)
	logged := &logRecords{}
	usage, err := NewUsageStore(Config{DB: app, Now: f.clock.Now, Logger: slog.New(logged)})
	if err != nil {
		t.Fatal(err)
	}
	mine := WithTenant(t.Context(), f.mine)
	for range erasedChargesLogged {
		if _, err := usage.Charge(mine, meter.ChargeRequest{Subject: meterSubject(f.child.ID, f.mine), Amount: 1}, nil); err != nil {
			t.Fatal(err)
		}
	}
	req := meter.EraseTenantUsageRequest{TenantID: f.mine}
	if _, err := usage.EraseTenantUsage(WithTenant(t.Context(), f.theirs), req); !errors.Is(err, limes.ErrScopeMismatch) {
		t.Fatalf("EraseTenantUsage under another tenant's scope: err = %v, want ErrScopeMismatch", err)
	}
	got, err := usage.EraseTenantUsage(mine, req)
	if err != nil {
		t.Fatal(err)
	}
	if got.Charges != erasedChargesLogged {
		t.Errorf("EraseTenantUsage removed %d charges, want %d", got.Charges, erasedChargesLogged)
	}
	logged.mu.Lock()
	defer logged.mu.Unlock()
	levels := map[slog.Level]int64{}
	for _, r := range logged.records {
		var op string
		var charges int64
		r.Attrs(func(a slog.Attr) bool {
			switch a.Key {
			case logKeyOp:
				op = a.Value.String()
			case logKeyCharges:
				charges = a.Value.Int64()
			}
			return true
		})
		if op == opEraseTenantUsage {
			levels[r.Level] = charges
		}
	}
	if c, ok := levels[slog.LevelInfo]; !ok || c != got.Charges {
		t.Errorf("the erase's info line counted %d charges (logged: %t), want %d", c, ok, got.Charges)
	}
	if _, ok := levels[slog.LevelWarn]; !ok {
		t.Error("the refused erase logged no warning")
	}
}
