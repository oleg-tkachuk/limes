// Package link lets the module's own packages reach what the root does not
// export: limes/inspect the token reader, metertest, storetest, memstore
// and the tests of memstore and pgstore the constructors of a MeterSubject and
// a BiscuitCopy, and limestest the codes of each optional component. The
// root sets each as it initialises, which is before any code of theirs runs:
// each imports the root.
package link

import "uuid"

// DecodeToken reads a compact capability token's header and claims without
// checking its signature, returning a limes.Capability.
var DecodeToken func(token string) (any, error)

// MeterSubjectFields are the parts of a limes.MeterSubject.
type MeterSubjectFields struct {
	CapabilityID, TenantID uuid.UUID
	// Requests is a limes.RequestCeiling, Budget a limes.BudgetCeiling,
	// which carries the subject's unit.
	Requests, Budget any
	// Copies is a []limes.CopyCeiling, CopyQuota a limes.CopyQuota.
	Copies, CopyQuota any
}

// NewMeterSubject builds a limes.MeterSubject from its parts, for the
// module's own conformance suites and tests: a meter checked against
// ceilings no issuer signed. The root sets it as it initialises; outside the
// module only a verified capability makes one.
var NewMeterSubject func(MeterSubjectFields) any

// NewBiscuitCopy builds a limes.BiscuitCopy of a capability id and a
// revocation id, for the module's own conformance suites: a store checked
// against copies no verifier read. The root sets it as it initialises;
// outside the module only Verifier.BiscuitCopy makes one.
var NewBiscuitCopy func(capabilityID uuid.UUID, revocationID []byte) any

// ExchangeCodes and MeterCodes are the codes of the exchange's errors and of
// the meter's, each a limes.Code's string, in limes.Codes' order: what
// limestest leaves out of a check for a consumer that runs neither. The root
// sets them as it initialises.
var ExchangeCodes, MeterCodes func() []string
