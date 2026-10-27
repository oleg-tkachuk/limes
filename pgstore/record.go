package pgstore

import (
	"encoding/json"
	"fmt"
	"time"
	"uuid"

	"github.com/oleg-tkachuk/limes"
)

// The jsonb columns hold a capability and a principal in this package's own
// shape. limes leaves its types without json tags on purpose — how a record is
// stored is the store's choice — so these structs are that choice, field by
// field. storetest reads every field back, so one that a struct here lacks
// fails it.

// capabilityRecord is a limes.Capability as the capability column holds it.
type capabilityRecord struct {
	ID              uuid.UUID       `json:"id"`
	Issuer          string          `json:"issuer"`
	Subject         principalRecord `json:"subject"`
	Audience        []string        `json:"audience"`
	Caveats         caveatsRecord   `json:"caveats"`
	IssuedAt        time.Time       `json:"issued_at"`
	NotBefore       time.Time       `json:"not_before"`
	ExpiresAt       time.Time       `json:"expires_at"`
	ParentID        uuid.UUID       `json:"parent_id"`
	ConfirmationJKT string          `json:"confirmation_jkt"`
	// ClientID is absent from a record stored before it existed: none.
	ClientID string `json:"client_id"`
}

type principalRecord struct {
	Type     limes.PrincipalType `json:"type"`
	TenantID uuid.UUID           `json:"tenant_id"`
	Subject  string              `json:"subject"`
	Agent    *agentRecord        `json:"agent"`
}

type agentRecord struct {
	AgentType    string    `json:"agent_type"`
	AgentVersion string    `json:"agent_version"`
	RunID        uuid.UUID `json:"run_id"`
	// ParentRunID keeps the key it was first stored under.
	ParentRunID uuid.UUID `json:"parent_agent_id"`
	Model       string    `json:"model"`
	MCPClient   string    `json:"mcp_client"`
}

type caveatsRecord struct {
	Ops                    []limes.Op    `json:"ops"`
	ResourcePrefixes       []string      `json:"resource_prefixes"`
	ResourceURIs           []string      `json:"resource_uris"`
	ResourceNamePrefixes   []string      `json:"resource_name_prefixes"`
	Requests               ceilingRecord `json:"requests"`
	Budget                 ceilingRecord `json:"budget"`
	Copies                 ceilingRecord `json:"copies"`
	UnitCode               string        `json:"unit_code"`
	AllowTaintedRead       bool          `json:"allow_tainted_read"`
	IdempotencyKeyRequired bool          `json:"idempotency_key_required"`
	SourceIPCIDR           []string      `json:"source_ip_cidr"`
}

// ceilingRecord is a RequestCeiling, a BudgetCeiling or a CopyQuota: a Max,
// or Unlimited.
type ceilingRecord struct {
	Max       int64 `json:"max"`
	Unlimited bool  `json:"unlimited"`
}

func toPrincipalRecord(p limes.Principal) principalRecord {
	r := principalRecord{Type: p.Type, TenantID: p.TenantID, Subject: p.Subject}
	if a := p.Agent; a != nil {
		r.Agent = &agentRecord{
			AgentType: a.AgentType, AgentVersion: a.AgentVersion, RunID: a.RunID,
			ParentRunID: a.ParentRunID, Model: a.Model, MCPClient: a.MCPClient,
		}
	}
	return r
}

func (r principalRecord) principal() limes.Principal {
	p := limes.Principal{Type: r.Type, TenantID: r.TenantID, Subject: r.Subject}
	if a := r.Agent; a != nil {
		p.Agent = &limes.AgentPrincipal{
			AgentType: a.AgentType, AgentVersion: a.AgentVersion, RunID: a.RunID,
			ParentRunID: a.ParentRunID, Model: a.Model, MCPClient: a.MCPClient,
		}
	}
	return p
}

