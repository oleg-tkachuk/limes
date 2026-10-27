package limes

import (
	"encoding/json/v2"
	"uuid"
)

// The token's claims are these unexported structs, never the exported types
// a caller holds: renaming a field of Principal, AgentPrincipal or Caveats is
// a Go change, and only an edit here changes what a token says. Their json
// tags are the wire format, pinned by the golden token.

// wirePrincipal is the limes_principal claim.
type wirePrincipal struct {
	Type     PrincipalType `json:"type"`
	TenantID uuid.UUID     `json:"tenant_id"`
	Subject  string        `json:"subject"`
	Agent    *wireAgent    `json:"agent,omitzero"`
	_        noCompare
}

// wireAgent is limes_principal.agent.
type wireAgent struct {
	AgentType    string    `json:"agent_type,omitempty"`
	AgentVersion string    `json:"agent_version,omitempty"`
	RunID        uuid.UUID `json:"run_id,omitzero"`
	ParentRunID  uuid.UUID `json:"parent_run_id,omitzero"`
	Model        string    `json:"model,omitempty"`
	MCPClient    string    `json:"mcp_client,omitempty"`
	_            noCompare
}

// wireCaveats is the limes_caveats claim.
type wireCaveats struct {
	Ops                    []Op     `json:"ops"`
	ResourcePrefixes       []string `json:"resource_prefixes,omitempty"`
	ResourceURIs           []string `json:"resource_uris,omitempty"`
	ResourceNamePrefixes   []string `json:"resource_name_prefixes,omitempty"`
	MaxRequests            int64    `json:"max_requests,omitzero"`
	UnlimitedRequests      bool     `json:"unlimited_requests,omitzero"`
	MaxBudget              Nanos    `json:"max_budget,omitzero"`
	UnlimitedBudget        bool     `json:"unlimited_budget,omitzero"`
	UnitCode               string   `json:"unit_code,omitempty"`
	MaxCopies              int64    `json:"max_copies,omitzero"`
	UnlimitedCopies        bool     `json:"unlimited_copies,omitzero"`
	AllowTaintedRead       bool     `json:"allow_tainted_read,omitzero"`
	IdempotencyKeyRequired bool     `json:"idempotency_key_required,omitzero"`
	SourceIPCIDR           []string `json:"source_ip_cidr,omitempty"`
}

// wireAgent has exactly the fields of AgentPrincipal, so each converts to the
// other: a field added to one and not the other stops the build. wireCaveats
// spells each ceiling as two members — an absent max_requests is a ceiling
// of 0, and only unlimited_requests lifts it — so it is converted field by
// field, and a test holds the two to the same fields.

func toWirePrincipal(p Principal) wirePrincipal {
	w := wirePrincipal{Type: p.Type, TenantID: p.TenantID, Subject: p.Subject}
	if p.Agent != nil {
		a := wireAgent(*p.Agent)
		w.Agent = &a
	}
	return w
}

func (w wirePrincipal) principal() Principal {
	p := Principal{Type: w.Type, TenantID: w.TenantID, Subject: w.Subject}
	if w.Agent != nil {
		a := AgentPrincipal(*w.Agent)
		p.Agent = &a
	}
	return p
}

// MarshalCaveats encodes c as a token's limes_caveats claim carries it, for
// a reader outside limes to be shown the same members a token holds, such as
// an RFC 7662 introspection answer.
func MarshalCaveats(c Caveats) ([]byte, error) {
	return json.Marshal(toWireCaveats(c))
}

func toWireCaveats(c Caveats) wireCaveats {
	return wireCaveats{
		Ops: c.Ops, ResourcePrefixes: c.ResourcePrefixes, ResourceURIs: c.ResourceURIs,
		ResourceNamePrefixes: c.ResourceNamePrefixes,
		MaxRequests:          c.Requests.Max, UnlimitedRequests: c.Requests.Unlimited,
		MaxBudget: c.Budget.Max, UnlimitedBudget: c.Budget.Unlimited,
		UnitCode: c.Budget.UnitCode, AllowTaintedRead: c.AllowTaintedRead,
		MaxCopies: c.Copies.Max, UnlimitedCopies: c.Copies.Unlimited,
		IdempotencyKeyRequired: c.IdempotencyKeyRequired, SourceIPCIDR: c.SourceIPCIDR,
	}
}

func (w wireCaveats) caveats() Caveats {
	return Caveats{
		Ops: w.Ops, ResourcePrefixes: w.ResourcePrefixes, ResourceURIs: w.ResourceURIs,
		ResourceNamePrefixes:   w.ResourceNamePrefixes,
		Requests:               RequestCeiling{Max: w.MaxRequests, Unlimited: w.UnlimitedRequests},
		Budget:                 BudgetCeiling{Max: w.MaxBudget, Unlimited: w.UnlimitedBudget, UnitCode: w.UnitCode},
		Copies:                 CopyQuota{Max: w.MaxCopies, Unlimited: w.UnlimitedCopies},
		AllowTaintedRead:       w.AllowTaintedRead,
		IdempotencyKeyRequired: w.IdempotencyKeyRequired, SourceIPCIDR: w.SourceIPCIDR,
	}
}
