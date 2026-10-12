package limes

import (
	"context"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes/internal/link"
)

// MeterSubject is what a Meter counts a request against: a verified
// capability's id and tenant, its request and budget ceilings, its unit and
// the limits of the Biscuit copy presented. Only
// VerifiedCapability.MeterSubject and StoredMeterSubject make one, so a
// ceiling is the one a verified token carries or a RecordReader reads back —
// never a value a caller sets on the subject itself. A store reads it through its methods. The zero
// MeterSubject names no capability and no tenant; a Meter refuses it with
// ErrInvalidRequest rather than count it anywhere.
type MeterSubject struct {
	capabilityID uuid.UUID
	tenantID     uuid.UUID
	maxRequests  RequestCeiling
	maxBudget    BudgetCeiling
	unitCode     string
	copies       []CopyCeiling
}

// MeterSubject is what the Meter counts this capability's requests against.
func (v VerifiedCapability) MeterSubject() MeterSubject {
	return MeterSubject{
		capabilityID: v.c.ID,
		tenantID:     v.c.Subject.TenantID,
		maxRequests:  v.c.Caveats.Requests,
		maxBudget:    v.c.Caveats.Budget,
		unitCode:     canonicalUnitCode(v.c.Caveats.UnitCode),
		copies:       v.c.clone().Copies,
	}
}

// StoredMeterSubject is what the Meter counts a capability against for a
// cost reported with no token in hand, read from the capability's record —
// records is the Store, or anything reading its records. It reads the record
// itself rather than take a Record, whose fields any caller can set, so the
// ceilings are the ones on record. A stored record names no
// Biscuit copy, so the subject carries none. A revoked capability still gives
// one: a cost already incurred belongs in the ledger. ErrNotFound when the
// capability is not on record.
func StoredMeterSubject(ctx context.Context, records RecordReader, capabilityID uuid.UUID) (MeterSubject, error) {
	rec, err := records.GetRecord(ctx, capabilityID)
	if err != nil {
		return MeterSubject{}, err
	}
	c := rec.Capability
	return MeterSubject{
		capabilityID: c.ID,
		tenantID:     c.Subject.TenantID,
		maxRequests:  c.Caveats.Requests,
		maxBudget:    c.Caveats.Budget,
		unitCode:     canonicalUnitCode(c.Caveats.UnitCode),
	}, nil
}

// CapabilityID is the capability the request counts against.
func (s MeterSubject) CapabilityID() uuid.UUID { return s.capabilityID }

// TenantID is the capability's tenant: its aggregate ceiling applies, and
// the ledger files the charge under it.
func (s MeterSubject) TenantID() uuid.UUID { return s.tenantID }

// Requests is the capability's own request ceiling.
func (s MeterSubject) Requests() RequestCeiling { return s.maxRequests }

// Budget is the capability's own budget ceiling.
func (s MeterSubject) Budget() BudgetCeiling { return s.maxBudget }

// UnitCode is the unit the capability counts in, normalised: empty reads
// as DefaultUnitCode.
func (s MeterSubject) UnitCode() string { return s.unitCode }

// Copies are the limits of the Biscuit copy presented, a copy of them.
func (s MeterSubject) Copies() []CopyCeiling { return cloneCopyCeilings(s.copies) }

func init() {
	link.NewMeterSubject = func(f link.MeterSubjectFields) any {
		copies, _ := f.Copies.([]CopyCeiling)
		requests, _ := f.Requests.(RequestCeiling)
		budget, _ := f.Budget.(BudgetCeiling)
		return MeterSubject{
			capabilityID: f.CapabilityID, tenantID: f.TenantID,
			maxRequests: requests, maxBudget: budget,
			unitCode: canonicalUnitCode(f.UnitCode), copies: copies,
		}
	}
}
