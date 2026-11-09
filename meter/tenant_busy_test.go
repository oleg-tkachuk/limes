package meter

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/oleg-tkachuk/limes"
)

// A TenantBudgetBusyError is ErrTenantBudgetBusy to errors.Is and to limes,
// wrapped or not, and its text says what the store saw of the holders:
// none it could inspect, one idle in its transaction, or one running.
func TestTenantBudgetBusyError(t *testing.T) {
	tenant := uuid.New()
	const waited, running = 5 * time.Second, 2 * time.Minute
	for _, tc := range []struct {
		name   string
		oldest *TenantBudgetHolder
		says   string
	}{
		{"none visible", nil, "none of them visible"},
		{"idle", &TenantBudgetHolder{Running: running, IdleInTransaction: true}, "idle in its transaction for " + running.String()},
		{"running", &TenantBudgetHolder{Running: running}, "running for " + running.String()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := fmt.Errorf("store: %w", &TenantBudgetBusyError{TenantID: tenant, Waited: waited, Holders: 2, Oldest: tc.oldest})
			if !errors.Is(err, ErrTenantBudgetBusy) {
				t.Errorf("errors.Is(%v, ErrTenantBudgetBusy) = false", err)
			}
			if errors.Is(err, limes.ErrConflict) || errors.Is(err, limes.ErrInternal) {
				t.Errorf("%v matches a family, want none", err)
			}
			if code, ok := limes.CodeOf(err); !ok || code != CodeTenantBudgetBusy {
				t.Errorf("CodeOf = %q, %v; want %q", code, ok, CodeTenantBudgetBusy)
			}
			if p := limes.ProblemFor(err); p.Status != http.StatusServiceUnavailable {
				t.Errorf("ProblemFor(err).Status = %d, want %d", p.Status, http.StatusServiceUnavailable)
			}
			var busy *TenantBudgetBusyError
			if !errors.As(err, &busy) || busy.Oldest != tc.oldest {
				t.Fatalf("errors.As did not return the error made")
			}
			msg := err.Error()
			for _, want := range []string{ErrTenantBudgetBusy.Error(), tenant.String(), waited.String(), "2 calls", tc.says} {
				if !strings.Contains(msg, want) {
					t.Errorf("%q does not say %q", msg, want)
				}
			}
		})
	}
}
