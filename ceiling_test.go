package limes

import (
	"errors"
	"testing"
)

// A ceiling's zero value allows nothing; only Unlimited lifts it.
func TestCeilingZeroAllowsNothing(t *testing.T) {
	if (RequestCeiling{}).Admits(1) || !(RequestCeiling{}).Admits(0) {
		t.Error("the zero RequestCeiling admits a request")
	}
	if (BudgetCeiling{}).Admits(1) || !(BudgetCeiling{}).Admits(0) {
		t.Error("the zero BudgetCeiling admits a spend")
	}
	if !(RequestCeiling{Unlimited: true}).Admits(MaxIJSONInteger) || !(BudgetCeiling{Unlimited: true}).Admits(MaxNanos) {
		t.Error("an unlimited ceiling refuses")
	}
	if !(RequestCeiling{Max: 3}).Admits(3) || (RequestCeiling{Max: 3}).Admits(4) {
		t.Error("a ceiling of 3 does not stop at 3")
	}
}

func TestCeilingValidate(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want error
	}{
		"requests within range":         {RequestCeiling{Max: MaxIJSONInteger}.Validate(), nil},
		"requests unlimited":            {RequestCeiling{Unlimited: true}.Validate(), nil},
		"requests negative":             {RequestCeiling{Max: -1}.Validate(), ErrInvalidRequest},
		"requests past I-JSON":          {RequestCeiling{Max: MaxIJSONInteger + 1}.Validate(), ErrInvalidRequest},
		"requests unlimited with a Max": {RequestCeiling{Max: 1, Unlimited: true}.Validate(), ErrInvalidRequest},
		"budget within range":           {BudgetCeiling{Max: MaxNanos}.Validate(), nil},
		"budget unlimited":              {BudgetCeiling{Unlimited: true}.Validate(), nil},
		"budget negative":               {BudgetCeiling{Max: -1}.Validate(), ErrInvalidAmount},
		"budget past MaxNanos":          {BudgetCeiling{Max: MaxNanos + 1}.Validate(), ErrInvalidAmount},
		"budget unlimited with a Max":   {BudgetCeiling{Max: 1, Unlimited: true}.Validate(), ErrInvalidRequest},
		// An out-of-range budget is a bad request too, not only a bad amount.
		"budget negative, a request": {BudgetCeiling{Max: -1}.Validate(), ErrInvalidRequest},
	} {
		if tc.want == nil && tc.err != nil || tc.want != nil && !errors.Is(tc.err, tc.want) {
			t.Errorf("%s: Validate() = %v, want %v", name, tc.err, tc.want)
		}
	}
}

// Within is the narrowing order: anything under unlimited, a bound only
// under a higher or equal bound, never unlimited under a bound.
func TestCeilingWithin(t *testing.T) {
	unlimited, five, three, zero := RequestCeiling{Unlimited: true}, RequestCeiling{Max: 5}, RequestCeiling{Max: 3}, RequestCeiling{}
	for name, tc := range map[string]struct {
		inner, outer RequestCeiling
		want         bool
	}{
		"a bound under unlimited":      {five, unlimited, true},
		"unlimited under unlimited":    {unlimited, unlimited, true},
		"a lower bound":                {three, five, true},
		"an equal bound":               {five, five, true},
		"zero under a bound":           {zero, five, true},
		"a higher bound":               {five, three, false},
		"unlimited under a bound":      {unlimited, five, false},
		"anything but zero under zero": {three, zero, false},
	} {
		if got := tc.inner.Within(tc.outer); got != tc.want {
			t.Errorf("%s: Within = %v, want %v", name, got, tc.want)
		}
	}
	if !(BudgetCeiling{Max: NanosPerUnit}).Within(BudgetCeiling{Unlimited: true}) || (BudgetCeiling{Unlimited: true}).Within(BudgetCeiling{Max: NanosPerUnit}) {
		t.Error("BudgetCeiling.Within disagrees with RequestCeiling.Within")
	}
}

func TestCeilingString(t *testing.T) {
	if got := (RequestCeiling{Max: 7}).String(); got != "7" {
		t.Errorf("String = %q, want 7", got)
	}
	if got := (BudgetCeiling{Unlimited: true}).String(); got != unlimitedText {
		t.Errorf("String = %q, want %q", got, unlimitedText)
	}
	if got := (BudgetCeiling{Max: 3 * NanosPerUnit / 2}).String(); got != "1.5" {
		t.Errorf("String = %q, want 1.5", got)
	}
}
