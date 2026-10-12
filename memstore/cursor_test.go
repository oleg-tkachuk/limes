package memstore

import (
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes"
)

// A cursor holds a utilization the store computed: a finite, non-negative
// decimal. NaN and the infinities order differently, so they are refused
// rather than read.
func TestTenantBudgetCursorRefusesNonFinite(t *testing.T) {
	tenant := uuid.New()
	ok := tenantBudgetCursor{utilization: "42.5", tenantID: tenant}
	if got, err := decodeTenantBudgetCursor(ok.encode()); err != nil || got != ok {
		t.Fatalf("round trip = %+v, %v; want %+v", got, err, ok)
	}
	for _, pct := range []string{"NaN", "Inf", "-Inf", "+Inf", "infinity", "-1", "1e999", "0x1p3"} {
		c := tenantBudgetCursor{utilization: pct, tenantID: tenant}.encode()
		if _, err := decodeTenantBudgetCursor(c); !errors.Is(err, limes.ErrInvalidRequest) {
			t.Errorf("utilization %q: err = %v, want ErrInvalidRequest", pct, err)
		}
	}
	for _, raw := range []string{"!!", base64.RawURLEncoding.EncodeToString([]byte("42.5")), base64.RawURLEncoding.EncodeToString([]byte("42.5|not-a-uuid"))} {
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
