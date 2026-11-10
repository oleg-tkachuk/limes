package meter

import (
	"errors"
	"testing"
	"time"
	"uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/internal/link"
)

// Every meter request that hands a store text refuses, as
// limes.ErrInvalidRequest, text a store cannot keep — one with a NUL, one
// that is not UTF-8 — and accepts the same request with plain text.
func TestMeterRequestsRefuseUnstorableText(t *testing.T) {
	subject := link.NewMeterSubject(link.MeterSubjectFields{
		CapabilityID: uuid.New(), TenantID: uuid.New(), Budget: limes.BudgetCeiling{UnitCode: "EUR"},
	}).(limes.MeterSubject)
	tenant := uuid.New()
	now := time.Now()
	cases := map[string]func(s string) error{
		"charge actor": func(s string) error { return ChargeRequest{Subject: subject, Amount: 1, Actor: s}.Validate() },
		"charge op": func(s string) error {
			return ChargeRequest{Subject: subject, Amount: 1, Op: limes.Op("x:" + s)}.Validate()
		},
		"charge external ref": func(s string) error {
			return ChargeRequest{Subject: subject, Amount: 1, ExternalRef: "r" + s}.Validate()
		},
		"reserve actor": func(s string) error { return ReserveRequest{Subject: subject, Amount: 1, Actor: s}.Validate() },
		"reserve op": func(s string) error {
			return ReserveRequest{Subject: subject, Amount: 1, Op: limes.Op("x:" + s)}.Validate()
		},
		"charge by ref":           func(s string) error { return ValidateExternalRef("r" + s) },
		"release matching":        func(s string) error { return ReleaseMatchingRequest{TenantID: tenant, Subject: "u" + s}.Validate() },
		"release matching client": func(s string) error { return ReleaseMatchingRequest{TenantID: tenant, ClientID: "c" + s}.Validate() },
		"sum charges actor": func(s string) error {
			return SumChargesRequest{TenantID: tenant, From: now, To: now.Add(time.Hour), Filter: ChargeFilter{Actors: []string{"a" + s}}}.Validate()
		},
		"sum charges op": func(s string) error {
			return SumChargesRequest{TenantID: tenant, From: now, To: now.Add(time.Hour), Filter: ChargeFilter{Ops: []limes.Op{limes.Op("x:" + s)}}}.Validate()
		},
	}
	for name, validate := range cases {
		if err := validate("plain"); err != nil {
			t.Errorf("%s with plain text: %v", name, err)
		}
		for _, s := range []string{"a\x00b", "a\xffb"} {
			if err := validate(s); !errors.Is(err, limes.ErrInvalidRequest) {
				t.Errorf("%s with %q: %v, want limes.ErrInvalidRequest", name, s, err)
			}
		}
	}
}
