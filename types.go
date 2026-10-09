package limes

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Errors exposed to callers. Mapped to transport errors by the consumer —
// not here. Verifier returns these so callers can switch.
var (
	ErrInvalidSignature  = errors.New("capability: invalid signature")
	ErrExpired           = errors.New("capability: expired")
	ErrNotYetValid       = errors.New("capability: not yet valid")
	ErrRevoked           = errors.New("capability: revoked")
	ErrCaveatViolation   = errors.New("capability: caveat violation")
	ErrAudienceMismatch  = errors.New("capability: audience mismatch")
	ErrBudgetExceeded    = errors.New("capability: budget exceeded")
	ErrDelegationTooWide = errors.New("capability: child wider than parent")
	// ErrUnitCodeMismatch — a delegated child capability declared a
	// different unit_code than its parent. We don't auto-convert
	// between currencies; charges flow through the system in their
	// declared unit and cross-currency delegation is rejected at
	// issuance.
	ErrUnitCodeMismatch = errors.New("capability: unit_code mismatch between parent and child")
	// ErrInvalidRequest is an Issue or Delegate request that is malformed in
	// itself: a missing tenant, an empty audience entry, a negative TTL, a
	// thumbprint that is not one, a not-before past the expiry, caveats that
	// do not validate. Nothing was minted. Every such error matches it, so a
	// caller maps the whole family to its "bad request" without listing it.
	ErrInvalidRequest = errors.New("capability: invalid request")
	// ErrUnknownTenant is an issuance for a tenant that does not exist —
	// typically one whose provisioning has not finished. The Store returns it.
	ErrUnknownTenant = errors.New("capability: unknown tenant")
	// ErrTenantDeleted is an issuance for a tenant in the trash: it exists,
	// so its rows may be restored, but nothing new is minted for it. The
	// Store returns it.
	ErrTenantDeleted = errors.New("capability: tenant deleted")
)

// requestError is one of the ErrInvalidRequest family with a name of its own.
type requestError struct{ msg string }

func (e *requestError) Error() string        { return e.msg }
func (e *requestError) Is(target error) bool { return target == ErrInvalidRequest }

// invalidRequest is an ErrInvalidRequest saying what was wrong.
func invalidRequest(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrInvalidRequest}, args...)...)
}

// DefaultUnitCode is the fallback when a request / row omits the
// unit. Keeps existing payloads / DB rows working without an explicit
// migration on the domain side.
const DefaultUnitCode = "USD"

// AbstractUnitCode is the unit of a budget that is not money — requests,
// credits, anything metered: ISO 4217's XXX, "no currency involved".
const AbstractUnitCode = "XXX"

// AllowedUnitCodes is the canonical set accepted for caveat / charge /
// tenant-budget unit_code fields: ISO 4217 codes, the abstract XXX among them.
var AllowedUnitCodes = []string{"USD", "EUR", "UAH", "GBP", AbstractUnitCode}

// IsAllowedUnitCode reports whether u is in AllowedUnitCodes. Empty
// string is NOT treated as valid here — callers that want the empty-
// means-default semantics should resolve through NormaliseUnitCode
// first.
func IsAllowedUnitCode(u string) bool {
	for _, allowed := range AllowedUnitCodes {
		if allowed == u {
			return true
		}
	}
	return false
}

// NormaliseUnitCode applies the empty-means-default policy and
// validates the result. Returns the canonical unit string and an
// error when the supplied non-empty value is unknown.
func NormaliseUnitCode(u string) (string, error) {
	if u == "" {
		return DefaultUnitCode, nil
	}
	if !IsAllowedUnitCode(u) {
		return "", fmt.Errorf("capability: unknown unit_code %q (allowed: %v)", u, AllowedUnitCodes)
	}
	return u, nil
}

// canonicalUnitCode is u as NormaliseUnitCode reads it, or u itself when it
// is unknown — for comparing two units, where an unknown one simply differs.
func canonicalUnitCode(u string) string {
	if n, err := NormaliseUnitCode(u); err == nil {
		return n
	}
	return u
}

