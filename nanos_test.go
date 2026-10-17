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
	for _, in := range []string{
		"-0.01", "0.0000000001", "1000000000", "1/3", "abc", "", "NaN",
		// big.Rat reads these; an amount is a plain decimal.
		"0x1p-2", "0x10", "0b1", "0o7", "1_000", "+1", ".5", "5.", " 1", "Inf",
		// An exponent long enough to cost real work, or to round to zero.
		"1e999999", "1e-999999", "1e1000",
		// A sign, even on zero: an amount is never negative.
		"-0", "-0.0e5",
	} {
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

// In JSON an amount is a decimal in a string, so that a reader whose numbers
// are float64 cannot round it: every Nanos round-trips exactly, including the
// eighteen-digit ones a float64 cannot hold.
func TestNanosJSONIsAString(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	const samples = 20_000 // random amounts across the whole range
	for range samples {
		n := Nanos(r.Int64N(int64(MaxNanos) + 1))
		b, err := json.Marshal(n)
		if err != nil {
			t.Fatal(err)
		}
		if want := strconv.Quote(n.String()); string(b) != want {
			t.Fatalf("Nanos %d encodes %s, want %s", int64(n), b, want)
		}
		var back Nanos
		if err := json.Unmarshal(b, &back); err != nil || back != n {
			t.Fatalf("%s read back as %d, %v; want %d", b, int64(back), err, int64(n))
		}
	}
}

// A JSON number is refused, as is a string ParseAmount refuses: an amount is
// read exactly or not at all.
func TestNanosJSONRefusesNumbersAndInexactStrings(t *testing.T) {
	for _, in := range []string{`1.5`, `0`, `null`, `"0.30000000000000004"`, `"0x1p-2"`, `"-1"`, `""`, `"1.5`} {
		var n Nanos
		if err := json.Unmarshal([]byte(in), &n); !errors.Is(err, ErrInvalidAmount) && !isSyntaxError(err) {
			t.Errorf("Unmarshal(%s) = %d, %v; want ErrInvalidAmount", in, int64(n), err)
		}
	}
}

// isSyntaxError reports whether err is encoding/json refusing malformed
// input before Nanos sees it.
func isSyntaxError(err error) bool {
	var syntax *json.SyntaxError
	return errors.As(err, &syntax)
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
	if AbstractUnitCode != "XXX" || !IsUnitCode(AbstractUnitCode) {
		t.Errorf("AbstractUnitCode = %q, want the ISO 4217 code XXX", AbstractUnitCode)
	}
	if IsUnitCode("UNIT") {
		t.Error("UNIT is still accepted")
	}
	if err := (Caveats{Ops: []Op{OpGet}, Budget: BudgetCeiling{UnitCode: "UNIT"}}).Validate(); !errors.Is(err, ErrInvalidCaveats) {
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

// FuzzParseAmount checks that whatever ParseAmount accepts is in range and
// reads back from its own String exactly.
func FuzzParseAmount(f *testing.F) {
	for _, s := range []string{"0", "1.5", "1e-7", "999999999.999999999", "0x1p-2", "1e999", "-1"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		n, err := ParseAmount(s)
		if err != nil {
			return
		}
		if n < 0 || n > MaxNanos {
			t.Fatalf("ParseAmount(%q) = %d, outside 0..MaxNanos", s, int64(n))
		}
		if back, err := ParseAmount(n.String()); err != nil || back != n {
			t.Fatalf("%q read as %s, which reads back as %d, %v", s, n, int64(back), err)
		}
	})
}

// String writes a negative amount — never a valid one, but printed in
// errors — with its sign, the most negative one included.
func TestNanosStringNegative(t *testing.T) {
	cases := map[Nanos]string{-1: "-0.000000001", -1_500_000_000: "-1.5", math.MinInt64: "-9223372036.854775808"}
	for n, want := range cases {
		if got := n.String(); got != want {
			t.Errorf("Nanos(%d).String() = %s, want %s", int64(n), got, want)
		}
	}
}

func TestAmountFromFloatReadsMinusZeroAsZero(t *testing.T) {
	if n, err := AmountFromFloat(math.Copysign(0, -1)); err != nil || n != 0 {
		t.Errorf("AmountFromFloat(-0) = %d, %v; want 0", int64(n), err)
	}
}

// An amount is usable from zero to MaxNanos and not past either end.
func TestNanosValidate(t *testing.T) {
	for _, n := range []Nanos{0, 1, MaxNanos} {
		if err := n.Validate(); err != nil {
			t.Errorf("Validate(%d) = %v, want nil", int64(n), err)
		}
	}
	for _, n := range []Nanos{-1, MaxNanos + 1} {
		if err := n.Validate(); !errors.Is(err, ErrInvalidAmount) {
			t.Errorf("Validate(%d) = %v, want ErrInvalidAmount", int64(n), err)
		}
	}
}
