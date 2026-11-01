package meter

import (
	"errors"
	"testing"
	"uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/internal/link"
)

// leaseSubject is a subject of a capability in a unit, as a verified token
// gives it.
func leaseSubject(capID, tenant uuid.UUID) limes.MeterSubject {
	return link.NewMeterSubject(link.MeterSubjectFields{
		CapabilityID: capID, TenantID: tenant,
		Requests: limes.RequestCeiling{Unlimited: true}, Budget: limes.BudgetCeiling{Unlimited: true, UnitCode: "USD"},
	}).(limes.MeterSubject)
}

func TestLeaseRequestValidate(t *testing.T) {
	subject := leaseSubject(uuid.New(), uuid.New())
	copySubject := link.NewMeterSubject(link.MeterSubjectFields{
		CapabilityID: uuid.New(), TenantID: uuid.New(),
		Requests: limes.RequestCeiling{Unlimited: true}, Budget: limes.BudgetCeiling{Unlimited: true, UnitCode: "USD"},
		Copies: []limes.CopyCeiling{{RevocationID: []byte("copy"), Budget: &limes.BudgetCeiling{Max: 1, UnitCode: "USD"}}},
	}).(limes.MeterSubject)
	for name, tc := range map[string]struct {
		req LeaseRequest
		ok  bool
	}{
		"an amount":         {LeaseRequest{Subject: subject, Amount: 1}, true},
		"a TTL":             {LeaseRequest{Subject: subject, Amount: 1, TTL: DefaultLeaseTTL}, true},
		"no amount":         {LeaseRequest{Subject: subject}, false},
		"requests alone":    {LeaseRequest{Subject: subject, Requests: 1}, true},
		"both":              {LeaseRequest{Subject: subject, Amount: 1, Requests: 1}, true},
		"negative requests": {LeaseRequest{Subject: subject, Amount: 1, Requests: -1}, false},
		"a negative amount": {LeaseRequest{Subject: subject, Amount: -1}, false},
		"past MaxNanos":     {LeaseRequest{Subject: subject, Amount: limes.MaxNanos + 1}, false},
		"a negative TTL":    {LeaseRequest{Subject: subject, Amount: 1, TTL: -1}, false},
		"no subject":        {LeaseRequest{Amount: 1}, false},
		"a Biscuit copy":    {LeaseRequest{Subject: copySubject, Amount: 1}, false},
	} {
		err := tc.req.Validate()
		if tc.ok != (err == nil) || (err != nil && !errors.Is(err, limes.ErrInvalidRequest) && !errors.Is(err, limes.ErrInvalidAmount)) {
			t.Errorf("%s: Validate = %v, want ok %v", name, err, tc.ok)
		}
	}
}

func TestSettleLeaseRequestValidate(t *testing.T) {
	if err := (SettleLeaseRequest{Subject: leaseSubject(uuid.New(), uuid.New())}).Validate(); err != nil {
		t.Errorf("a subject: %v", err)
	}
	if err := (SettleLeaseRequest{}).Validate(); !errors.Is(err, limes.ErrInvalidRequest) {
		t.Errorf("no subject: %v, want ErrInvalidRequest", err)
	}
}

func TestLeaseRemaining(t *testing.T) {
	l := Lease{Amount: 10, Spent: 3, Requests: 5, RequestsSpent: 2}
	if got := l.Remaining(); got != 7 {
		t.Errorf("Remaining = %v, want 7", got)
	}
	if got := l.RequestsRemaining(); got != 3 {
		t.Errorf("RequestsRemaining = %v, want 3", got)
	}
}