// Op is an operation an agent may perform. The package defines a small
// built-in set (below); a consumer adds its own as namespaced names of the
// form "<namespace>:<name>" — "tool:search", "mcp:github/create_issue" —
// which Op.Validate accepts. A consumer-defined operation is treated as
// state-changing unless the server declares otherwise (Effect).
// Fine-grained method gating can still layer admin-authored policy on top.
type Op string

const (
	OpGet     Op = "get"
	OpPut     Op = "put"
	OpList    Op = "list"
	OpDelete  Op = "delete"
	OpPresign Op = "presign"
	OpTag     Op = "tag"
	OpSearch  Op = "search"
	OpEmbed   Op = "embed"
	OpShare   Op = "share"  // delegate a sub-capability to another agent
	OpManage  Op = "manage" // admin-tier (tenant CRUD, policy authoring)
)

// Principal identifies the subject the capability is issued to. Agent
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
	// Agent fields are populated only for Type == PrincipalAgent.
	Agent *AgentPrincipal
}

// PrincipalType discriminates Principal.Type.
type PrincipalType string

const (
	PrincipalUser    PrincipalType = "user"
	PrincipalAgent   PrincipalType = "agent"
	PrincipalService PrincipalType = "service"
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
	// ParentAgentID is set when this agent was spawned by another via
	// `OpShare`. Empty for top-level agents.
	ParentAgentID uuid.UUID
	// Model identifies the LLM behind the agent ("example-model-1",
	// "gpt-5-pro"). Used for cost attribution.
	Model string
	// MCPClient is the agent host's user-agent ("example-host@1.4.0",
	// "cursor@0.44"). Useful for compatibility tracking.
	MCPClient string
}

// Capability is the in-memory representation of a verified token. The
// wire form is a JWT; this struct is what callers receive from
// Verifier.Verify and what the Issuer fills before signing.
type Capability struct {
	// ID is the unique identifier (UUIDv7 — sortable by issuance time
	// so per-tenant indexes stay tight).
	ID uuid.UUID

	// Issuer is the service instance that minted the capability. Verifiers
	// accept tokens whose Issuer is in the trusted set.
	Issuer string

	// Subject is the principal the capability authorises.
	Subject Principal

	// Audience pins which services the capability is valid for.
	// Typical values: "data", "admin", "mcp". Verifiers reject tokens
	// whose audience does not include the calling plane.
	Audience []string

	// Caveats is the conjunction of restrictions — every caveat must
	// be satisfied by the request, and child capabilities can only
	// add further caveats, never relax one.
	Caveats Caveats

	// IssuedAt / NotBefore / ExpiresAt are standard JWT-style times.
	IssuedAt  time.Time
	NotBefore time.Time
	ExpiresAt time.Time

	// ParentID, if non-zero, points at the parent capability whose
	// holder delegated this one. Narrowing is checked once, at
	// delegation. At use, the lineage matters twice: revoking any
	// ancestor revokes this capability (Store.IsRevoked answers for the
	// whole chain), and every charge also debits each ancestor, so a
	// parent's budget bounds the spend of everything delegated from it.
	ParentID uuid.UUID

	// Generation is an issuer-assigned counter (1 for a fresh root, 1 for
	// every delegated child) carried in the token for audit. A consumer
	// that rotates a capability may reissue with Generation+1 and revoke
	// the previous one, and compare generations in its own records.
	//
	// It is NOT a fence token: nothing in this package compares it
	// against a stored current value, so an older generation stays usable
	// until it expires or is revoked. Revocation is the mechanism that
	// stops a superseded capability.
	Generation int64

	// ConfirmationJKT, when set, binds the capability to a key: the RFC 7638
	// thumbprint of the public key whose holder alone may present it. Every
	// request must then carry a DPoP proof signed by that key (dpop.go). A
	// delegated child of a bound capability is bound too. Carried in the
	// token as `cnf.jkt`, and absent from tokens that are not bound.
	ConfirmationJKT string

	// BiscuitRoot is set only on the token sealed inside a Biscuit (see
	// Issuer.Biscuit): the base64url Ed25519 public key that roots the
	// Biscuit's signature chain. A token carrying it is refused when
	// presented on its own, so it cannot be lifted out of an attenuated
	// Biscuit to shed the attenuation.
	BiscuitRoot string

	// Copies are the request and budget limits the attenuation blocks of a
	// verified Biscuit set, innermost block first. Each is counted on its
	// own, beside the capability's counters, by every Meter call that is
	// handed them. Set by the verifier only; never part of a token or a
	// stored record.
	Copies []CopyCeiling `json:"-"`
}

