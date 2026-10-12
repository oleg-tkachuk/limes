package limes

import "github.com/google/uuid"

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
}

// wireAgent is limes_principal.agent.
type wireAgent struct {
	AgentType     string    `json:"agent_type,omitempty"`
	AgentVersion  string    `json:"agent_version,omitempty"`
	RunID         uuid.UUID `json:"run_id,omitzero"`
	ParentAgentID uuid.UUID `json:"parent_agent_id,omitzero"`
	Model         string    `json:"model,omitempty"`
	MCPClient     string    `json:"mcp_client,omitempty"`
}

// wireCaveats is the limes_caveats claim.
type wireCaveats struct {
	Ops                    []Op     `json:"ops"`
	ResourcePrefixes       []string `json:"resource_prefixes,omitempty"`
	ResourceURIs           []string `json:"resource_uris,omitempty"`
	MaxRequests            int64    `json:"max_requests,omitzero"`
	MaxBudgetAmount        Nanos    `json:"max_budget,omitzero"`
	UnitCode               string   `json:"unit_code,omitempty"`
	AllowTaintedRead       bool     `json:"allow_tainted_read,omitzero"`
	IdempotencyKeyRequired bool     `json:"idempotency_key_required,omitzero"`
	SourceIPCIDR           []string `json:"source_ip_cidr,omitempty"`
}

// wireAgent and wireCaveats have exactly the fields of AgentPrincipal and
// Caveats, so each converts to the other: a field added to one and not the
// other stops the build.

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

func toWireCaveats(c Caveats) wireCaveats { return wireCaveats(c) }

func (w wireCaveats) caveats() Caveats { return Caveats(w) }
