package demo

import (
	"testing"

	"github.com/oleg-tkachuk/limes"
)

// An amount reads with its unit once; a ceiling, which carries its unit,
// prints it once by itself.
func TestMoneyNamesTheUnitOnce(t *testing.T) {
	const unit = "USD"
	if got, want := Money(limes.MustParseAmount("0.35"), unit), "0.35 USD"; got != want {
		t.Errorf("Money = %q, want %q", got, want)
	}
	ceiling := limes.BudgetCeiling{Max: limes.MustParseAmount("100"), UnitCode: unit}
	if got, want := ceiling.String(), "100 USD"; got != want {
		t.Errorf("a ceiling = %q, want %q", got, want)
	}
}
