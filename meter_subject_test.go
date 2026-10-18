package limes

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes/internal/link"
)

// The subject carries what the verified capability carries, with the unit
// normalised, so a store counts against the token's own ceilings.
func TestMeterSubjectCarriesTheVerifiedCeilings(t *testing.T) {
	const requests = 7
	budget := MustParseAmount("2.50")
	c := Capability{
		ID:      uuid.New(),
		Subject: Principal{Type: PrincipalTypeAgent, TenantID: uuid.New(), Subject: "a"},
		Caveats: Caveats{Requests: RequestCeiling{Max: requests}, Budget: BudgetCeiling{Max: budget}},
	}
	copies := []CopyCeiling{{RevocationID: []byte{1}, Requests: &RequestCeiling{Max: 1}, Budget: &BudgetCeiling{Max: budget}}}
	s := VerifiedCapability{c: c, copies: copies}.MeterSubject()
	if s.CapabilityID() != c.ID || s.TenantID() != c.Subject.TenantID {
		t.Errorf("subject names %s of %s, want %s of %s", s.CapabilityID(), s.TenantID(), c.ID, c.Subject.TenantID)
	}
	if s.Requests() != (RequestCeiling{Max: requests}) || s.Budget() != (BudgetCeiling{Max: budget}) {
		t.Errorf("ceilings = %v requests, %v; want %d, %v", s.Requests(), s.Budget(), requests, budget)
	}
	if s.UnitCode() != "" {
		t.Errorf("unit of a capability naming none = %q, want none", s.UnitCode())
	}
	if !reflect.DeepEqual(s.Copies(), copies) {
		t.Errorf("copies = %+v, want %+v", s.Copies(), copies)
	}
}

// A stored record gives the subject a reporter with no token counts
// against: the record's ceilings and unit, and no Biscuit copy, read from
// the store — a revoked capability included.
func TestStoredMeterSubjectCarriesTheStoredCeilings(t *testing.T) {
	const requests, unit = 3, "EUR"
	ctx := context.Background()
	budget := MustParseAmount("4")
	c := Capability{
		ID:        uuid.New(),
		Subject:   Principal{Type: PrincipalTypeAgent, TenantID: uuid.New(), Subject: "a"},
		Caveats:   Caveats{Ops: []Op{OpGet}, Requests: RequestCeiling{Max: requests}, Budget: BudgetCeiling{Max: budget, UnitCode: unit}},
		ExpiresAt: time.Now().Add(time.Hour),
	}
	store := newMemStore()
	if err := store.Insert(ctx, InsertRequest{Capability: c, IssuedBy: Principal{Subject: "op"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.Revoke(ctx, RevokeRequest{CapabilityID: c.ID}); err != nil {
		t.Fatal(err)
	}
	s, err := StoredMeterSubject(ctx, store, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if s.CapabilityID() != c.ID || s.TenantID() != c.Subject.TenantID {
		t.Errorf("subject names %s of %s, want the record's", s.CapabilityID(), s.TenantID())
	}
	if s.Requests() != (RequestCeiling{Max: requests}) || s.Budget() != (BudgetCeiling{Max: budget, UnitCode: unit}) || s.UnitCode() != unit {
		t.Errorf("subject = %v requests, %v in %s; want %d, %v %s", s.Requests(), s.Budget(), s.UnitCode(), requests, budget, unit)
	}
	if len(s.Copies()) != 0 {
		t.Errorf("copies = %+v, want none from a stored record", s.Copies())
	}
	if got, err := StoredMeterSubject(ctx, store, uuid.New()); err == nil || got.CapabilityID() != uuid.Nil {
		t.Errorf("a capability not on record = %+v, %v; want the store's error and no subject", got, err)
	}
}

// Neither the capability it came from nor a caller holding its copies can
// change a subject's ceilings afterwards.
func TestMeterSubjectCopiesAreItsOwn(t *testing.T) {
	copies := []CopyCeiling{{RevocationID: []byte{1}, Requests: &RequestCeiling{Max: 1}}}
	vc := VerifiedCapability{c: Capability{ID: uuid.New()}, copies: copies}
	s := vc.MeterSubject()
	read := vc.Copies()
	*read[0].Requests, read[0].RevocationID[0] = RequestCeiling{}, 0
	if again := vc.Copies(); !ceilingIs(again[0].Requests, RequestCeiling{Max: 1}) || again[0].RevocationID[0] != 1 {
		t.Errorf("VerifiedCapability.Copies after mutating what it returned = %+v, want its own", again)
	}
	*copies[0].Requests, copies[0].RevocationID[0] = RequestCeiling{}, 0
	got := s.Copies()
	*got[0].Requests, got[0].RevocationID[0] = RequestCeiling{}, 0
	if again := s.Copies(); !ceilingIs(again[0].Requests, RequestCeiling{Max: 1}) || again[0].RevocationID[0] != 1 {
		t.Errorf("copies after mutating the sources = %+v, want the verified ones", again)
	}
}

// link.NewMeterSubject takes its parts as any: a part of another type is the
// module's own wiring gone wrong, and is refused loudly rather than read as
// none — no copy ceilings would let a copy spend past its limits. A part
// left out is its zero value.
func TestLinkedMeterSubjectRefusesAMistypedPart(t *testing.T) {
	copies := []CopyCeiling{{RevocationID: []byte("copy"), Budget: &BudgetCeiling{Max: 1}}}
	for name, f := range map[string]link.MeterSubjectFields{
		"copies by pointer":   {Copies: &copies},
		"copies by element":   {Copies: copies[0]},
		"requests by pointer": {Requests: &RequestCeiling{Max: 1}},
		"budget as requests":  {Budget: RequestCeiling{Max: 1}},
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: built a subject", name)
				}
			}()
			link.NewMeterSubject(f)
		}()
	}

	s := link.NewMeterSubject(link.MeterSubjectFields{CapabilityID: uuid.New()}).(MeterSubject)
	if s.Copies() != nil || s.Requests() != (RequestCeiling{}) || s.Budget() != (BudgetCeiling{}) {
		t.Errorf("parts left out = %v, %+v, %+v; want none, and ceilings of zero", s.Copies(), s.Requests(), s.Budget())
	}
	s = link.NewMeterSubject(link.MeterSubjectFields{Copies: copies}).(MeterSubject)
	if !reflect.DeepEqual(s.Copies(), copies) {
		t.Errorf("copies = %+v, want %+v", s.Copies(), copies)
	}
}

// An amount is counted in a unit, and none is assumed: a request that moves
// one refuses a subject naming none, while a bump, which counts a request
// alone, takes it.
func TestAnAmountNeedsAUnit(t *testing.T) {
	none := MeterSubject{capabilityID: uuid.New(), tenantID: uuid.New()}
	for name, err := range map[string]error{
		"Charge":  ChargeRequest{Subject: none, Amount: NanosPerUnit}.Validate(),
		"Reserve": ReserveRequest{Subject: none, Amount: NanosPerUnit}.Validate(),
		"Settle":  SettleRequest{Subject: none, Amount: NanosPerUnit}.Validate(),
	} {
		if !errors.Is(err, ErrInvalidRequest) {
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
