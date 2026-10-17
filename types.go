package limes

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"golang.org/x/text/currency"
)

// Errors exposed to callers, which switch on them with errors.Is. A
// consumer maps them to its transport, or lets Verifier.ErrorResponse
// map them to HTTP.
//
// Some are families that more specific sentinels match as well, so a caller
// branches on the one it needs:
//
//	ErrUnauthenticated       the presented credentials were refused (401)
//	├── ErrNoCredentials     none were presented
//	├── ErrInvalidToken      the token is not genuine, or not this verifier's
//	│   └── ErrBiscuitAttenuation
//	├── ErrProofOfPossession the proof of possession is missing or wrong
//	│   └── ErrAuthSchemeMismatch
//	├── ErrRevoked
//	│   └── ErrDelegationTooDeep
//	└── ErrExpired, ErrNotYetValid, ErrAudienceMismatch
//	ErrCaveatViolation       a caveat refuses the operation (403)
//	ErrBudgetExceeded, ErrTenantBudgetExceeded, ErrRequestLimitExceeded
//	                         a ceiling refuses the request (403)
//	ErrInvalidRequest        the request itself is malformed
//	├── ErrInvalidCaveats, ErrInvalidAmount, ErrEffectConflict
//	└── ErrMalformedAuthorization  the client's Authorization header (400)
//	ErrNotFound              what the request names is not on record
//	└── ErrChargeNotFound, ErrReservationNotFound, ErrUsageNotFound, …
var (
	// ErrUnauthenticated is the family of every refusal of the presented
	// credentials, token or proof: the one errors.Is a consumer needs to
	// answer them all as unauthenticated.
	ErrUnauthenticated = errors.New("limes: unauthenticated")
	// ErrInvalidToken — the token is not genuine or not this verifier's
	// to accept: a bad signature, an unknown kid, an untrusted issuer, a
	// malformed header or claims. One sentinel for all, so a caller cannot
	// tell an attacker which check failed.
	ErrInvalidToken error = &familyError{"limes: invalid token", ErrUnauthenticated}
	// ErrProofOfPossession is the family of every proof-of-possession
	// refusal: the token may be genuine, the proof that its holder sent it
	// is what failed. The verifier wraps each dpop refusal in it.
	ErrProofOfPossession error = &familyError{"limes: proof of possession refused", ErrUnauthenticated}
	ErrExpired           error = &familyError{"limes: expired", ErrUnauthenticated}
	ErrNotYetValid       error = &familyError{"limes: not yet valid", ErrUnauthenticated}
	ErrRevoked           error = &familyError{"limes: revoked", ErrUnauthenticated}
	// ErrDelegationTooDeep — a capability with more than MaxDelegationDepth
	// ancestors on record. Its root lies past the bounded IsRevoked walk,
	// so a revocation there would go unseen: it is refused as revoked,
	// never accepted, and Delegate refuses to mint one. A store answers it
	// as an error, which no revocation cache keeps.
	ErrDelegationTooDeep error = &familyError{"limes: delegation chain too deep", ErrRevoked}
	ErrAudienceMismatch  error = &familyError{"limes: audience mismatch", ErrUnauthenticated}
	ErrCaveatViolation         = errors.New("limes: caveat violation")
	ErrBudgetExceeded          = errors.New("limes: budget exceeded")
	ErrDelegationTooWide       = errors.New("limes: child wider than parent")
	// ErrUnitCodeMismatch — two units that would have to be one: a
	// delegated child declaring a unit_code other than its parent's, or an
	// amount in a unit other than the one a counter (a capability's, an
	// ancestor's, a tenant's) already counts in. Nothing converts between
	// currencies, so cross-currency delegation is refused at issuance and a
	// cross-currency charge at the Meter.
	ErrUnitCodeMismatch = errors.New("limes: unit_code mismatch")
	// ErrInvalidRequest is a request malformed in itself: a missing tenant,
	// an empty audience entry, a negative TTL, a thumbprint that is not one,
	// a not-before past the expiry, caveats or an amount that do not
	// validate. Nothing was done. Every such error matches it, so a caller
	// maps the whole family to its "bad request" without listing it. A
	// request that is well formed but asks for more than is allowed has its
	// own sentinel instead: ErrDelegationTooWide, ErrUnitCodeMismatch.
	ErrInvalidRequest = errors.New("limes: invalid request")
	// ErrUnknownTenant is an issuance for a tenant that does not exist —
	// typically one whose provisioning has not finished. The Store returns it.
	ErrUnknownTenant = errors.New("limes: unknown tenant")
	// ErrTenantDeleted is an issuance for a tenant in the trash: it exists,
	// so its rows may be restored, but nothing new is minted for it. The
	// Store returns it.
	ErrTenantDeleted = errors.New("limes: tenant deleted")
	// ErrScopeMismatch is a store call whose context is scoped to a tenant
	// other than the one its request names — or to none — under a store
	// that keeps each tenant's rows apart. The rows are there; the call may
	// not see them. It is the caller's wiring that is wrong, not the
	// request, so it matches neither ErrInvalidRequest nor ErrNotFound, and
	// ErrorResponse answers it 500.
	ErrScopeMismatch = errors.New("limes: call scoped to another tenant than its request names")
	// ErrProofUnsupported is a key-bound capability presented to a verifier
	// with no DPoP configured: no proof the client sends could be checked.
	// It is the deployment's fault, not the request's, so it matches no
	// ErrUnauthenticated sentinel and ErrorResponse invites no retry.
	ErrProofUnsupported = errors.New("limes: key-bound capability, and this verifier checks no proof")
)

