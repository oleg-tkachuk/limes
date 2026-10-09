package limes

import (
	"encoding/json"
	"errors"
	"math"
	"math/rand/v2"
	"strconv"
	"testing"
)

func TestParseAmountIsExact(t *testing.T) {
	cases := map[string]Nanos{
		"0": 0, "1": NanosPerUnit, "1.5": 1_500_000_000, "0.35": 350_000_000,
		"0.000000001": 1, "1e-7": 100, "25.00": 25 * NanosPerUnit,
		"999999999.999999999": MaxNanos,
	}
	for in, want := range cases {
		if got, err := ParseAmount(in); err != nil || got != want {
			t.Errorf("ParseAmount(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
}

func TestParseAmountRefusesWhatItCannotCountExactly(t *testing.T) {
	for _, in := range []string{"-0.01", "0.0000000001", "1000000000", "1/3", "abc", "", "NaN"} {
		if _, err := ParseAmount(in); !errors.Is(err, ErrInvalidAmount) {
			t.Errorf("ParseAmount(%q): err = %v, want ErrInvalidAmount", in, err)
		}
	}
}

func TestNanosString(t *testing.T) {
	cases := map[Nanos]string{
		0: "0", 1: "0.000000001", 1_500_000_000: "1.5", 25 * NanosPerUnit: "25",
		MaxNanos: "999999999.999999999", -1_500_000_000: "-1.5",
	}
	for n, want := range cases {
		if got := n.String(); got != want {
			t.Errorf("Nanos(%d).String() = %q, want %q", int64(n), got, want)
		}
	}
}

// The sums float64 gets wrong are exact in Nanos.
func TestNanosSumsExactly(t *testing.T) {
	tenth, fifth, threeTenths := mustParse(t, "0.1"), mustParse(t, "0.2"), mustParse(t, "0.3")
	if tenth+fifth != threeTenths {
		t.Errorf("0.1 + 0.2 = %s, want 0.3", tenth+fifth)
	}
	var sum Nanos
	for range 10 {
		sum += tenth
	}
	if sum != NanosPerUnit {
		t.Errorf("ten × 0.1 = %s, want 1", sum)
	}
}

func TestAmountFromFloatRoundsTheDecimalAFloatStandsFor(t *testing.T) {
	tenth, fifth := 0.1, 0.2 // variables: constants would fold 0.1+0.2 to exactly 0.3
	cases := []struct {
		f    float64
		want Nanos
	}{{0.1, 100_000_000}, {tenth + fifth, 300_000_000}, {19.99, 19_990_000_000}, {1e-9, 1}, {4e-10, 0}, {6e-10, 1}, {5e-10, 0}, {1.5e-9, 2}}
	for _, c := range cases {
		if got, err := AmountFromFloat(c.f); err != nil || got != c.want {
			t.Errorf("AmountFromFloat(%v) = %d, %v; want %d", c.f, got, err, c.want)
		}
	}
	for _, f := range []float64{-0.01, math.NaN(), math.Inf(1), 1e9} {
		if _, err := AmountFromFloat(f); !errors.Is(err, ErrInvalidAmount) {
			t.Errorf("AmountFromFloat(%v): err = %v, want ErrInvalidAmount", f, err)
		}
	}
}

// A token's amount is the bytes a float64 encoded before amounts were
// Nanos: whatever such a float held reads back to its nano, and a Nanos
// that a float could hold encodes to the same bytes, so the token format
// does not move.
func TestNanosJSONMatchesTheFloatEncoding(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	const microNanos = 1_000 // below a micro, a float64 encodes with an exponent
	for range 200_000 {
		// Fifteen significant digits: what a float64 holds exactly.
		n := Nanos(r.Int64N(999_999_999_999_999) * microNanos)
		// The float a caller held for that decimal: the nearest one.
		f, err := strconv.ParseFloat(n.String(), 64)
		if err != nil {
			t.Fatal(err)
		}
		old, err := json.Marshal(f)
		if err != nil {
			t.Fatal(err)
		}
		got, err := json.Marshal(n)
		if err != nil || string(got) != string(old) {
			t.Fatalf("Nanos %d encodes %s, float64 encoded %s", int64(n), got, old)
		}
		var back Nanos
		if err := json.Unmarshal(old, &back); err != nil || back != n {
			t.Fatalf("float64 bytes %s read back as %d, %v; want %d", old, int64(back), err, int64(n))
		}
	}
}

func TestNanosJSONReadsFloatNoiseAndExponents(t *testing.T) {
	cases := map[string]Nanos{"0.30000000000000004": 300_000_000, "1e-07": 100, "1.5": 1_500_000_000}
	for in, want := range cases {
		var n Nanos
		if err := json.Unmarshal([]byte(in), &n); err != nil || n != want {
			t.Errorf("Unmarshal(%s) = %d, %v; want %d", in, int64(n), err, int64(want))
		}
	}
}

func mustParse(t *testing.T, s string) Nanos {
	t.Helper()
	n, err := ParseAmount(s)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// The abstract unit is ISO 4217's XXX; UNIT, its name before, is refused.
func TestAbstractUnitIsXXX(t *testing.T) {
	if got, err := NormaliseUnitCode(AbstractUnitCode); err != nil || got != "XXX" {
		t.Errorf("NormaliseUnitCode(XXX) = %q, %v", got, err)
	}
	if _, err := NormaliseUnitCode("UNIT"); err == nil {
		t.Error("UNIT is still accepted")
	}
	if err := (Caveats{Ops: []Op{OpGet}, UnitCode: "UNIT"}).Validate(); !errors.Is(err, ErrInvalidCaveats) {
		t.Errorf("caveats in UNIT: err = %v, want ErrInvalidCaveats", err)
	}
}

func TestNanosText(t *testing.T) {
	var n Nanos
	if err := n.UnmarshalText([]byte("0.35")); err != nil || n != 350_000_000 {
		t.Errorf("UnmarshalText(0.35) = %d, %v", int64(n), err)
	}
	if b, err := n.MarshalText(); err != nil || string(b) != "0.35" {
		t.Errorf("MarshalText = %s, %v", b, err)
	}
	if err := n.UnmarshalText([]byte("0.0000000001")); !errors.Is(err, ErrInvalidAmount) {
		t.Errorf("text finer than a nano: err = %v, want ErrInvalidAmount", err)
	}
}
