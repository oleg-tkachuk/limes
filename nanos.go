package limes

import (
	"fmt"
	"math"
	"math/big"
	"regexp"
	"strconv"
	"strings"
)

// Nanos is an amount of a currency or metering unit (see BudgetCeiling.UnitCode) in
// billionths of the unit: 1.5 USD is 1_500_000_000. Every amount the module
// counts — a charge, a ceiling, a hold, a refund — is Nanos, so spend sums,
// compares and refunds exactly, however many charges it takes. Float64
// appears only at the edges, for display (Float64) and for a caller whose
// prices arrive as floats (AmountFromFloat).
//
// In JSON, and so in a capability token, an amount is a decimal number of
// units in a string ("max_budget": "1.5"), written and read without a float64
// in between.
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
// Nanos.Validate refuses, and NaN and the infinities.
func AmountFromFloat(f float64) (Nanos, error) {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, fmt.Errorf("%w: %v is not a number", ErrInvalidAmount, f)
	}
	if f == 0 { // minus zero included, which would format with a sign
		return 0, nil
	}
	n, _, err := parseDecimal(strconv.FormatFloat(f, 'g', -1, 64))
	return n, err
}

// maxExponentDigits bounds the exponent of an amount written in scientific
// notation. Three digits reach far past every amount Nanos holds, and keep
// 1e999999 from costing the big.Rat it would build.
const maxExponentDigits = 3

// decimalAmount is the one form an amount is written in: digits, an optional
// fraction, an optional exponent — no sign, since an amount is never
// negative. big.Rat reads far more — fractions, hex, binary and octal
// floats, underscores — none of which is a decimal amount.
var decimalAmount = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?([eE][+-]?[0-9]{1,` +
	strconv.Itoa(maxExponentDigits) + `})?$`)

// parseDecimal reads s as a decimal number of units and rounds it to the
// nearest nano, halves to even, reporting whether that was exact.
func parseDecimal(s string) (Nanos, bool, error) {
	if strings.HasPrefix(s, "-") {
		return 0, false, fmt.Errorf("%w: %s is negative", ErrInvalidAmount, s)
	}
	if !decimalAmount.MatchString(s) {
		return 0, false, fmt.Errorf("%w: %q is not a decimal amount", ErrInvalidAmount, s)
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return 0, false, fmt.Errorf("%w: %q is not a decimal amount", ErrInvalidAmount, s)
	}
	q, exact := roundHalfEven(r.Mul(r, nanosPerUnitRat))
	if !q.IsInt64() || Nanos(q.Int64()) > MaxNanos {
		return 0, false, fmt.Errorf("%w: %s exceeds %s", ErrInvalidAmount, s, MaxNanos)
	}
	return Nanos(q.Int64()), exact, nil
}

// roundHalfEven is r, which is not negative, rounded to the nearest
// integer, halves to even, and whether that was exact.
func roundHalfEven(r *big.Rat) (*big.Int, bool) {
	q, rem := new(big.Int).QuoRem(r.Num(), r.Denom(), new(big.Int))
	if rem.Sign() == 0 {
		return q, true
	}
	// Round half to even on the remainder's share of the denominator.
	switch new(big.Int).Lsh(rem, 1).Cmp(r.Denom()) {
	case 1:
		q.Add(q, big.NewInt(1))
	case 0:
		if q.Bit(0) == 1 {
			q.Add(q, big.NewInt(1))
		}
	}
	return q, false
}

// String writes n as a decimal number of units with no trailing zeros:
// "1.5", "0.000000001", "25".
func (n Nanos) String() string {
	// The magnitude, as uint64: negating in two's complement makes it right
	// for every negative n, math.MinInt64 included.
	sign, abs := "", uint64(n) //nolint:gosec // G115: a negative n is negated below
	if n < 0 {
		sign, abs = "-", -abs
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

// MarshalJSON writes n as a JSON string holding a decimal number of units,
// "1.5": a string, so that a reader whose numbers are float64 — JavaScript's
// JSON.parse among them — cannot round it.
func (n Nanos) MarshalJSON() ([]byte, error) { return strconv.AppendQuote(nil, n.String()), nil }

// UnmarshalJSON reads what MarshalJSON writes, exactly, as ParseAmount does:
// a JSON string, never a number.
func (n *Nanos) UnmarshalJSON(b []byte) error {
	s, err := strconv.Unquote(string(b))
	if err != nil || len(b) == 0 || b[0] != '"' {
		return fmt.Errorf("%w: %s is not a JSON string", ErrInvalidAmount, b)
	}
	v, err := ParseAmount(s)
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

// Validate reports whether n is usable as a charge or refund amount: not
// negative and at most MaxNanos. Implementations call it before touching
// state.
func (n Nanos) Validate() error {
	if n < 0 || n > MaxNanos {
		return fmt.Errorf("%w: %s is outside 0..%s", ErrInvalidAmount, n, MaxNanos)
	}
	return nil
}