// familyError is a sentinel with a name of its own that also matches the
// family it belongs to, and that family's own: errors.Is(dpop.ErrReplayed,
// ErrProofOfPossession) and errors.Is(dpop.ErrReplayed, ErrUnauthenticated)
// both hold.
type familyError struct {
	msg    string
	family error
}

func (e *familyError) Error() string { return e.msg }
func (e *familyError) Is(target error) bool {
	return target == e.family || errors.Is(e.family, target)
}

// invalidRequest is an ErrInvalidRequest saying what was wrong.
func invalidRequest(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrInvalidRequest}, args...)...)
}

// AbstractUnitCode is the unit of a budget that is not money — requests,
// credits, anything metered: ISO 4217's XXX, "no currency involved".
const AbstractUnitCode = "XXX"

// IsUnitCode reports whether u is an ISO 4217 currency code, in its
// canonical upper-case spelling: "USD", "JPY", "XXX". Empty is not one: it
// names no unit, and no unit is ever assumed for it.
func IsUnitCode(u string) bool {
	unit, err := currency.ParseISO(u)
	return err == nil && unit.String() == u
}

// validUnitCode refuses a unit that is neither empty — none named — nor an
// ISO 4217 code.
func validUnitCode(u string) error {
	if u != "" && !IsUnitCode(u) {
		return invalidRequest("unit_code %q is not an ISO 4217 code in upper case", u)
	}
	return nil
}

// Op is an operation an agent may perform. The package defines a small
// built-in set (below); a consumer adds its own as namespaced names of the
// form "<namespace>:<name>" — "tool:search", "mcp:github/create_issue" —
// which Op.Validate accepts. A consumer-defined operation is treated as
// state-changing unless the server declares otherwise (Effect).
// Fine-grained method gating can still layer admin-authored policy on top.
type Op string

// The built-in operations. Get, List, Search and Embed read; the rest may
// change state (Op.ResolveEffect).
const (
	OpGet     Op = "get"     // read one resource
	OpPut     Op = "put"     // write one resource
	OpList    Op = "list"    // list resources under a prefix
	OpDelete  Op = "delete"  // delete a resource
	OpPresign Op = "presign" // mint a pre-signed URL for a resource
	OpTag     Op = "tag"     // label a resource
	OpSearch  Op = "search"  // query an index
	OpEmbed   Op = "embed"   // compute embeddings
	OpShare   Op = "share"   // delegate a sub-capability to another agent through the issuer; not needed to attenuate a Biscuit copy
	OpManage  Op = "manage"  // administer: tenants, policy
)

// noCompare makes a struct that holds it not comparable with ==. The structs
// that may gain a field in 1.x carry one, so a field of a type == cannot
// compare — a slice, a map — is an addition rather than a break: no caller
// can have written a comparison it would stop compiling. Compare them field
// by field, or with reflect.DeepEqual.
type noCompare [0]func()

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
	// ParentAgentID is set when this agent was spawned by another via
	// `OpShare`. Empty for top-level agents.
	ParentAgentID uuid.UUID
	// Model identifies the LLM behind the agent ("example-model-1",
	// "gpt-5-pro"). Used for cost attribution.
	Model string
	// MCPClient is the agent host's user-agent ("example-host@1.4.0",
	// "cursor@0.44"). Useful for compatibility tracking.
	MCPClient string
	_         noCompare
}

// Capability is the in-memory representation of a token. The wire form is
// a JWT; this struct is what the Issuer fills before signing, and what
// Verifier.Verify returns inside a VerifiedCapability.
type Capability struct {
	// ID is the unique identifier (UUIDv7 — sortable by issuance time
	// so per-tenant indexes stay tight).
	ID uuid.UUID

	// Issuer is the service instance that minted the capability. Verifiers
	// accept tokens whose Issuer is in the trusted set.
	Issuer string

	// Subject is the principal the capability authorises.
	Subject Principal

	// Audience pins which services the capability is valid for: a
	// verifier refuses a token whose audience does not include the one it
	// verifies for (VerifyRequest.Audience).
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
	// request must then carry a DPoP proof signed by that key (package dpop). A
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
	Copies []CopyCeiling
}

