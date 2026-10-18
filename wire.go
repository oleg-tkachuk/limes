package limes

import (
	"fmt"

	"github.com/google/uuid"
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

// wireAgent is limes_principal.agent. The parent's run is parent_run_id in
// v5 and parent_agent_id in v4; each type refuses the other's member.
type wireAgent struct {
	AgentType    string    `json:"agent_type,omitempty"`
	AgentVersion string    `json:"agent_version,omitempty"`
	RunID        uuid.UUID `json:"run_id,omitzero"`
	ParentRunID  uuid.UUID `json:"parent_run_id,omitzero"`
	Model        string    `json:"model,omitempty"`
	MCPClient    string    `json:"mcp_client,omitempty"`
	// ParentAgentID is ParentRunID as TokenTypeV4 spells it.
	ParentAgentID uuid.UUID `json:"parent_agent_id,omitzero"`
	_             noCompare
}

// The member a v4 token names the parent's run by.
const claimParentAgentID = "parent_agent_id"

// wireCaveats is the limes_caveats claim.
type wireCaveats struct {
	Ops                    []Op     `json:"ops"`
	ResourcePrefixes       []string `json:"resource_prefixes,omitempty"`
	ResourceURIs           []string `json:"resource_uris,omitempty"`
	MaxRequests            int64    `json:"max_requests,omitzero"`
	UnlimitedRequests      bool     `json:"unlimited_requests,omitzero"`
	MaxBudget              Nanos    `json:"max_budget,omitzero"`
	UnlimitedBudget        bool     `json:"unlimited_budget,omitzero"`
	UnitCode               string   `json:"unit_code,omitempty"`
	AllowTaintedRead       bool     `json:"allow_tainted_read,omitzero"`
	IdempotencyKeyRequired bool     `json:"idempotency_key_required,omitzero"`
	SourceIPCIDR           []string `json:"source_ip_cidr,omitempty"`
}

// wireAgent has the fields of AgentPrincipal and v4's spelling of one of
// them, and a test holds the two to the same fields. wireCaveats
// spells each ceiling as two members — an absent max_requests is a ceiling
// of 0, and only unlimited_requests lifts it — so it is converted field by
// field, and a test holds the two to the same fields.

// toWirePrincipal is p as a token of type typ carries it.
func toWirePrincipal(p Principal, typ TokenType) wirePrincipal {
	w := wirePrincipal{Type: p.Type, TenantID: p.TenantID, Subject: p.Subject}
	if a := p.Agent; a != nil {
		w.Agent = &wireAgent{
			AgentType: a.AgentType, AgentVersion: a.AgentVersion, RunID: a.RunID,
			ParentRunID: a.ParentRunID, Model: a.Model, MCPClient: a.MCPClient,
		}
		if typ == TokenTypeV4 {
			w.Agent.ParentAgentID, w.Agent.ParentRunID = a.ParentRunID, uuid.Nil
		}
	}
	return w
}

// principal is the principal a token of type typ carries, refusing the
// member of the parent's run the type does not spell it with.
func (w wirePrincipal) principal(typ TokenType) (Principal, error) {
	p := Principal{Type: w.Type, TenantID: w.TenantID, Subject: w.Subject}
	a := w.Agent
	if a == nil {
		return p, nil
	}
	parent := a.ParentRunID
	switch {
	case typ == TokenTypeV4 && a.ParentRunID != uuid.Nil:
		return Principal{}, fmt.Errorf("limes: %s carries parent_run_id; v4 spells it %s", typ, claimParentAgentID)
	case typ == TokenTypeV4:
		parent = a.ParentAgentID
	case a.ParentAgentID != uuid.Nil:
		return Principal{}, fmt.Errorf("limes: %s carries %s; it spells it parent_run_id", typ, claimParentAgentID)
	}
	p.Agent = &AgentPrincipal{
		AgentType: a.AgentType, AgentVersion: a.AgentVersion, RunID: a.RunID,
		ParentRunID: parent, Model: a.Model, MCPClient: a.MCPClient,
	}
	return p, nil
}

func toWireCaveats(c Caveats) wireCaveats {
	return wireCaveats{
		Ops: c.Ops, ResourcePrefixes: c.ResourcePrefixes, ResourceURIs: c.ResourceURIs,
		MaxRequests: c.Requests.Max, UnlimitedRequests: c.Requests.Unlimited,
		MaxBudget: c.Budget.Max, UnlimitedBudget: c.Budget.Unlimited,
		UnitCode: c.Budget.UnitCode, AllowTaintedRead: c.AllowTaintedRead,
		IdempotencyKeyRequired: c.IdempotencyKeyRequired, SourceIPCIDR: c.SourceIPCIDR,
	}
}

func (w wireCaveats) caveats() Caveats {
	return Caveats{
		Ops: w.Ops, ResourcePrefixes: w.ResourcePrefixes, ResourceURIs: w.ResourceURIs,
		Requests:               RequestCeiling{Max: w.MaxRequests, Unlimited: w.UnlimitedRequests},
		Budget:                 BudgetCeiling{Max: w.MaxBudget, Unlimited: w.UnlimitedBudget, UnitCode: w.UnitCode},
		AllowTaintedRead:       w.AllowTaintedRead,
		IdempotencyKeyRequired: w.IdempotencyKeyRequired, SourceIPCIDR: w.SourceIPCIDR,
	}
}
