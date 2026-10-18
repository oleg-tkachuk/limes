package meter

import (
	"errors"
	"reflect"
	"strconv"
	"testing"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes"
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
		"an amount":          {RefundRequest{ChargeID: charge, Amount: limes.NanosPerUnit}, nil},
		"remaining":          {RefundRequest{ChargeID: charge, Remaining: true}, nil},
		"neither":            {RefundRequest{ChargeID: charge}, limes.ErrInvalidRequest},
		"both":               {RefundRequest{ChargeID: charge, Amount: limes.NanosPerUnit, Remaining: true}, limes.ErrInvalidRequest},
		"a negative amount":  {RefundRequest{ChargeID: charge, Amount: -limes.NanosPerUnit}, limes.ErrInvalidAmount},
		"past the most kept": {RefundRequest{ChargeID: charge, Amount: limes.MaxNanos + 1}, limes.ErrInvalidAmount},
	}
	for name, tc := range cases {
		if err := tc.req.Validate(); !errors.Is(err, tc.want) || (tc.want == nil && err != nil) {
			t.Errorf("%s: Validate() = %v, want %v", name, err, tc.want)
		}
	}
}

func TestTenantBudgetUtilization(t *testing.T) {
	const ceiling = 10 * limes.NanosPerUnit
	cases := map[string]struct {
		budget  TenantBudget
		pct     float64
		limited bool
	}{
		"no ceiling":       {TenantBudget{Ceiling: limes.BudgetCeiling{Unlimited: true}, SpentAmount: ceiling}, 0, false},
		"a ceiling of 0":   {TenantBudget{SpentAmount: 0}, wholePct, true},
		"nothing spent":    {TenantBudget{Ceiling: limes.BudgetCeiling{Max: ceiling}}, 0, true},
		"a quarter spent":  {TenantBudget{Ceiling: limes.BudgetCeiling{Max: ceiling}, SpentAmount: ceiling / 4}, wholePct / 4, true},
		"all spent":        {TenantBudget{Ceiling: limes.BudgetCeiling{Max: ceiling}, SpentAmount: ceiling}, wholePct, true},
		"past the ceiling": {TenantBudget{Ceiling: limes.BudgetCeiling{Max: ceiling}, SpentAmount: 2 * ceiling}, wholePct, true},
	}
	for name, tc := range cases {
		if pct, limited := tc.budget.Utilization(); pct != tc.pct || limited != tc.limited {
			t.Errorf("%s: Utilization() = %v, %v; want %v, %v", name, pct, limited, tc.pct, tc.limited)
		}
	}
}

func TestOverrunPolicyNamesAndValidates(t *testing.T) {
	cases := []struct {
		policy OverrunPolicy
		name   string
		valid  bool
	}{
		{OverrunPolicyUnspecified, "unspecified", true},
		{OverrunPolicyReject, "reject", true},
		{OverrunPolicyRecord, "record", true},
		{OverrunPolicyRecord + 1, "OverrunPolicy(3)", false},
	}
	for _, tc := range cases {
		if got := tc.policy.String(); got != tc.name {
			t.Errorf("String() = %q, want %q", got, tc.name)
		}
		if err := tc.policy.Validate(); tc.valid != (err == nil) || (err != nil && !errors.Is(err, limes.ErrInvalidRequest)) {
			t.Errorf("%s: Validate() = %v, want valid=%v", tc.policy, err, tc.valid)
		}
	}
}

func TestCopyUsageRequestValidate(t *testing.T) {
	atLimit := make([][]byte, MaxCopyUsageIDs)
	for i := range atLimit {
		atLimit[i] = []byte(strconv.Itoa(i))
	}
	capID := uuid.New()
	cases := map[string]struct {
		capID uuid.UUID
		ids   [][]byte
		want  error
	}{
		"one id":        {capID, [][]byte{[]byte("copy")}, nil},
		"at the limit":  {capID, atLimit, nil},
		"no capability": {uuid.Nil, [][]byte{[]byte("copy")}, limes.ErrInvalidRequest},
		"no id":         {capID, nil, limes.ErrInvalidRequest},
		"past limit":    {capID, append(atLimit[:MaxCopyUsageIDs:MaxCopyUsageIDs], []byte("one more")), limes.ErrInvalidRequest},
		"empty id":      {capID, [][]byte{[]byte("copy"), {}}, limes.ErrInvalidRequest},
	}
	for name, tc := range cases {
		if err := (CopyUsageRequest{CapabilityID: tc.capID, RevocationIDs: tc.ids}).Validate(); !errors.Is(err, tc.want) {
			t.Errorf("%s: Validate() = %v, want %v", name, err, tc.want)
		}
	}
}
