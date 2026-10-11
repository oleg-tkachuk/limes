package limes

import (
	"reflect"
	"testing"

	"github.com/google/uuid"
)

// The subject carries what the verified capability carries, with the unit
// normalised, so a store counts against the token's own ceilings.
func TestMeterSubjectCarriesTheVerifiedCeilings(t *testing.T) {
	const requests = 7
	budget := MustParseAmount("2.50")
	c := Capability{
		ID:      uuid.New(),
		Subject: Principal{Type: PrincipalAgent, TenantID: uuid.New(), Subject: "a"},
		Caveats: Caveats{MaxRequests: requests, MaxBudgetAmount: budget},
		Copies:  []CopyCeiling{{RevocationID: []byte{1}, MaxRequests: 1, MaxBudget: budget}},
	}
	s := asVerified(c).MeterSubject()
	if s.CapabilityID() != c.ID || s.TenantID() != c.Subject.TenantID {
		t.Errorf("subject names %s of %s, want %s of %s", s.CapabilityID(), s.TenantID(), c.ID, c.Subject.TenantID)
	}
	if s.MaxRequests() != requests || s.MaxBudget() != budget {
		t.Errorf("ceilings = %d requests, %v; want %d, %v", s.MaxRequests(), s.MaxBudget(), requests, budget)
	}
	if s.UnitCode() != DefaultUnitCode {
		t.Errorf("unit of a capability naming none = %q, want %q", s.UnitCode(), DefaultUnitCode)
	}
	if !reflect.DeepEqual(s.Copies(), c.Copies) {
		t.Errorf("copies = %+v, want %+v", s.Copies(), c.Copies)
	}
}

// A stored record gives the subject a reporter with no token counts
// against: the record's ceilings and unit, and no Biscuit copy.
func TestRecordMeterSubjectCarriesTheStoredCeilings(t *testing.T) {
	const requests, unit = 3, "EUR"
	budget := MustParseAmount("4")
	rec := Record{
		Capability: Capability{
			ID:      uuid.New(),
			Subject: Principal{Type: PrincipalAgent, TenantID: uuid.New(), Subject: "a"},
			Caveats: Caveats{MaxRequests: requests, MaxBudgetAmount: budget, UnitCode: unit},
			Copies:  []CopyCeiling{{RevocationID: []byte{1}, MaxRequests: 1}},
		},
		Revocation: &Revocation{Reason: "done"},
	}
	s := rec.MeterSubject()
	if s.CapabilityID() != rec.Capability.ID || s.TenantID() != rec.Capability.Subject.TenantID {
		t.Errorf("subject names %s of %s, want the record's", s.CapabilityID(), s.TenantID())
	}
	if s.MaxRequests() != requests || s.MaxBudget() != budget || s.UnitCode() != unit {
		t.Errorf("subject = %d requests, %v %s; want %d, %v %s", s.MaxRequests(), s.MaxBudget(), s.UnitCode(), requests, budget, unit)
	}
	if len(s.Copies()) != 0 {
		t.Errorf("copies = %+v, want none from a stored record", s.Copies())
	}
}

// Neither the capability it came from nor a caller holding its copies can
// change a subject's ceilings afterwards.
func TestMeterSubjectCopiesAreItsOwn(t *testing.T) {
	c := Capability{ID: uuid.New(), Copies: []CopyCeiling{{RevocationID: []byte{1}, MaxRequests: 1}}}
	s := asVerified(c).MeterSubject()
	c.Copies[0].MaxRequests, c.Copies[0].RevocationID[0] = 0, 0
	got := s.Copies()
	got[0].MaxRequests, got[0].RevocationID[0] = 0, 0
	if again := s.Copies(); again[0].MaxRequests != 1 || again[0].RevocationID[0] != 1 {
		t.Errorf("copies after mutating the sources = %+v, want the verified ones", again)
	}
}
