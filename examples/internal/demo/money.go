package demo

import "github.com/oleg-tkachuk/limes"

// Money reads an amount in unit, as a person reads a price: "0.35 USD". It
// takes an amount only: a limes.BudgetCeiling prints its own unit already.
func Money(amount limes.Nanos, unit string) string { return amount.String() + " " + unit }
