package meter

import (
	"errors"
	"net/http"

	"github.com/oleg-tkachuk/limes"
)

// The codes of the meter's own sentinels (docs/errors.md), beside limes's.
const (
	CodeCounterOverflow             limes.Code = "counter_overflow"
	CodeChargeNotFound              limes.Code = "charge_not_found"
	CodeRefundExceedsCharge         limes.Code = "refund_exceeds_charge"
	CodeReservationNotFound         limes.Code = "reservation_not_found"
	CodeCopyLimitExceeded           limes.Code = "copy_limit_exceeded"
	CodeUsageNotFound               limes.Code = "usage_not_found"
	CodeTenantBudgetNotFound        limes.Code = "tenant_budget_not_found"
	CodeTenantBudgetVersionMismatch limes.Code = "tenant_budget_version_mismatch"
	CodeExternalRefConflict         limes.Code = "external_ref_conflict"
	CodeSettleConflict              limes.Code = "settle_conflict"
)

// codeRule is one of the meter's sentinels, its code and, for one limes
// does not answer as a refusal, its HTTP status and title.
type codeRule struct {
	sentinel error
	code     limes.Code
	status   int
	title    string
}

// codeRules is the one table CodeOf and ProblemOf read.
var codeRules = []codeRule{
	{ErrCopyLimitExceeded, CodeCopyLimitExceeded, 0, ""},
	{ErrCounterOverflow, CodeCounterOverflow, http.StatusConflict, "The counter has no room for the amount"},
	{ErrChargeNotFound, CodeChargeNotFound, http.StatusNotFound, "The charge is not on record"},
	{ErrRefundExceedsCharge, CodeRefundExceedsCharge, http.StatusConflict, "The refund exceeds what the charge took"},
	{ErrReservationNotFound, CodeReservationNotFound, http.StatusNotFound, "The reservation is not on record, released or expired"},
	{ErrUsageNotFound, CodeUsageNotFound, http.StatusNotFound, "The capability has no usage on record"},
	{ErrTenantBudgetNotFound, CodeTenantBudgetNotFound, http.StatusNotFound, "The tenant has no budget on record"},
	{ErrTenantBudgetVersionMismatch, CodeTenantBudgetVersionMismatch, http.StatusConflict, "The tenant budget changed since it was read"},
	{ErrExternalRefConflict, CodeExternalRefConflict, http.StatusConflict, "The external reference names another charge"},
	{ErrSettleConflict, CodeSettleConflict, http.StatusConflict, "The reservation was settled for another amount"},
}

// codeRuleFor is the meter's rule err matches.
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

// CodeOf is err's code: the meter's own, or limes.CodeOf's.
func CodeOf(err error) (limes.Code, bool) {
	if r, ok := codeRuleFor(err); ok {
		return r.code, true
	}
	return limes.CodeOf(err)
}

// Codes is every code the meter adds to limes.Codes, in a fixed order.
func Codes() []limes.Code {
	out := make([]limes.Code, len(codeRules))
	for i, r := range codeRules {
		out[i] = r.code
	}
	return out
}

// ProblemOf is limes.ProblemOf with the meter's own codes: an error the
// meter returned, as RFC 9457 problem details. A refusal keeps the answer
// limes gives it, its code the meter's; any other of the meter's sentinels
// has its own status, and a type ending in its code.
func ProblemOf(err error) (limes.Problem, bool) {
	r, ok := codeRuleFor(err)
	if !ok {
		return limes.ProblemOf(err)
	}
	p, known := limes.ProblemOf(err)
	if !known || r.status != 0 {
		p = limes.Problem{Type: limes.ProblemTypeURI(string(r.code)), Title: r.title, Status: r.status}
	}
	p.Code = r.code
	return p, true
}
