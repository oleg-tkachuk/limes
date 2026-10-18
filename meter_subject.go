package limes

import (
	"context"
	"fmt"

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
	copies       []CopyCeiling
}

// MeterSubject is what the Meter counts this capability's requests against.
func (v VerifiedCapability) MeterSubject() MeterSubject {
	return MeterSubject{
		capabilityID: v.c.ID,
		tenantID:     v.c.Subject.TenantID,
		maxRequests:  v.c.Caveats.Requests,
		maxBudget:    v.c.Caveats.Budget,
		copies:       cloneCopyCeilings(v.copies),
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

// UnitCode is the unit the capability counts in, its budget's; empty when
// that names none, and then it counts requests alone.
func (s MeterSubject) UnitCode() string { return s.maxBudget.UnitCode }

// Copies are the limits of the Biscuit copy presented, a copy of them.
func (s MeterSubject) Copies() []CopyCeiling { return cloneCopyCeilings(s.copies) }

// validate refuses a subject that names no capability or no tenant — the
// zero MeterSubject, a request that set none — or an unknown unit.
func (s MeterSubject) validate() error {
	if s.capabilityID == uuid.Nil || s.tenantID == uuid.Nil {
		return invalidRequest("the meter subject names no capability or no tenant")
	}
	return validUnitCode(s.maxBudget.UnitCode)
}

// validateAmounts is validate for a request that moves an amount, which
// needs a unit to count it in: no unit is ever assumed.
func (s MeterSubject) validateAmounts() error {
	if err := s.validate(); err != nil {
		return err
	}
	if s.maxBudget.UnitCode == "" {
		return invalidRequest("capability %s names no unit_code, so it counts no amount", s.capabilityID)
	}
	return nil
}

func init() {
	link.NewMeterSubject = func(f link.MeterSubjectFields) any {
		copies := linkedPart[[]CopyCeiling]("Copies", f.Copies)
		requests := linkedPart[RequestCeiling]("Requests", f.Requests)
		budget := linkedPart[BudgetCeiling]("Budget", f.Budget)
		return MeterSubject{
			capabilityID: f.CapabilityID, tenantID: f.TenantID,
			maxRequests: requests, maxBudget: budget, copies: copies,
		}
	}
}

// linkedPart is the part v of link.MeterSubjectFields named name, as a T. A
// part left out, nil, is T's zero value. One of another type is the module's
// own wiring gone wrong, and panics: read as none, a missing copy ceiling
// would let a copy spend past its limits.
func linkedPart[T any](name string, v any) T {
	if v == nil {
		var zero T
		return zero
	}
	t, ok := v.(T)
	if !ok {
		panic(fmt.Sprintf("limes: link.MeterSubjectFields.%s is a %T, not a %T", name, v, t))
	}
	return t
}
