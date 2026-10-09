package limes

import "errors"

// Reason is a stable, machine-readable name for why a presented capability
// was refused. The sentinels stay the way to branch in Go; a Reason is what a
// consumer puts on the wire, so that a client in any language tells "this
// capability is spent" from "this request was malformed" without reading a
// message. The values are never renamed; new ones may be added.
type Reason string

// The refusals a verifier, Caveats.Check or a Meter returns for a presented
// capability.
const (
	ReasonInvalidSignature       Reason = "invalid_signature"
	ReasonKeysUnavailable        Reason = "keys_unavailable"
	ReasonExpired                Reason = "expired"
	ReasonNotYetValid            Reason = "not_yet_valid"
	ReasonRevoked                Reason = "revoked"
	ReasonAudienceMismatch       Reason = "audience_mismatch"
	ReasonInvalidBiscuit         Reason = "invalid_biscuit"
	ReasonProofRequired          Reason = "proof_required"
	ReasonProofInvalid           Reason = "proof_invalid"
	ReasonProofReplayed          Reason = "proof_replayed"
	ReasonOpNotAllowed           Reason = "op_not_allowed"
	ReasonResourceNotAllowed     Reason = "resource_not_allowed"
	ReasonSourceNotAllowed       Reason = "source_not_allowed"
	ReasonIdempotencyKeyRequired Reason = "idempotency_key_required"
	ReasonTaintedReadNotAllowed  Reason = "tainted_read_not_allowed"
	ReasonRequestLimitExceeded   Reason = "request_limit_exceeded"
	ReasonBudgetExceeded         Reason = "budget_exceeded"
	ReasonTenantBudgetExceeded   Reason = "tenant_budget_exceeded"
)

// reasonRule maps one sentinel to its reason. permanent is Reason.Permanent.
type reasonRule struct {
	sentinel  error
	reason    Reason
	permanent bool
}

// reasonRules is the one table of reasons, most specific sentinel first. The
// verifier wraps a key it cannot resolve, a bad proof of possession and a bad
// Biscuit block in ErrInvalidSignature as well, and every caveat sentinel
// matches ErrCaveatViolation, so order decides. An unknown kid stays an
// invalid signature, as the verifier means it to; keys the verifier could not
// fetch at all are an outage on its side, not a fault in the token.
var reasonRules = []reasonRule{
	{ErrJWKSUnavailable, ReasonKeysUnavailable, false},
	{ErrDPoPRequired, ReasonProofRequired, false},
	{ErrDPoPInvalid, ReasonProofInvalid, false},
	{ErrDPoPReplayed, ReasonProofReplayed, false},
	{ErrBiscuitAttenuation, ReasonInvalidBiscuit, true},
	{ErrInvalidSignature, ReasonInvalidSignature, true},
	{ErrExpired, ReasonExpired, true},
	{ErrNotYetValid, ReasonNotYetValid, false},
	{ErrRevoked, ReasonRevoked, true},
	{ErrAudienceMismatch, ReasonAudienceMismatch, true},
	{ErrOpNotAllowed, ReasonOpNotAllowed, true},
	{ErrResourceNotAllowed, ReasonResourceNotAllowed, true},
	{ErrSourceIPNotAllowed, ReasonSourceNotAllowed, true},
	{ErrIdempotencyKeyRequired, ReasonIdempotencyKeyRequired, false},
	{ErrTaintedReadNotAllowed, ReasonTaintedReadNotAllowed, false},
	{ErrRequestLimitExceeded, ReasonRequestLimitExceeded, true},
	{ErrBudgetExceeded, ReasonBudgetExceeded, false},
	{ErrTenantBudgetExceeded, ReasonTenantBudgetExceeded, false},
}

// ReasonOf returns the reason err refused a capability for, and false when
// err is no such refusal — nil, a store failure, or a programming error such
// as ErrInvalidRequest, which a consumer maps as it maps any other.
func ReasonOf(err error) (Reason, bool) {
	if err == nil {
		return "", false
	}
	for _, r := range reasonRules {
		if errors.Is(err, r.sentinel) {
			return r.reason, true
		}
	}
	return "", false
}

// Permanent reports whether a refusal for r stands for as long as the
// capability does: the same request with the same capability will be refused
// again, whatever happens elsewhere, so a caller stops rather than retries.
//
// A refusal that something else can lift is not permanent: a budget frees as
// holds lapse or charges are refunded, a tenant ceiling is raised by an
// operator, a taint flag is cleared, a not-yet-valid capability becomes
// valid, a proof is signed afresh, the verifier fetches its keys again, and
// an idempotency key can be added to the request.
func (r Reason) Permanent() bool {
	for _, rule := range reasonRules {
		if rule.reason == r {
			return rule.permanent
		}
	}
	return false
}

// Reasons returns every reason this package defines, in a fixed order, so a
// consumer can prove its own mapping covers them all.
func Reasons() []Reason {
	out := make([]Reason, 0, len(reasonRules))
	for _, r := range reasonRules {
		out = append(out, r.reason)
	}
	return out
}
