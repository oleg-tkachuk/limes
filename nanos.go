package limes

import (
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
)

// Nanos is an amount of a currency or metering unit (see UnitCode) in
// billionths of the unit: 1.5 USD is 1_500_000_000. Every amount the module
// counts — a charge, a ceiling, a hold, a refund — is Nanos, so spend sums,
// compares and refunds exactly, however many charges it takes. Float64
// appears only at the edges, for display (Float64) and for a caller whose
// prices arrive as floats (AmountFromFloat).
//
// In JSON, and so in a capability token, an amount is a plain decimal number
// of units ("MaxBudgetAmount": 1.5), written and read without a float64 in
// between.
type Nanos int64

// NanosPerUnit is how many nanos make one unit.
const NanosPerUnit = 1_000_000_000

// nanosDecimals is the number of decimal places a Nanos carries.
const nanosDecimals = 9

// MaxNanos is the largest amount the module counts: just under a billion
// units, which is what a column of numeric(18,9) holds. Bounding every
// amount and counter by it leaves the sum of any two inside int64.
const MaxNanos Nanos = 999_999_999_999_999_999

// nanosPerUnitRat is NanosPerUnit as a big.Rat, for exact decimal parsing.
var nanosPerUnitRat = new(big.Rat).SetInt64(NanosPerUnit)

// ParseAmount reads a decimal amount of units — "0.35", "25", "1e-7" — into
// Nanos exactly. It refuses a negative amount, one finer than a nano and one
// beyond MaxNanos, rather than rounding any of them.
func ParseAmount(s string) (Nanos, error) {
	n, exact, err := parseDecimal(s)
	if err != nil {
		return 0, err
	}
	if !exact {
		return 0, fmt.Errorf("%w: %q is finer than a nano", ErrInvalidAmount, s)
	}
	return n, nil
}

// MustParseAmount is ParseAmount for an amount written into the program, a
// price or a test value: it panics on one ParseAmount refuses.
func MustParseAmount(s string) Nanos {
	n, err := ParseAmount(s)
	if err != nil {
		panic(err)
	}
	return n
}

// AmountFromFloat converts an amount held as a float64 to Nanos, rounding to
// the nearest nano, halves to even. It reads the float as the shortest
// decimal that identifies it — 0.1 as "0.1", the sum 0.1+0.2 as
// "0.30000000000000004" — so an amount written with at most nine decimals
// and fifteen significant digits converts exactly. It refuses what
// ValidateAmount refuses, and NaN and the infinities.
func AmountFromFloat(f float64) (Nanos, error) {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, fmt.Errorf("%w: %v is not a number", ErrInvalidAmount, f)
	}
	n, _, err := parseDecimal(strconv.FormatFloat(f, 'g', -1, 64))
	return n, err
}

// parseDecimal reads s as a decimal number of units and rounds it to the
// nearest nano, halves to even, reporting whether that was exact.
func parseDecimal(s string) (Nanos, bool, error) {
	// big.Rat also reads "a/b"; an amount is a decimal, never a fraction.
	if strings.ContainsRune(s, '/') {
		return 0, false, fmt.Errorf("%w: %q is not a decimal amount", ErrInvalidAmount, s)
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return 0, false, fmt.Errorf("%w: %q is not a decimal amount", ErrInvalidAmount, s)
	}
	if r.Sign() < 0 {
		return 0, false, fmt.Errorf("%w: %s is negative", ErrInvalidAmount, s)
	}
	r.Mul(r, nanosPerUnitRat)
	q, rem := new(big.Int).QuoRem(r.Num(), r.Denom(), new(big.Int))
	exact := rem.Sign() == 0
	if !exact {
		// Round half to even on the remainder's share of the denominator.
		switch new(big.Int).Lsh(rem, 1).Cmp(r.Denom()) {
		case 1:
			q.Add(q, big.NewInt(1))
		case 0:
			if q.Bit(0) == 1 {
				q.Add(q, big.NewInt(1))
			}
		}
	}
	if !q.IsInt64() || Nanos(q.Int64()) > MaxNanos {
		return 0, false, fmt.Errorf("%w: %s exceeds %s", ErrInvalidAmount, s, MaxNanos)
	}
	return Nanos(q.Int64()), exact, nil
}

// String writes n as a decimal number of units with no trailing zeros:
// "1.5", "0.000000001", "25".
func (n Nanos) String() string {
	sign, abs := "", uint64(n)
	if n < 0 {
		sign, abs = "-", uint64(-n)
	}
	units, frac := abs/NanosPerUnit, abs%NanosPerUnit
	if frac == 0 {
		return sign + strconv.FormatUint(units, 10)
	}
	digits := fmt.Sprintf("%0*d", nanosDecimals, frac)
	return sign + strconv.FormatUint(units, 10) + "." + strings.TrimRight(digits, "0")
}

// Float64 is n in units, the nearest float64. For display and metrics only:
// never count with it.
func (n Nanos) Float64() float64 {
	units, frac := int64(n)/NanosPerUnit, int64(n)%NanosPerUnit
	return float64(units) + float64(frac)/NanosPerUnit
}

// MarshalJSON writes n as a decimal number of units.
func (n Nanos) MarshalJSON() ([]byte, error) { return []byte(n.String()), nil }

// UnmarshalJSON reads a JSON number of units: exactly, when it has at most
// nine decimals. A token issued while amounts were float64 may carry the
// float's noise past the ninth decimal (0.30000000000000004); that is rounded
// to the nearest nano, which is the amount it was issued for.
func (n *Nanos) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		return nil
	}
	v, _, err := parseDecimal(string(b))
	if err != nil {
		return err
	}
	*n = v
	return nil
}

// MarshalText writes n as a decimal number of units, as String does.
func (n Nanos) MarshalText() ([]byte, error) { return []byte(n.String()), nil }

// UnmarshalText reads a decimal number of units exactly, as ParseAmount
// does: configuration that carries an amount — a YAML scalar, an
// environment variable — reads it without a float64 in between.
func (n *Nanos) UnmarshalText(b []byte) error {
	v, err := ParseAmount(string(b))
	if err != nil {
		return err
	}
	*n = v
	return nil
}
