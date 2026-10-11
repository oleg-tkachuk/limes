package limes

import (
	"slices"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes/internal/link"
)

// MeterSubject is what a Meter counts a request against: a verified
// capability's id and tenant, its request and budget ceilings, its unit and
// the limits of the Biscuit copy presented. Only
// VerifiedCapability.MeterSubject and Record.MeterSubject make one, so a
// ceiling is always the one the token or the stored record carries — never a
// value a caller typed, or left at zero and so lifted. A store reads it
// through its methods.
type MeterSubject struct {
	capabilityID uuid.UUID
	tenantID     uuid.UUID
	maxRequests  int64
	maxBudget    Nanos
	unitCode     string
	copies       []CopyCeiling
}

// MeterSubject is what the Meter counts this capability's requests against.
func (v VerifiedCapability) MeterSubject() MeterSubject {
	return MeterSubject{
		capabilityID: v.c.ID,
		tenantID:     v.c.Subject.TenantID,
		maxRequests:  v.c.Caveats.MaxRequests,
		maxBudget:    v.c.Caveats.MaxBudgetAmount,
		unitCode:     canonicalUnitCode(v.c.Caveats.UnitCode),
		copies:       v.c.clone().Copies,
	}
}

// MeterSubject is what the Meter counts this record's capability against,
// for a cost reported with no token in hand. A stored record names no Biscuit
// copy, so the subject carries none. A revoked capability still gives one: a
// cost already incurred belongs in the ledger.
func (r Record) MeterSubject() MeterSubject {
	c := r.Capability
	return MeterSubject{
		capabilityID: c.ID,
		tenantID:     c.Subject.TenantID,
		maxRequests:  c.Caveats.MaxRequests,
		maxBudget:    c.Caveats.MaxBudgetAmount,
		unitCode:     canonicalUnitCode(c.Caveats.UnitCode),
	}
}

// CapabilityID is the capability the request counts against.
func (s MeterSubject) CapabilityID() uuid.UUID { return s.capabilityID }

// TenantID is the capability's tenant: its aggregate ceiling applies, and
// the ledger files the charge under it.
func (s MeterSubject) TenantID() uuid.UUID { return s.tenantID }

// MaxRequests is the capability's own request ceiling; 0 is unlimited.
func (s MeterSubject) MaxRequests() int64 { return s.maxRequests }

// MaxBudget is the capability's own budget ceiling; 0 is unlimited.
func (s MeterSubject) MaxBudget() Nanos { return s.maxBudget }

// UnitCode is the unit the capability counts in, normalised: empty reads
// as DefaultUnitCode.
func (s MeterSubject) UnitCode() string { return s.unitCode }

// Copies are the limits of the Biscuit copy presented, a copy of them.
func (s MeterSubject) Copies() []CopyCeiling {
	out := slices.Clone(s.copies)
	for i := range out {
		out[i].RevocationID = slices.Clone(out[i].RevocationID)
	}
	return out
}

func init() {
	link.NewMeterSubject = func(f link.MeterSubjectFields) any {
		copies, _ := f.Copies.([]CopyCeiling)
		return MeterSubject{
			capabilityID: f.CapabilityID, tenantID: f.TenantID,
			maxRequests: f.MaxRequests, maxBudget: Nanos(f.MaxBudget),
			unitCode: canonicalUnitCode(f.UnitCode), copies: copies,
		}
	}
}
