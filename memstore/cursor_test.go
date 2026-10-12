package memstore

import (
	"encoding/base64"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes"
)

// A cursor holds a utilization the store computed: a finite, non-negative
// decimal. NaN and the infinities order differently, so they are refused
// rather than read.
func TestTenantBudgetCursorRefusesNonFinite(t *testing.T) {
	tenant := uuid.New()
	ok := tenantBudgetCursor{utilization: "42.5", tenantID: tenant}
	if got, err := decodeTenantBudgetCursor(ok.encode()); err != nil || got != ok {
		t.Fatalf("round trip = %+v, %v; want %+v", got, err, ok)
	}
	for _, pct := range []string{"NaN", "Inf", "-Inf", "+Inf", "infinity", "-1", "1e999", "0x1p3"} {
		c := tenantBudgetCursor{utilization: pct, tenantID: tenant}.encode()
		if _, err := decodeTenantBudgetCursor(c); !errors.Is(err, limes.ErrInvalidRequest) {
			t.Errorf("utilization %q: err = %v, want ErrInvalidRequest", pct, err)
		}
	}
	for _, raw := range []string{"!!", base64.RawURLEncoding.EncodeToString([]byte("42.5")), base64.RawURLEncoding.EncodeToString([]byte("42.5|not-a-uuid"))} {
		if _, err := decodeTenantBudgetCursor(raw); !errors.Is(err, limes.ErrInvalidRequest) {
			t.Errorf("cursor %q: err = %v, want ErrInvalidRequest", raw, err)
		}
	}
}
