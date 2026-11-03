package limes

// Category is what a caller does about an error, coarser than its Code:
// every code belongs to exactly one, so a consumer maps the categories onto
// its transport rather than every code (docs/errors.md, "Categories").
// Categories are never renamed; new ones may be added.
type Category string

// The categories.
const (
	// CategoryUnauthenticated — the credential is missing, unproven or no
	// longer good: present another. One the request carries malformed is
	// CategoryInvalid.
	CategoryUnauthenticated Category = "unauthenticated"
	// CategoryDenied — the credential is good and does not allow the
	// request.
	CategoryDenied Category = "denied"
	// CategoryExhausted — a request, budget, copy or lease limit refused
	// it.
	CategoryExhausted Category = "exhausted"
	// CategoryInvalid — the request itself is wrong: sent again unchanged,
	// it fails again.
	CategoryInvalid Category = "invalid"
	// CategoryNotFound — what the request names is not on record.
	CategoryNotFound Category = "not_found"
	// CategoryConflict — the request disagrees with what is on record: read
	// it again, and decide.
	CategoryConflict Category = "conflict"
	// CategoryAlreadyExists — what the request would create is on record
	// already: read it rather than create it again.
	CategoryAlreadyExists Category = "already_exists"
	// CategoryPrecondition — the request is well formed, and the state it
	// needs is not there: the store's configuration, the ledger or another
	// capability's lease. Sent again it fails again until that state
	// changes, which is no retry's to change.
	CategoryPrecondition Category = "precondition"
	// CategoryUnavailable — a dependency failed on this request — did not
	// answer, or answered a page it had already given: retry later.
	CategoryUnavailable Category = "unavailable"
	// CategoryInternal — the server's configuration or a store failed: the
	// server's to fix.
	CategoryInternal Category = "internal"
)

// Categories returns every category, in a fixed order.
func Categories() []Category {
	return []Category{
		CategoryUnauthenticated, CategoryDenied, CategoryExhausted, CategoryInvalid,
		CategoryNotFound, CategoryConflict, CategoryAlreadyExists, CategoryPrecondition, CategoryUnavailable, CategoryInternal,
	}
}

// CategoryOf returns err's category, and false when err is no error of
// limes's, as CodeOf does. The meter's and the exchange's errors are
// limes's: one registry answers every code.
func CategoryOf(err error) (Category, bool) {
	r, ok := codeRuleFor(err)
	return r.category, ok
}

// CategoryOfCode returns code's category, and false for a code that is none
// of this package's, the exchange's and the meter's among them. A consumer
// proving its own mapping of codes reads it beside Codes.
func CategoryOfCode(code Code) (Category, bool) {
	r, ok := codeRuleOf(code)
	return r.category, ok
}
