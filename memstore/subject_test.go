package memstore

import (
	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/internal/link"
)

// subjectSpec is a meter subject's parts.
type subjectSpec struct {
	capID       uuid.UUID
	tenant      uuid.UUID
	maxRequests int64
	maxBudget   limes.Nanos
	unit        string
	copies      []limes.CopyCeiling
}

// newSubject is the MeterSubject a verified capability with spec's parts
// would give, so a test can meter against ceilings no issuer signed.
func newSubject(spec subjectSpec) limes.MeterSubject {
	return link.NewMeterSubject(link.MeterSubjectFields{
		CapabilityID: spec.capID, TenantID: spec.tenant, MaxRequests: spec.maxRequests,
		MaxBudgetAmount: int64(spec.maxBudget), UnitCode: spec.unit, Copies: spec.copies,
	}).(limes.MeterSubject)
}
