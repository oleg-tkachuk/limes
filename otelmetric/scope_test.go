package otelmetric

import (
	"fmt"
	"testing"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/meter"
)

// A refusal for a limit names the ceiling that refused it, the narrowest
// first, through a wrap too; any other outcome names none.
func TestRefusalScope(t *testing.T) {
	cases := []struct {
		err   error
		scope string
		ok    bool
	}{
		{limes.ErrBudgetExceeded, scopeCapability, true},
		{limes.ErrRequestLimitExceeded, scopeCapability, true},
		{meter.ErrLeaseExhausted, scopeLease, true},
		{meter.ErrLeaseRequestsExhausted, scopeLease, true},
		{fmt.Errorf("charging: %w", meter.ErrCopyLimitExceeded), scopeCopy, true},
		{limes.ErrTenantBudgetExceeded, scopeTenant, true},
		{nil, "", false},
		{limes.ErrInvalidRequest, "", false},
	}
	for _, tc := range cases {
		if scope, ok := refusalScope(tc.err); scope != tc.scope || ok != tc.ok {
			t.Errorf("refusalScope(%v) = %q, %v; want %q, %v", tc.err, scope, ok, tc.scope, tc.ok)
		}
	}
}
