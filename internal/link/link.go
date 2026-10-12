// Package link lets limes/inspect reach the root package's token reader
// without the root exporting it. The root sets DecodeToken as it
// initialises, which is before any code of inspect runs: inspect imports the
// root.
package link

import "github.com/google/uuid"

// DecodeToken reads a compact capability token's header and claims without
// checking its signature, returning a limes.Capability.
var DecodeToken func(token string) (any, error)

// MeterSubjectFields are the parts of a limes.MeterSubject.
type MeterSubjectFields struct {
	CapabilityID, TenantID uuid.UUID
	// Requests is a limes.RequestCeiling, Budget a limes.BudgetCeiling.
	Requests, Budget any
	UnitCode         string
	// Copies is a []limes.CopyCeiling.
	Copies any
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
