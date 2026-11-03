package limes

import (
	"errors"
	"net/http"
	"slices"

	"github.com/oleg-tkachuk/limes/dpop"
	"github.com/oleg-tkachuk/limes/jwks"
)

// Code is a stable, machine-readable name for any error limes returns
// (docs/adr/0012-error-codes.md, docs/errors.md): what a consumer puts on
// its wire, so a client branches on it rather than on a message, whose text
// is never a contract. A refusal of a presented capability has its Reason as
// its code; an error that Reason folds into a wider one keeps a code of its
// own (tenant_erased, reported as revoked). Codes are never renamed; new ones
// may be added.
type Code string

// The codes of the refusals, each its Reason.
const (
	CodeNoCredentials          = Code(ReasonNoCredentials)
	CodeMalformedAuthorization = Code(ReasonMalformedAuthorization)
	CodeInvalidToken           = Code(ReasonInvalidToken)
	CodeKeysUnavailable        = Code(ReasonKeysUnavailable)
	CodeExpired                = Code(ReasonExpired)
	CodeNotYetValid            = Code(ReasonNotYetValid)
	CodeRevoked                = Code(ReasonRevoked)
	CodeAudienceMismatch       = Code(ReasonAudienceMismatch)
	CodeBiscuitAttenuation     = Code(ReasonBiscuitAttenuation)
	CodeProofRequired          = Code(ReasonProofRequired)
	CodeNonceRequired          = Code(ReasonNonceRequired)
	CodeAuthSchemeMismatch     = Code(ReasonAuthSchemeMismatch)
	CodeProofInvalid           = Code(ReasonProofInvalid)
	CodeProofReplayed          = Code(ReasonProofReplayed)
	CodeOpNotAllowed           = Code(ReasonOpNotAllowed)
	CodeResourceNotAllowed     = Code(ReasonResourceNotAllowed)
	CodeSourceIPNotAllowed     = Code(ReasonSourceIPNotAllowed)
	CodeIdempotencyKeyRequired = Code(ReasonIdempotencyKeyRequired)
	CodeTaintedReadNotAllowed  = Code(ReasonTaintedReadNotAllowed)
	CodeRequestLimitExceeded   = Code(ReasonRequestLimitExceeded)
	CodeBudgetExceeded         = Code(ReasonBudgetExceeded)
	CodeTenantBudgetExceeded   = Code(ReasonTenantBudgetExceeded)
)

// The codes of the other errors.
const (
	// Refusals Reason reports under a wider one, or not at all.
	CodeTenantErased           Code = "tenant_erased"
	CodeDelegationTooDeep      Code = "delegation_too_deep"
	CodeUnknownKID             Code = "unknown_kid"
	CodeUnauthenticated        Code = "unauthenticated"
	CodeProofOfPossession      Code = "proof_of_possession"
	CodeCaveatViolation        Code = "caveat_violation"
	CodeConflict               Code = "conflict"
	CodeAlreadyExists          Code = "already_exists"
	CodeUnitCodeMismatch       Code = "unit_code_mismatch"
	CodeInvalidRequest         Code = "invalid_request"
	CodeInvalidCaveats         Code = "invalid_caveats"
	CodeInvalidAmount          Code = "invalid_amount"
	CodeEffectConflict         Code = "effect_conflict"
	CodeCursorMismatch         Code = "cursor_mismatch"
	CodeDelegationTooWide      Code = "delegation_too_wide"
	CodeNotFound               Code = "not_found"
	CodeUnknownTenant          Code = "unknown_tenant"
	CodeTenantDeleted          Code = "tenant_deleted"
	CodeScopeMismatch          Code = "scope_mismatch"
	CodeProofUnsupported       Code = "proof_unsupported"
	CodeNoRevocationLineage    Code = "no_revocation_lineage"
	CodeCopyCountersNotMetered Code = "copy_counters_not_metered"
	CodeCursorRepeated         Code = "cursor_repeated"
	CodeInvalidConfig          Code = "invalid_config"
	CodeInternal               Code = "internal"
)

// codeRule is one sentinel's code, its Category and, for one no refusal rule
// answers, the HTTP status and title of its problem; a refusal's are
// refusalRules', so ProblemOf and ErrorResponse answer it alike.
type codeRule struct {
	sentinel error
	code     Code
	status   int
	title    string
	category Category
}

