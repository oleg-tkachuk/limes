package limes

import (
	"slices"
	"time"
	"uuid"
)

// Op is an operation an agent may perform. The package defines a small
// built-in set (below); a consumer adds its own as namespaced names of the
// form "<namespace>:<name>" — "tool:search", "mcp:github/create_issue" —
// which Op.Validate accepts. A consumer-defined operation is treated as
// state-changing unless the server declares otherwise (Effect).
// Fine-grained method gating can still layer admin-authored policy on top.
type Op string

// The built-in operations: the ones every resource server shares. Get and
// List read; the rest may change state (Op.ResolveEffect). A domain's own
// operations — searching an index, computing embeddings — are the
// consumer's, namespaced: "index:search", "model:embed".
const (
	OpGet    Op = "get"    // read one resource
	OpPut    Op = "put"    // write one resource
	OpList   Op = "list"   // list resources under a prefix
	OpDelete Op = "delete" // delete a resource
	OpShare  Op = "share"  // delegate a sub-capability to another agent through the issuer; not needed to attenuate a Biscuit copy
	OpManage Op = "manage" // administer: tenants, policy
)

// noCompare makes a struct that holds it not comparable with ==. The structs
// that may gain a field in 1.x carry one, so a field of a type == cannot
// compare — a slice, a map — is an addition rather than a break: no caller
// can have written a comparison it would stop compiling. Compare them field
// by field, or with reflect.DeepEqual.
type noCompare [0]func()

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

	// ConfirmationJKT, when set, binds the capability to a key: the RFC 7638
	// thumbprint of the public key whose holder alone may present it. Every
	// request must then carry a DPoP proof signed by that key (package dpop). A
	// delegated child of a bound capability is bound too. Carried in the
	// token as `cnf.jkt`, and absent from tokens that are not bound.
	ConfirmationJKT string
}

// grant is what a token says beyond its capability: the parts no record
// holds, which only this package reads and writes.
type grant struct {
	Capability
	// biscuitRoot is set only on the token sealed inside a Biscuit (see
	// Issuer.Biscuit): the base64url Ed25519 public key that roots the
	// Biscuit's signature chain. A token carrying it is refused when
	// presented on its own, so it cannot be lifted out of an attenuated
	// Biscuit to shed the attenuation.
	biscuitRoot string
	// copies are the request and budget limits the attenuation blocks of a
	// verified Biscuit set, innermost block first (VerifiedCapability.Copies).
	copies []CopyCeiling
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
	// Budget bounds the copy's spend, in the capability's unit, which its
	// UnitCode carries; nil when the block set no budget of its own. Never
	// Unlimited.
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

	// Budget is the cost budget the capability authorises, exact to the
	// nano, in Budget.UnitCode — the unit every amount metered against the
	// capability counts in. The zero value allows no spend and needs no
	// unit: the capability counts requests (Meter.Bump) but no amount, and
	// a Charge, Reserve or Settle on it with no unit is ErrInvalidRequest.
	// BudgetCeiling{Unlimited: true, UnitCode: u} lifts the bound. Once it
	// is exhausted, a charge returns ErrBudgetExceeded. A delegated child
	// whose budget names no unit takes its parent's.
	Budget BudgetCeiling

	// Copies is how many Biscuit copies of the capability a meter counts —
	// the copies whose blocks set request or budget limits of their own,
	// each a counter the store keeps; the zero value counts none, and
	// CopyQuota{Unlimited: true} as many as the store allows. A block that
	// sets no limit makes no copy a meter counts, so it needs none.
	Copies CopyQuota

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
	return c
}