func toCapabilityRecord(c limes.Capability) capabilityRecord {
	cv := c.Caveats
	return capabilityRecord{
		ID: c.ID, Issuer: c.Issuer, Subject: toPrincipalRecord(c.Subject), Audience: c.Audience,
		Caveats: caveatsRecord{
			Ops: cv.Ops, ResourcePrefixes: cv.ResourcePrefixes, ResourceURIs: cv.ResourceURIs,
			ResourceNamePrefixes: cv.ResourceNamePrefixes,
			Requests:             ceilingRecord{Max: cv.Requests.Max, Unlimited: cv.Requests.Unlimited},
			Budget:               ceilingRecord{Max: int64(cv.Budget.Max), Unlimited: cv.Budget.Unlimited},
			Copies:               ceilingRecord{Max: cv.Copies.Max, Unlimited: cv.Copies.Unlimited},
			UnitCode:             cv.Budget.UnitCode, AllowTaintedRead: cv.AllowTaintedRead,
			IdempotencyKeyRequired: cv.IdempotencyKeyRequired, SourceIPCIDR: cv.SourceIPCIDR,
		},
		IssuedAt: c.IssuedAt, NotBefore: c.NotBefore, ExpiresAt: c.ExpiresAt, ParentID: c.ParentID,
		ConfirmationJKT: c.ConfirmationJKT, ClientID: c.ClientID,
	}
}

func (r capabilityRecord) capability() limes.Capability {
	cv := r.Caveats
	return limes.Capability{
		ID: r.ID, Issuer: r.Issuer, Subject: r.Subject.principal(), Audience: r.Audience,
		Caveats: limes.Caveats{
			Ops: cv.Ops, ResourcePrefixes: cv.ResourcePrefixes, ResourceURIs: cv.ResourceURIs,
			ResourceNamePrefixes:   cv.ResourceNamePrefixes,
			Requests:               limes.RequestCeiling{Max: cv.Requests.Max, Unlimited: cv.Requests.Unlimited},
			Budget:                 limes.BudgetCeiling{Max: limes.Nanos(cv.Budget.Max), Unlimited: cv.Budget.Unlimited, UnitCode: cv.UnitCode},
			Copies:                 limes.CopyQuota{Max: cv.Copies.Max, Unlimited: cv.Copies.Unlimited},
			AllowTaintedRead:       cv.AllowTaintedRead,
			IdempotencyKeyRequired: cv.IdempotencyKeyRequired, SourceIPCIDR: cv.SourceIPCIDR,
		},
		IssuedAt: r.IssuedAt, NotBefore: r.NotBefore, ExpiresAt: r.ExpiresAt, ParentID: r.ParentID,
		ConfirmationJKT: r.ConfirmationJKT, ClientID: r.ClientID,
	}
}

// encodeCapability is c as the capability column holds it.
func encodeCapability(c limes.Capability) ([]byte, error) {
	b, err := json.Marshal(toCapabilityRecord(c))
	if err != nil {
		return nil, fmt.Errorf("pgstore: encode capability %s: %w", c.ID, err)
	}
	return b, nil
}

// encodePrincipal is p as the issued_by column holds it.
func encodePrincipal(p limes.Principal) ([]byte, error) {
	b, err := json.Marshal(toPrincipalRecord(p))
	if err != nil {
		return nil, fmt.Errorf("pgstore: encode principal: %w", err)
	}
	return b, nil
}

func decodeCapability(b []byte) (limes.Capability, error) {
	var r capabilityRecord
	if err := json.Unmarshal(b, &r); err != nil {
		return limes.Capability{}, fmt.Errorf("pgstore: decode capability: %w", err)
	}
	return r.capability(), nil
}

func decodePrincipal(b []byte) (limes.Principal, error) {
	var r principalRecord
	if err := json.Unmarshal(b, &r); err != nil {
		return limes.Principal{}, fmt.Errorf("pgstore: decode principal: %w", err)
	}
	return r.principal(), nil
}