// codeRules is the one table CodeOf and ProblemOf read: the first rule an
// error matches decides, so each family's members come before its root.
var codeRules = []codeRule{
	// Answered as refusals.
	{ErrMalformedAuthorization, CodeMalformedAuthorization, 0, "", CategoryInvalid},
	{ErrNoCredentials, CodeNoCredentials, 0, "", CategoryUnauthenticated},
	{jwks.ErrUnavailable, CodeKeysUnavailable, 0, "", CategoryUnavailable},
	{dpop.ErrNonceRequired, CodeNonceRequired, 0, "", CategoryUnauthenticated},
	{dpop.ErrRequired, CodeProofRequired, 0, "", CategoryUnauthenticated},
	{dpop.ErrInvalid, CodeProofInvalid, 0, "", CategoryUnauthenticated},
	{dpop.ErrReplayed, CodeProofReplayed, 0, "", CategoryUnauthenticated},
	{ErrAuthSchemeMismatch, CodeAuthSchemeMismatch, 0, "", CategoryUnauthenticated},
	{ErrProofOfPossession, CodeProofOfPossession, 0, "", CategoryUnauthenticated},
	{ErrBiscuitAttenuation, CodeBiscuitAttenuation, 0, "", CategoryUnauthenticated},
	{ErrInvalidToken, CodeInvalidToken, 0, "", CategoryUnauthenticated},
	{ErrExpired, CodeExpired, 0, "", CategoryUnauthenticated},
	{ErrNotYetValid, CodeNotYetValid, 0, "", CategoryUnauthenticated},
	{ErrTenantErased, CodeTenantErased, 0, "", CategoryUnauthenticated},
	{ErrDelegationTooDeep, CodeDelegationTooDeep, 0, "", CategoryUnauthenticated},
	{ErrRevoked, CodeRevoked, 0, "", CategoryUnauthenticated},
	{ErrAudienceMismatch, CodeAudienceMismatch, 0, "", CategoryUnauthenticated},
	{ErrUnauthenticated, CodeUnauthenticated, 0, "", CategoryUnauthenticated},
	{ErrOpNotAllowed, CodeOpNotAllowed, 0, "", CategoryDenied},
	{ErrResourceNotAllowed, CodeResourceNotAllowed, 0, "", CategoryDenied},
	{ErrSourceIPNotAllowed, CodeSourceIPNotAllowed, 0, "", CategoryDenied},
	{ErrIdempotencyKeyRequired, CodeIdempotencyKeyRequired, 0, "", CategoryDenied},
	{ErrTaintedReadNotAllowed, CodeTaintedReadNotAllowed, 0, "", CategoryDenied},
	{ErrCaveatViolation, CodeCaveatViolation, 0, "", CategoryDenied},
	{ErrRequestLimitExceeded, CodeRequestLimitExceeded, 0, "", CategoryExhausted},
	{ErrBudgetExceeded, CodeBudgetExceeded, 0, "", CategoryExhausted},
	{ErrTenantBudgetExceeded, CodeTenantBudgetExceeded, 0, "", CategoryExhausted},
	{ErrUnitCodeMismatch, CodeUnitCodeMismatch, 0, "", CategoryPrecondition},
	{ErrAlreadyExists, CodeAlreadyExists, 0, "", CategoryAlreadyExists},
	{ErrConflict, CodeConflict, 0, "", CategoryConflict},

	// The caller's request.
	{ErrInvalidCaveats, CodeInvalidCaveats, http.StatusBadRequest, "The caveats are invalid", CategoryInvalid},
	{ErrInvalidAmount, CodeInvalidAmount, http.StatusBadRequest, "The amount is invalid", CategoryInvalid},
	{ErrEffectConflict, CodeEffectConflict, http.StatusBadRequest, "The declared effect conflicts with the operation", CategoryInvalid},
	{ErrCursorMismatch, CodeCursorMismatch, http.StatusBadRequest, "The cursor is of another listing", CategoryInvalid},
	{ErrInvalidRequest, CodeInvalidRequest, http.StatusBadRequest, "The request is invalid", CategoryInvalid},
	{ErrDelegationTooWide, CodeDelegationTooWide, http.StatusForbidden, "The delegation asks for more than its parent holds", CategoryDenied},
	{jwks.ErrUnknownKID, CodeUnknownKID, http.StatusUnauthorized, "The key id names no key", CategoryUnauthenticated},

	// What is, or is not, on record.
	{ErrNotFound, CodeNotFound, http.StatusNotFound, "Not found", CategoryNotFound},
	{ErrUnknownTenant, CodeUnknownTenant, http.StatusNotFound, "The tenant is unknown", CategoryNotFound},
	{ErrTenantDeleted, CodeTenantDeleted, http.StatusGone, "The tenant is deleted", CategoryNotFound},
	{ErrScopeMismatch, CodeScopeMismatch, http.StatusInternalServerError, "The call is scoped to another tenant than its request names", CategoryInternal},

	// The server's configuration or its upstream: its own to fix.
	{ErrProofUnsupported, CodeProofUnsupported, http.StatusInternalServerError, "The capability is bound to a key and no proof is checked", CategoryPrecondition},
	{ErrNoRevocationLineage, CodeNoRevocationLineage, http.StatusBadGateway, "The revocation upstream names no lineage", CategoryUnavailable},
	{ErrCopyCountersNotMetered, CodeCopyCountersNotMetered, http.StatusInternalServerError, "Copy limits need a verifier that meters copies", CategoryInternal},
	{ErrCursorRepeated, CodeCursorRepeated, http.StatusBadGateway, "The store returned a cursor already fetched", CategoryUnavailable},
	{ErrInvalidConfig, CodeInvalidConfig, http.StatusInternalServerError, "The server is misconfigured", CategoryInternal},
	{ErrInternal, CodeInternal, http.StatusInternalServerError, internalTitle, CategoryInternal},
}

