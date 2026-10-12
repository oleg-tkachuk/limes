package memstore

import (
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes"
)

// A tenant budget cursor holds the last row's spend and ceiling exactly, and
// refuses anything a store could not have written: a class it does not rank
// by, an amount outside 0..MaxNanos, a bounded row with no ceiling.
func TestTenantBudgetCursorRoundTripsAndRefusesGarbage(t *testing.T) {
	tenant := uuid.New()
	for _, ok := range []tenantBudgetCursor{
		{class: classBounded, spent: 29, max: 100, tenantID: tenant},
		{class: classBounded, spent: limes.MaxNanos, max: 1, tenantID: tenant},
		{class: classZero, tenantID: tenant},
		{class: classUnlimited, tenantID: tenant},
	} {
		if got, err := decodeTenantBudgetCursor(ok.encode()); err != nil || got != ok {
			t.Errorf("round trip of %+v = %+v, %v", ok, got, err)
		}
	}
	enc := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	id := tenant.String()
	for _, raw := range []string{
		"!!", enc("1|29|100"), enc("1|29|100|not-a-uuid"),
		// The three-field form a cursor had before it carried the fraction.
		enc("1|29.0|" + id),
		enc("x|29|100|" + id), enc("-1|29|100|" + id), enc("3|29|100|" + id),
		enc("1|-1|100|" + id), enc("1|29|-100|" + id), enc("1|NaN|100|" + id), enc("1|29|1.5|" + id),
		enc("1|29|0|" + id),  // a bounded row has a ceiling
		enc("0|0|100|" + id), // a row of another class has none
		enc("1|1000000000000000001|100|" + id),
	} {
		if _, err := decodeTenantBudgetCursor(raw); !errors.Is(err, limes.ErrInvalidRequest) {
			t.Errorf("cursor %q: err = %v, want ErrInvalidRequest", raw, err)
		}
	}
}

// A reservation cursor keeps its place to the nanosecond, past year 2262 too,
// and refuses anything that is not one.
func TestReservationCursorRoundTripsAndRefusesGarbage(t *testing.T) {
	id := uuid.New()
	for _, at := range []time.Time{
		time.Unix(0, 1),
		time.Date(2026, time.October, 12, 9, 30, 0, 123456789, time.UTC),
		time.Date(2318, time.January, 1, 0, 0, 0, 999999999, time.UTC),
	} {
		p := reservationPlace{expires: at, id: id}
		got, err := parseReservationPlace(p.String())
		if err != nil || !got.expires.Equal(at) || got.id != id {
			t.Errorf("round trip of %v = %+v, %v", at, got, err)
		}
	}
	for _, cursor := range []string{
		"", "1_2", "1_2_" + id.String() + "_x", "x_0_" + id.String(), "1_x_" + id.String(),
		"1_-1_" + id.String(), "1_1000000000_" + id.String(), "1_0_not-a-uuid",
	} {
		if _, err := parseReservationPlace(cursor); !errors.Is(err, limes.ErrInvalidRequest) {
			t.Errorf("cursor %q: err = %v, want ErrInvalidRequest", cursor, err)
		}
	}
}

// A threshold compares exactly: 29 of 100 is at 29%, where a float quotient
// reads 28.999…; and a spend near MaxNanos does not overflow the product.
func TestAtOrPastIsExact(t *testing.T) {
	const unit = limes.NanosPerUnit
	for _, tc := range []struct {
		spent, ceiling limes.Nanos
		pct            float64
		want           bool
	}{
		{29 * unit, 100 * unit, 29, true},
		{29*unit - 1, 100 * unit, 29, false},
		{1, 3, 33.3, true},
		{limes.MaxNanos, limes.MaxNanos, 100, true},
		{limes.MaxNanos - 1, limes.MaxNanos, 100, false},
	} {
		if got := atOrPast(tc.spent, tc.ceiling, tc.pct); got != tc.want {
			t.Errorf("atOrPast(%s, %s, %v) = %v, want %v", tc.spent, tc.ceiling, tc.pct, got, tc.want)
		}
	}
}

// Utilizations compare exactly: two that a float64 reads as equal still
// order by their true size, and a non-dyadic threshold is met at its value.
func TestUtilizationIsExact(t *testing.T) {
	const third = 333_333_333_333_333_333
	a := budgetRow{class: classBounded, spent: third + 1, max: 3 * third}
	b := budgetRow{class: classBounded, spent: 1, max: 3}
	if float64(a.spent)/float64(a.max) != float64(b.spent)/float64(b.max) {
		t.Skip("the float64 quotients differ; the case shows nothing")
	}
	if compareUtilization(a, b) <= 0 || compareUtilization(b, a) >= 0 {
		t.Error("a utilization just past a third did not rank above a third")
	}
	if !atOrPast(11, 1000, 1.1) || atOrPast(10, 1000, 1.1) {
		t.Error("11 of 1000 must be at 1.1%, and 10 of 1000 under it")
	}
}
