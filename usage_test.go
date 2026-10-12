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