// CopyCeiling is a limit one attenuation block of a Biscuit set on the copy
// it ends and every copy attenuated from it, all of which carry the block's
// revocation id. Its counters are keyed by that id, so siblings attenuated
// apart count apart, while the capability's own counters still bound them
// all together.
type CopyCeiling struct {
	// RevocationID is the block's revocation id.
	RevocationID []byte
	// Requests bounds the requests made with the copy; nil when the block
	// set no request limit of its own. Never Unlimited: a copy only narrows.
	Requests *RequestCeiling
	// Budget bounds the copy's spend, in the capability's unit; nil when the
	// block set no budget of its own. Never Unlimited.
	Budget *BudgetCeiling
}

// AdmitsRequests reports whether n requests made with the copy in all stay
// under its own request limit; with none of its own, any number does.
func (c CopyCeiling) AdmitsRequests(n int64) bool { return c.Requests == nil || c.Requests.Admits(n) }

// AdmitsBudget reports whether amount spent with the copy in all stays under
// its own budget; with none of its own, any amount does.
func (c CopyCeiling) AdmitsBudget(amount Nanos) bool {
	return c.Budget == nil || c.Budget.Admits(amount)
}

// HeldCopy is a Biscuit copy a hold is on: a hold counts no requests, so it
// carries the copy's budget alone.
type HeldCopy struct {
	// RevocationID is the copy's revocation id.
	RevocationID []byte
	// Budget is the copy's own budget the hold counts against; nil when the
	// copy has none of its own.
	Budget *BudgetCeiling
}

// Caveats is a typed bag of restrictions, AND-combined. Ops is required; an
// empty resource or source-network list places no restriction on its axis.
// The ceilings are explicit: a zero RequestCeiling or BudgetCeiling allows
// nothing, and only Unlimited lifts one.
//
// It is written into a token as the limes_caveats claim (wire.go), so
// renaming a field changes nothing a token carries. A verifier refuses a
// member it does not know: a caveat it cannot enforce must not pass as one
// it did.
//
// A child capability may only narrow caveats — set a tighter prefix,
// drop ops, lower the budget, shorten TTL. Issuer.Delegate enforces this
// through Narrows before it signs, and the verifier does for every Biscuit
// attenuation block, so an attempt to widen is refused, never minted.
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

	// Requests caps the total number of times the capability may be
	// used within its TTL; the zero value allows none, and
	// RequestCeiling{Unlimited: true} lifts the cap.
	Requests RequestCeiling

	// Budget is the cost budget the capability authorises, in the
	// currency identified by UnitCode, exact to the nano; the zero value
	// allows no spend, and BudgetCeiling{Unlimited: true} lifts it. The
	// Meter counts spend against it; once exhausted, a charge returns
	// ErrBudgetExceeded.
	Budget BudgetCeiling

	// UnitCode pins the currency or unit: an ISO 4217 code (IsUnitCode),
	// XXX ("no currency") for a budget that is not money. Empty names none:
	// the capability counts requests (Meter.Bump) but no amount, and a
	// Charge, Reserve or Settle on it is ErrInvalidRequest, and a
	// Budget.Max above zero without one is ErrInvalidCaveats. A delegated
	// child that names none takes its parent's.
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

// clone is c with slices of its own, so a capability built from it shares
// nothing with the caller's request or with the parent it was inherited from.
func (c Caveats) clone() Caveats {
	c.Ops = slices.Clone(c.Ops)
	c.ResourcePrefixes = slices.Clone(c.ResourcePrefixes)
	c.ResourceURIs = slices.Clone(c.ResourceURIs)
	c.SourceIPCIDR = slices.Clone(c.SourceIPCIDR)
	return c
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

// Clone is c with slices and pointers of its own: a store keeps, and hands
// out, a capability no caller can alter through a slice it kept — the record
// is append-only.
func (c Capability) Clone() Capability { return c.clone() }

// cloneCopyCeilings is cs with revocation ids of its own; nil stays nil.
func cloneCopyCeilings(cs []CopyCeiling) []CopyCeiling {
	if cs == nil {
		return nil
	}
	out := make([]CopyCeiling, len(cs))
	for i, c := range cs {
		out[i] = CopyCeiling{
			RevocationID: slices.Clone(c.RevocationID),
			Requests:     clonePtr(c.Requests),
			Budget:       clonePtr(c.Budget),
		}
	}
	return out
}

// clonePtr is a pointer to a copy of *p; nil stays nil.
func clonePtr[T any](p *T) *T {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

// clone is c with slices and pointers of its own.
func (c Capability) clone() Capability {
	c.Subject = c.Subject.clone()
	c.Audience = slices.Clone(c.Audience)
	c.Caveats = c.Caveats.clone()
	c.Copies = cloneCopyCeilings(c.Copies)
	return c
}
