package pgstore

import (
	"errors"
	"testing"

	"github.com/oleg-tkachuk/limes"
)

// Every filter limes defines is one this store names, each as what it
// keeps; one limes adds later fails here until the store names it, rather
// than list every tenant for a filter that narrows.
func TestKeepOfNamesEveryFilter(t *testing.T) {
	want := map[limes.TenantBudgetFilter]string{
		limes.TenantBudgetsUnspecified: keepEvery,
		limes.TenantBudgetsAll:         keepEvery,
		limes.TenantBudgetsAtOrPast:    keepAtOrPast,
		limes.TenantBudgetsUnlimited:   keepUnlimited,
	}
	f := limes.TenantBudgetFilter(0)
	for ; f.Validate() == nil; f++ {
		got, err := keepOf(f)
		if w, ok := want[f]; !ok || err != nil || got != w {
			t.Errorf("keepOf(%s) = %q, %v; want %q", f, got, err, w)
		}
	}
	if len(want) != int(f) {
		t.Errorf("limes defines %d filters, the test names %d", f, len(want))
	}
	if _, err := keepOf(f); !errors.Is(err, limes.ErrInvalidRequest) {
		t.Errorf("keepOf(%s), a filter limes lacks: err = %v, want ErrInvalidRequest", f, err)
	}
}
