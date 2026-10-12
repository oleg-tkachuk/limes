package limes

import (
	"errors"
	"reflect"
	"testing"

	"github.com/google/uuid"
)

// A meter request names its ceilings only through the subject: a field of
// its own would be one a caller could type, or leave at zero and so lift.
func TestMeterRequestsCarryNoCeilingOfTheirOwn(t *testing.T) {
	ceilings := []string{"CapabilityID", "TenantID", "MaxRequests", "MaxBudgetAmount", "UnitCode", "Copies"}
	for _, req := range []any{BumpRequest{}, ChargeRequest{}, ReserveRequest{}, SettleRequest{}} {
		typ := reflect.TypeOf(req)
		for _, name := range ceilings {
			if _, ok := typ.FieldByName(name); ok {
				t.Errorf("%s has a field %s outside its Subject", typ.Name(), name)
			}
		}
	}
}

func TestRefundRequestNamesOneAmount(t *testing.T) {
	charge := uuid.New()
	cases := map[string]struct {
		req  RefundRequest
		want error
	}{
		"an amount":          {RefundRequest{ChargeID: charge, Amount: NanosPerUnit}, nil},
		"remaining":          {RefundRequest{ChargeID: charge, Remaining: true}, nil},
		"neither":            {RefundRequest{ChargeID: charge}, ErrInvalidRequest},
		"both":               {RefundRequest{ChargeID: charge, Amount: NanosPerUnit, Remaining: true}, ErrInvalidRequest},
		"a negative amount":  {RefundRequest{ChargeID: charge, Amount: -NanosPerUnit}, ErrInvalidAmount},
		"past the most kept": {RefundRequest{ChargeID: charge, Amount: MaxNanos + 1}, ErrInvalidAmount},
	}
	for name, tc := range cases {
		if err := tc.req.Validate(); !errors.Is(err, tc.want) || (tc.want == nil && err != nil) {
			t.Errorf("%s: Validate() = %v, want %v", name, err, tc.want)
		}
	}
}

func TestTenantBudgetUtilization(t *testing.T) {
	const ceiling = 10 * NanosPerUnit
	cases := map[string]struct {
		budget  TenantBudget
		pct     float64
		limited bool
	}{
		"no ceiling":         {TenantBudget{SpentAmount: ceiling}, 0, false},
		"nothing spent":      {TenantBudget{MaxBudgetAmount: ceiling}, 0, true},
		"a quarter spent":    {TenantBudget{MaxBudgetAmount: ceiling, SpentAmount: ceiling / 4}, wholePct / 4, true},
		"all spent":          {TenantBudget{MaxBudgetAmount: ceiling, SpentAmount: ceiling}, wholePct, true},
		"past the ceiling":   {TenantBudget{MaxBudgetAmount: ceiling, SpentAmount: 2 * ceiling}, wholePct, true},
		"a negative ceiling": {TenantBudget{MaxBudgetAmount: -ceiling, SpentAmount: ceiling}, 0, false},
	}
	for name, tc := range cases {
		if pct, limited := tc.budget.Utilization(); pct != tc.pct || limited != tc.limited {
			t.Errorf("%s: Utilization() = %v, %v; want %v, %v", name, pct, limited, tc.pct, tc.limited)
		}
	}
}
