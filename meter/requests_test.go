package meter

import (
	"errors"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/internal/link"
)

// The meter's requests refuse what every store would refuse, so a store
// calling Validate enforces a rule added later without a change of its own.
func TestMeterRequestsValidate(t *testing.T) {
	subject := link.NewMeterSubject(link.MeterSubjectFields{
		CapabilityID: uuid.New(), TenantID: uuid.New(), Budget: limes.BudgetCeiling{UnitCode: "EUR"},
	}).(limes.MeterSubject)
	noTenant := link.NewMeterSubject(link.MeterSubjectFields{CapabilityID: uuid.New()}).(limes.MeterSubject)
	badUnit := link.NewMeterSubject(link.MeterSubjectFields{
		CapabilityID: uuid.New(), TenantID: uuid.New(), Budget: limes.BudgetCeiling{UnitCode: "euro"},
	}).(limes.MeterSubject)
	const one = limes.Nanos(1)
	longRef := strings.Repeat("r", MaxExternalRefBytes+1)

	for name, tc := range map[string]struct {
		req   interface{ Validate() error }
		valid bool
	}{
		"bump":                    {BumpRequest{Subject: subject}, true},
		"bump, no subject":        {BumpRequest{}, false},
		"bump, no tenant":         {BumpRequest{Subject: noTenant}, false},
		"bump, unknown unit":      {BumpRequest{Subject: badUnit}, false},
		"charge":                  {ChargeRequest{Subject: subject, Amount: one, ExternalRef: "call-1", Overrun: OverrunPolicyRecord}, true},
		"charge of nothing":       {ChargeRequest{Subject: subject}, true},
		"charge, negative":        {ChargeRequest{Subject: subject, Amount: -one}, false},
		"charge, past MaxNanos":   {ChargeRequest{Subject: subject, Amount: limes.MaxNanos + 1}, false},
		"charge, unknown overrun": {ChargeRequest{Subject: subject, Amount: one, Overrun: OverrunPolicyRecord + 1}, false},
		"charge, ref too long":    {ChargeRequest{Subject: subject, Amount: one, ExternalRef: longRef}, false},
		"charge, no subject":      {ChargeRequest{Amount: one}, false},
		"reserve":                 {ReserveRequest{Subject: subject, Amount: one, TTL: time.Minute}, true},
		"reserve, default TTL":    {ReserveRequest{Subject: subject, Amount: one}, true},
		"reserve, negative TTL":   {ReserveRequest{Subject: subject, Amount: one, TTL: -time.Second}, false},
		"reserve, negative":       {ReserveRequest{Subject: subject, Amount: -one}, false},
		"reserve, no subject":     {ReserveRequest{Amount: one}, false},
		"settle":                  {SettleRequest{Subject: subject, ReservationID: uuid.New(), Amount: one}, true},
		"settle, no reservation":  {SettleRequest{Subject: subject, Amount: one}, true},
		"settle, negative":        {SettleRequest{Subject: subject, ReservationID: uuid.New(), Amount: -one}, false},
		"settle, unknown overrun": {SettleRequest{Subject: subject, ReservationID: uuid.New(), Overrun: OverrunPolicyRecord + 1}, false},
		"settle, no subject":      {SettleRequest{ReservationID: uuid.New(), Amount: one}, false},
		"release":                 {ReleaseRequest{Subject: subject, ReservationID: uuid.New()}, true},
		"release, no subject":     {ReleaseRequest{ReservationID: uuid.New()}, false},
		"release, unknown unit":   {ReleaseRequest{Subject: badUnit, ReservationID: uuid.New()}, false},
	} {
		err := tc.req.Validate()
		if tc.valid && err != nil {
			t.Errorf("%s: refused: %v", name, err)
		}
		if !tc.valid && !errors.Is(err, limes.ErrInvalidRequest) {
			t.Errorf("%s: err = %v, want ErrInvalidRequest", name, err)
		}
	}
}