// codeRuleFor is the first code rule err matches.
func codeRuleFor(err error) (codeRule, bool) {
	if err == nil {
		return codeRule{}, false
	}
	for _, r := range codeRules {
		if errors.Is(err, r.sentinel) {
			return r, true
		}
	}
	return codeRule{}, false
}

// CodeOf returns err's code, and false when err is no error of limes's —
// nil, or a store's or the consumer's own — which a consumer answers as its
// own failure.
func CodeOf(err error) (Code, bool) {
	r, ok := codeRuleFor(err)
	return r.code, ok
}

// Codes returns every code this package defines, in a fixed order, so a
// consumer can prove its own mapping covers them all.
func Codes() []Code {
	out := make([]Code, 0, len(codeRules))
	for _, r := range codeRules {
		if !slices.Contains(out, r.code) {
			out = append(out, r.code)
		}
	}
	return out
}

// ProblemTypeURI is the RFC 9457 type URI of the problem a code, or a
// refusal's Reason, names.
func ProblemTypeURI(kind string) string { return problemTypeBase + kind }

// ProblemFor is ProblemOf for any error: one that is not limes's — a
// store's own failure, a consumer's — is answered as internal, 500, so every
// error a consumer passes has a code and a body.
func ProblemFor(err error) Problem {
	if p, ok := ProblemOf(err); ok {
		return p
	}
	return Problem{Type: ProblemTypeURI(string(CodeInternal)), Title: internalTitle, Status: http.StatusInternalServerError, Code: CodeInternal}
}

// internalTitle is the title of a problem with no more to say than that the
// server failed.
const internalTitle = "Internal error"

// ProblemOf is the RFC 9457 problem details of err, an error limes returned,
// for a consumer's own API, whatever its transport: its type URI, a fixed
// title, its HTTP status, its code and, for a refusal, its Reason. A
// refusal's is the body Verifier.ErrorResponse writes. It carries no detail:
// an error's text can name kids, issuers or stores, which is for the
// server's log. False for an error that is not limes's: answer that as the
// server's failure.
func ProblemOf(err error) (Problem, bool) {
	cr, ok := codeRuleFor(err)
	if !ok {
		return Problem{}, false
	}
	if rr, refused := ruleFor(err); refused {
		kind := rr.answer.slug
		if rr.reason != "" {
			kind = string(rr.reason)
		}
		p := answer(rr.answer, kind, rr.reason).Problem
		p.Code = cr.code
		return p, true
	}
	return Problem{Type: ProblemTypeURI(string(cr.code)), Title: cr.title, Status: cr.status, Code: cr.code}, true
}
