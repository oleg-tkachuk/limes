package limes

import "uuid"

// Principal identifies the subject the capability is issued to. It is
// written into a token as the limes_principal claim (wire.go). Agent
// principals have a richer shape than service principals: they carry
// the run / tool-call lineage that audit and cost attribution need.
type Principal struct {
	// Type discriminates the principal kind.
	Type PrincipalType
	// TenantID is required for every principal — capability without
	// tenant scope is malformed and the verifier rejects it.
	TenantID uuid.UUID
	// Subject is the stable identity (user UUID, agent run ID, service
	// account name). Format depends on Type.
	Subject string
	// Agent fields are populated only for Type == PrincipalTypeAgent.
	Agent *AgentPrincipal
	_     noCompare
}

// PrincipalType discriminates Principal.Type.
type PrincipalType string

// The principal kinds. The zero value, PrincipalTypeUnspecified, names none
// and is refused wherever a kind is required.
const (
	PrincipalTypeUnspecified PrincipalType = ""
	PrincipalTypeUser        PrincipalType = "user"    // a person
	PrincipalTypeAgent       PrincipalType = "agent"   // an AI agent; Principal.Agent says which
	PrincipalTypeService     PrincipalType = "service" // a service account
)

// AgentPrincipal carries the fields specific to AI-agent principals.
// Threaded into audit + lineage so per-tool-call replay reconstructs
// the call graph without joining across half a dozen tables.
type AgentPrincipal struct {
	// AgentType is a human-meaningful label ("research-orchestrator",
	// "code-reviewer-bot"). Used for grouping, dashboards, anomaly
	// baselines.
	AgentType string
	// AgentVersion pins the build / git-sha of the agent runtime.
	AgentVersion string
	// RunID is a UUID minted by the agent host at session start.
	RunID uuid.UUID
	// ParentRunID is the RunID of the agent whose capability this one was
	// delegated from. Issuer.Delegate sets it from the parent, and refuses
	// a request naming another; zero for a root capability, or one
	// delegated from a principal that is no agent or names no run.
	ParentRunID uuid.UUID
	// Model identifies the LLM behind the agent ("example-model-1",
	// "gpt-5-pro"). Used for cost attribution.
	Model string
	// MCPClient is the agent host's user-agent ("example-host@1.4.0",
	// "cursor@0.44"). Useful for compatibility tracking.
	MCPClient string
	_         noCompare
}

// Clone is p with agent details of its own: a store keeps, and hands out,
// a principal no caller can alter through a pointer it kept.
func (p Principal) Clone() Principal { return p.clone() }

// clone is p with agent details of its own.
func (p Principal) clone() Principal {
	if p.Agent != nil {
		agent := *p.Agent
		p.Agent = &agent
	}
	return p
}