// A tenant budget request is refused as malformed before any store reads it.
func TestSetTenantBudgetRequestValidate(t *testing.T) {
	tenant := uuid.New()
	ok := SetTenantBudgetRequest{TenantID: tenant, Ceiling: limes.BudgetCeiling{Max: limes.NanosPerUnit, UnitCode: "USD"}}
	if err := ok.Validate(); err != nil {
		t.Fatalf("a well-formed request: %v", err)
	}
	for name, req := range map[string]SetTenantBudgetRequest{
		"no tenant":          {Ceiling: limes.BudgetCeiling{Max: limes.NanosPerUnit}},
		"a negative version": {TenantID: tenant, ExpectedVersion: -1},
		"a negative ceiling": {TenantID: tenant, Ceiling: limes.BudgetCeiling{Max: -1}},
		"a lower-case unit":  {TenantID: tenant, Ceiling: limes.BudgetCeiling{Max: 1, UnitCode: "usd"}},
		"spend in no unit":   {TenantID: tenant, Ceiling: limes.BudgetCeiling{Unlimited: true}},
	} {
		if err := req.Validate(); !errors.Is(err, limes.ErrInvalidRequest) {
			t.Errorf("%s: err = %v, want ErrInvalidRequest", name, err)
		}
	}
}

// A threshold is a whole number of basis points within a whole; anything
// else is refused before any store compares against it.
func TestThresholdBasisPointsBounds(t *testing.T) {
	for bp, ok := range map[int]bool{0: true, 1: true, BasisPointsPerWhole: true, -1: false, BasisPointsPerWhole + 1: false} {
		err := (ListTenantBudgetsRequest{Filter: TenantBudgetFilterAtOrPast, ThresholdBasisPoints: bp}).Validate()
		if ok && err != nil || !ok && !errors.Is(err, limes.ErrInvalidRequest) {
			t.Errorf("a threshold of %d basis points: err = %v, want accepted: %v", bp, err, ok)
		}
	}
}

// A tenant budget filter names itself, and one this package lacks is refused.
func TestTenantBudgetFilter(t *testing.T) {
	for f, want := range map[TenantBudgetFilter]string{TenantBudgetFilterUnspecified: "unspecified", TenantBudgetFilterAll: "all", TenantBudgetFilterAtOrPast: "at-or-past", TenantBudgetFilterUnlimited: "unlimited"} {
		if f.String() != want || f.Validate() != nil {
			t.Errorf("%d: String = %q, Validate = %v; want %q, nil", f, f.String(), f.Validate(), want)
		}
	}
	if err := (TenantBudgetFilterUnlimited + 1).Validate(); !errors.Is(err, limes.ErrInvalidRequest) {
		t.Errorf("an unknown filter: err = %v, want ErrInvalidRequest", err)
	}
}

// An amount is counted in a unit, and none is assumed: a request that moves
// one refuses a subject naming none, while a bump, which counts a request
// alone, takes it.
func TestAnAmountNeedsAUnit(t *testing.T) {
	none := link.NewMeterSubject(link.MeterSubjectFields{CapabilityID: uuid.New(), TenantID: uuid.New()}).(limes.MeterSubject)
	for name, err := range map[string]error{
		"Charge":  ChargeRequest{Subject: none, Amount: limes.NanosPerUnit}.Validate(),
		"Reserve": ReserveRequest{Subject: none, Amount: limes.NanosPerUnit}.Validate(),
		"Settle":  SettleRequest{Subject: none, Amount: limes.NanosPerUnit}.Validate(),
	} {
		if !errors.Is(err, limes.ErrInvalidRequest) {
			t.Errorf("%s of a subject naming no unit: err = %v, want ErrInvalidRequest", name, err)
		}
	}
	for name, err := range map[string]error{
		"Bump":    BumpRequest{Subject: none}.Validate(),
		"Release": ReleaseRequest{Subject: none}.Validate(),
	} {
		if err != nil {
			t.Errorf("%s of a subject naming no unit: %v", name, err)
		}
	}
}
