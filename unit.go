package limes

import "golang.org/x/text/currency"

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
