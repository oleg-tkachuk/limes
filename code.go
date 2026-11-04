package limes

import (
	"errors"
	"net/http"
	"slices"

	"github.com/oleg-tkachuk/limes/dpop"
	"github.com/oleg-tkachuk/limes/internal/errcode"
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

// The codes of package meter's sentinels, which this package answers as its
// own (docs/errors.md, "The meter's codes").
const (
	CodeCounterOverflow             Code = "counter_overflow"
	CodeChargeNotFound              Code = "charge_not_found"
	CodeRefundExceedsCharge         Code = "refund_exceeds_charge"
	CodeReservationNotFound         Code = "reservation_not_found"
	CodeCopyLimitExceeded           Code = "copy_limit_exceeded"
	CodeUsageNotFound               Code = "usage_not_found"
	CodeTenantBudgetNotFound        Code = "tenant_budget_not_found"
	CodeTenantBudgetVersionMismatch Code = "tenant_budget_version_mismatch"
	CodeExternalRefConflict         Code = "external_ref_conflict"
	CodeSettleConflict              Code = "settle_conflict"
	CodeLeaseExhausted              Code = "lease_exhausted"
	CodeLeaseRequestsExhausted      Code = "lease_requests_exhausted"
	CodeLeaseNotFound               Code = "lease_not_found"
	CodeLeaseNested                 Code = "lease_nested"
	CodeLeasingDisabled             Code = "leasing_disabled"
	CodeFeatureDisabled             Code = "feature_disabled"
)

// The codes of package exchange's sentinels, which this package answers as
// its own (docs/errors.md, "The exchange's codes"): the names RFC 6749
// §5.2, RFC 8707 and RFC 9396 give them, which the token endpoint answers
// with. exchange.ErrInvalidRequest is CodeInvalidRequest.
const (
	CodeInvalidClient               Code = "invalid_client"
	CodeUnauthorizedClient          Code = "unauthorized_client"
	CodeUnsupportedGrantType        Code = "unsupported_grant_type"
	CodeInvalidGrant                Code = "invalid_grant"
	CodeInvalidTarget               Code = "invalid_target"
	CodeInvalidScope                Code = "invalid_scope"
	CodeInvalidAuthorizationDetails Code = "invalid_authorization_details"
	CodeServerError                 Code = "server_error"
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

// meterCodeRules answer the meter's sentinels, which name their own code
// (errcode.Coded) since this package cannot name them. A refusal's sentinel
// is the limes family it is refused as, which gives its Reason; another's is
// nil, and its own status answers it.
var meterCodeRules = []codeRule{
	{ErrRequestLimitExceeded, CodeCopyLimitExceeded, 0, "", CategoryExhausted},
	{nil, CodeCounterOverflow, http.StatusConflict, "The counter has no room for the amount", CategoryPrecondition},
	{nil, CodeChargeNotFound, http.StatusNotFound, "The charge is not on record", CategoryNotFound},
	{nil, CodeRefundExceedsCharge, http.StatusConflict, "The refund exceeds what the charge took", CategoryPrecondition},
	{nil, CodeReservationNotFound, http.StatusNotFound, "The reservation is not on record, released or expired", CategoryNotFound},
	{nil, CodeUsageNotFound, http.StatusNotFound, "The capability has no usage on record", CategoryNotFound},
	{nil, CodeTenantBudgetNotFound, http.StatusNotFound, "The tenant has no budget on record", CategoryNotFound},
	{nil, CodeTenantBudgetVersionMismatch, http.StatusConflict, "The tenant budget changed since it was read", CategoryConflict},
	{nil, CodeExternalRefConflict, http.StatusConflict, "The external reference names another charge", CategoryConflict},
	{nil, CodeSettleConflict, http.StatusConflict, "The reservation was settled for another amount", CategoryConflict},
	{ErrBudgetExceeded, CodeLeaseExhausted, 0, "", CategoryExhausted},
	{ErrRequestLimitExceeded, CodeLeaseRequestsExhausted, 0, "", CategoryExhausted},
	{nil, CodeLeaseNotFound, http.StatusNotFound, "The capability holds no lease", CategoryNotFound},
	{nil, CodeLeaseNested, http.StatusConflict, "An ancestor of the capability holds a lease", CategoryPrecondition},
	{nil, CodeLeasingDisabled, http.StatusInternalServerError, "Leasing is not enabled on the store", CategoryPrecondition},
	{nil, CodeFeatureDisabled, http.StatusInternalServerError, "A feature is disabled by the store's configuration", CategoryPrecondition},
}

// exchangeCodeRules answer the exchange's sentinels, which name their own
// code as the meter's do, each with the status the token endpoint answers
// it with.
var exchangeCodeRules = []codeRule{
	{nil, CodeInvalidClient, http.StatusUnauthorized, "The client is not authenticated", CategoryUnauthenticated},
	{nil, CodeUnauthorizedClient, http.StatusBadRequest, "The client may not exchange tokens", CategoryDenied},
	{nil, CodeUnsupportedGrantType, http.StatusBadRequest, "The grant type is not supported", CategoryInvalid},
	{nil, CodeInvalidGrant, http.StatusBadRequest, "The subject token was refused", CategoryUnauthenticated},
	{nil, CodeInvalidTarget, http.StatusBadRequest, "The resource or audience was refused", CategoryDenied},
	{nil, CodeInvalidScope, http.StatusBadRequest, "The scope was refused", CategoryDenied},
	{nil, CodeInvalidAuthorizationDetails, http.StatusBadRequest, "The authorization details were refused", CategoryDenied},
	{nil, CodeServerError, http.StatusInternalServerError, "The server could not decide on the request", CategoryInternal},
}

// Origin is where a code comes from: the package whose errors it names. A
// consumer that does not run a component maps its codes as a formality, and
// may leave them out of a check of its own mapping (CodesOf). It is one of
// the Origin… values: no other can be spelled, so a misspelt origin does not
// compile. The zero Origin is none, and has no codes.
type Origin struct {
	name string
}

// The origins.
var (
	// OriginLimes — the root package and the packages it answers for:
	// verifying and checking a capability, its stores, dpop and jwks.
	OriginLimes = Origin{"limes"}
	// OriginExchange — module exchange, the token endpoint and an
	// Exchanger in process; a consumer that verifies tokens alone never
	// meets them.
	OriginExchange = Origin{"exchange"}
	// OriginMeter — package meter and the usage stores.
	OriginMeter = Origin{"meter"}
)

// String is the origin's name: the package its codes come from.
func (o Origin) String() string { return o.name }

// origins is every origin with its code rules, in the order Codes lists
// them: the one table Origins, CodesOf and Codes read, so an origin added
// has its codes wherever it is listed.
var origins = []struct {
	origin Origin
	rules  []codeRule
}{
	{OriginLimes, codeRules},
	{OriginExchange, exchangeCodeRules},
	{OriginMeter, meterCodeRules},
}

// allCodeRules is every code's rule, in origins' order: what a code alone
// is looked up in.
var allCodeRules = func() []codeRule {
	var out []codeRule
	for _, o := range origins {
		out = append(out, o.rules...)
	}
	return out
}()

// Origins returns every origin, in the order Codes lists their codes.
func Origins() []Origin {
	out := make([]Origin, 0, len(origins))
	for _, o := range origins {
		out = append(out, o.origin)
	}
	return out
}

// CodesOf returns the codes of first and rest: a consumer that runs no
// exchange checks its mapping of CodesOf(OriginLimes, OriginMeter). Each
// code comes once, in Codes' order — the origins in Origins' order, however
// they are passed, and one passed twice counted once. It takes one origin
// at least, since none would read as every code, which Codes is; the zero
// Origin has none.
func CodesOf(first Origin, rest ...Origin) []Code {
	asked := append([]Origin{first}, rest...)
	var rules []codeRule
	for _, o := range origins {
		if slices.Contains(asked, o.origin) {
			rules = append(rules, o.rules...)
		}
	}
	return codesIn(rules)
}

// codesIn are the codes of rules, each once.
func codesIn(rules []codeRule) []Code {
	out := make([]Code, 0, len(rules))
	for _, r := range rules {
		if !slices.Contains(out, r.code) {
			out = append(out, r.code)
		}
	}
	return out
}

// codeRuleOf is code's rule.
func codeRuleOf(code Code) (codeRule, bool) {
	for _, r := range allCodeRules {
		if r.code == code {
			return r, true
		}
	}
	return codeRule{}, false
}

// codeRuleFor is the rule of the code err names itself, a meter sentinel's,
// or else the first code rule err matches.
func codeRuleFor(err error) (codeRule, bool) {
	if err == nil {
		return codeRule{}, false
	}
	var coded errcode.Coded
	if errors.As(err, &coded) {
		if r, ok := codeRuleOf(Code(coded.LimesErrorCode())); ok {
			return r, true
		}
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

// Codes returns every code this package answers, the exchange's and the
// meter's among them, in a fixed order, so a consumer can prove its own
// mapping covers them all.
func Codes() []Code {
	return codesIn(allCodeRules)
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
	return Problem{Type: ProblemTypeURI(string(CodeInternal)), Title: internalTitle, Status: http.StatusInternalServerError, Code: CodeInternal, Category: CategoryInternal}
}

// internalTitle is the title of a problem with no more to say than that the
// server failed.
const internalTitle = "Internal error"

// ProblemOf is the RFC 9457 problem details of err, an error limes returned,
// for a consumer's own API, whatever its transport: its type URI, a fixed
// title, its HTTP status, its code, its category and, for a refusal, its
// Reason. A
// refusal's is the body Verifier.ErrorResponse writes. It carries no detail:
// an error's text can name kids, issuers or stores, which is for the
// server's log. False for an error that is not limes's: answer that as the
// server's failure.
func ProblemOf(err error) (Problem, bool) {
	cr, ok := codeRuleFor(err)
	if !ok {
		return Problem{}, false
	}
	// A refusal is answered as one, unless its code has a status of its
	// own: a meter sentinel in a refusal's family that is no refusal.
	if rr, refused := ruleFor(err); refused && cr.status == 0 {
		kind := rr.answer.slug
		if rr.reason != "" {
			kind = string(rr.reason)
		}
		p := answer(rr.answer, kind, rr.reason).Problem
		p.Code, p.Category = cr.code, cr.category
		return p, true
	}
	return problemOfRule(cr), true
}

// problemOfRule is the problem of a code with a status of its own.
func problemOfRule(r codeRule) Problem {
	return Problem{Type: ProblemTypeURI(string(r.code)), Title: r.title, Status: r.status, Code: r.code, Category: r.category}
}

// ProblemOfCode is the problem ProblemOf answers an error with code with,
// and false for a code that is none of this package's, the meter's among
// them: a consumer holding only a code, or pinning the contract its mapping
// was written against (limestest), reads it without an error to hand.
func ProblemOfCode(code Code) (Problem, bool) {
	r, ok := codeRuleOf(code)
	if !ok {
		return Problem{}, false
	}
	if r.status != 0 {
		return problemOfRule(r), true
	}
	// A refusal's code: answered as the family it is refused as.
	p, _ := ProblemOf(r.sentinel)
	p.Code, p.Category = r.code, r.category
	return p, true
}