// CopyCeiling is a limit one attenuation block of a Biscuit set on the copy
// it ends and every copy attenuated from it, all of which carry the block's
// revocation id. Its counters are keyed by that id, so siblings attenuated
// apart count apart, while the capability's own counters still bound them
// all together.
type CopyCeiling struct {
	// RevocationID is the block's revocation id.
	RevocationID []byte
	// MaxRequests bounds the requests made with the copy; 0 = no limit here.
	MaxRequests int64
	// MaxBudget bounds the copy's spend, in the capability's unit; 0 = no
	// limit here.
	MaxBudget Nanos
}

// Caveats is a typed bag of restrictions. Empty values are interpreted
// as "no restriction on that axis"; explicit bounds are AND-combined.
//
// A child capability may only narrow caveats — set a tighter prefix,
// drop ops, lower the budget, shorten TTL. The verifier enforces this
// at delegation time, not just at use time, so attempts to widen are
// detected at issue and rejected.
type Caveats struct {
	// Ops is the set of operations the capability authorises. The
	// issuer rejects an empty set: a capability that allows nothing is
	// a mistake, not a credential.
	Ops []Op

	// ResourcePrefixes restricts the capability to URIs under any prefix
	// in the slice, matched at a "/" segment boundary (MatchResource):
	// "a/b" covers "a/b" and "a/b/c" but not "a/bc". Empty slice =
	// unrestricted within the tenant; an empty ENTRY is invalid.
	ResourcePrefixes []string

	// ResourceURIs pins the capability to specific URIs. Used by
	// hand-off flows where the parent passes exactly the artifacts the
	// child needs.
	ResourceURIs []string

	// MaxRequests caps the total number of times the capability may
	// be used. 0 = unlimited within the TTL.
	MaxRequests int

	// MaxBudgetAmount is the cost budget the capability authorises, in
	// the currency identified by UnitCode, exact to the nano. The Meter
	// counts spend against it; once exhausted, a charge returns
	// ErrBudgetExceeded.
	//
	// Renamed from MaxBudgetUSD — same field, no longer USD-pinned.
	MaxBudgetAmount Nanos

	// UnitCode pins the currency or unit: an ISO 4217 code, USD/EUR/UAH/GBP,
	// or XXX ("no currency") for a budget that is not money. Empty value is
	// interpreted as the default ("USD") at the application boundary; the
	// in-memory shape is happy to carry either form.
	UnitCode string

	// AllowTaintedRead permits non-mutating operations on resources the
	// consumer has flagged with prompt-injection / PII / secrets signals.
	// Off by default. Enforced by Caveats.Check, which can only act on a
	// taint signal the consumer supplies in CheckRequest.ResourceTainted
	// — a consumer that tracks no taint flags gets no protection from
	// this caveat, and should say so rather than imply it.
	AllowTaintedRead bool

	// IdempotencyKeyRequired forces the bearer to send an explicit
	// idempotency key on mutating operations (CheckRequest.Mutating).
	// Enforced by Caveats.Check from CheckRequest.HasIdempotencyKey.
	IdempotencyKeyRequired bool

	// SourceIPCIDR optionally pins the capability to clients whose
	// observed IP is in the listed CIDRs. Empty = unconstrained.
	// Enforced by Caveats.CheckSource; delegation requires each child
	// network to lie inside a parent network.
	SourceIPCIDR []string
}
