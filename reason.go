package limes

import (
	"errors"
	"slices"

	"github.com/oleg-tkachuk/limes/dpop"
	"github.com/oleg-tkachuk/limes/jwks"
)

// Reason is a stable, machine-readable name for why a presented capability
// was refused. The sentinels stay the way to branch in Go; a Reason is what a
// consumer puts on the wire, so that a client in any language tells "this
// capability is spent" from "this request was malformed" without reading a
// message. The values are never renamed; new ones may be added.
type Reason string

// The refusals a verifier, Caveats.Check or a Meter returns for a presented
// capability.
const (
	ReasonMalformedAuthorization Reason = "malformed_authorization"
	ReasonInvalidToken           Reason = "invalid_token"
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

// refusalRule is one sentinel: the Reason ReasonOf gives it, whether that
// refusal stands (Reason.Permanent), and how ErrorResponse answers it. A
// family root has no Reason of its own — its members do — and answers what
// a consumer's own error in that family gets.
type refusalRule struct {
	sentinel  error
	reason    Reason
	permanent bool
	answer    refusal
}

// refusalRules is the one table ReasonOf and ErrorResponse both read: the
// first rule an error matches decides. Families do not overlap, so order
// matters only within one — each member before its family's root, and
// ErrBiscuitAttenuation, an invalid token of its own kind, before
// ErrInvalidToken.
var refusalRules = []refusalRule{
	{ErrMalformedAuthorization, ReasonMalformedAuthorization, true, answerMalformed},
	{jwks.ErrUnavailable, ReasonKeysUnavailable, false, answerKeysUnavailable},

	// A nonce challenge asks for a proof signed afresh, and a token under the
	// wrong scheme for the same token sent under DPoP; neither stands. RFC
	// 9449 §7.2 answers the scheme as an invalid token, not a bad proof.
	{dpop.ErrNonceRequired, ReasonProofInvalid, false, answerNonce},
	{dpop.ErrRequired, ReasonProofRequired, false, answerProof},
	{dpop.ErrInvalid, ReasonProofInvalid, false, answerProof},
	{dpop.ErrReplayed, ReasonProofReplayed, false, answerProof},
	{ErrAuthSchemeMismatch, ReasonProofRequired, false, answerToken},
	{ErrProofOfPossession, "", false, answerProof},

	{ErrBiscuitAttenuation, ReasonInvalidBiscuit, true, answerToken},
	{ErrInvalidToken, ReasonInvalidToken, true, answerToken},
	{ErrExpired, ReasonExpired, true, answerToken},
	{ErrNotYetValid, ReasonNotYetValid, false, answerToken},
	{ErrRevoked, ReasonRevoked, true, answerToken},
	{ErrAudienceMismatch, ReasonAudienceMismatch, true, answerToken},
	{ErrUnauthenticated, "", false, answerToken},

	{ErrOpNotAllowed, ReasonOpNotAllowed, true, answerScope},
	{ErrResourceNotAllowed, ReasonResourceNotAllowed, true, answerScope},
	{ErrSourceIPNotAllowed, ReasonSourceNotAllowed, true, answerScope},
	{ErrIdempotencyKeyRequired, ReasonIdempotencyKeyRequired, false, answerScope},
	{ErrTaintedReadNotAllowed, ReasonTaintedReadNotAllowed, false, answerScope},
	{ErrCaveatViolation, "", false, answerScope},

	{ErrRequestLimitExceeded, ReasonRequestLimitExceeded, true, answerExhausted},
	{ErrBudgetExceeded, ReasonBudgetExceeded, false, answerExhausted},
	{ErrTenantBudgetExceeded, ReasonTenantBudgetExceeded, false, answerExhausted},
}

// ruleFor is the first rule err matches.
func ruleFor(err error) (refusalRule, bool) {
	if err == nil {
		return refusalRule{}, false
	}
	for _, r := range refusalRules {
		if errors.Is(err, r.sentinel) {
			return r, true
		}
	}
	return refusalRule{}, false
}

// ReasonOf returns the reason err refused a capability for, and false when
// err is no such refusal — nil, a store failure, or a programming error such
// as ErrInvalidRequest, which a consumer maps as it maps any other.
func ReasonOf(err error) (Reason, bool) {
	rule, ok := ruleFor(err)
	if !ok || rule.reason == "" {
		return "", false
	}
	return rule.reason, true
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
	for _, rule := range refusalRules {
		if rule.reason == r {
			return rule.permanent
		}
	}
	return false
}

// Reasons returns every reason this package defines, in a fixed order, so a
// consumer can prove its own mapping covers them all.
func Reasons() []Reason {
	out := make([]Reason, 0, len(refusalRules))
	for _, r := range refusalRules {
		if r.reason != "" && !slices.Contains(out, r.reason) {
			out = append(out, r.reason)
		}
	}
	return out
}
