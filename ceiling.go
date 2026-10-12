package limes

import "fmt"

// A ceiling bounds what a capability, a copy of one or a tenant may use.
// Its zero value is a ceiling of nothing — no request, no spend — so a bound
// left out fails closed; Unlimited is the one way to lift it. No number
// stands for "no limit".

// RequestCeiling bounds a count of requests: at most Max, or none at all
// when Unlimited.
type RequestCeiling struct {
	// Max is the most requests allowed; 0 allows none. Within
	// 0..MaxIJSONInteger, and 0 when Unlimited.
	Max int64
	// Unlimited lifts the ceiling.
	Unlimited bool
}

// BudgetCeiling bounds an amount spent: at most Max, in the capability's
// unit, or none at all when Unlimited.
type BudgetCeiling struct {
	// Max is the most that may be spent; 0 allows nothing. Within
	// 0..MaxNanos, and 0 when Unlimited.
	Max Nanos
	// Unlimited lifts the ceiling.
	Unlimited bool
}

// unlimitedWithMax says what is wrong with a ceiling that both names a bound
// and lifts it.
const unlimitedWithMax = "a ceiling is either Unlimited or has a Max, not both"

// Validate refuses a negative or out-of-range Max, and a Max beside
// Unlimited, as ErrInvalidRequest.
func (c RequestCeiling) Validate() error {
	switch {
	case c.Unlimited && c.Max != 0:
		return invalidRequest("requests: %s (Max %d)", unlimitedWithMax, c.Max)
	case c.Max < 0 || c.Max > MaxIJSONInteger:
		return invalidRequest("requests: Max %d is outside 0..%d", c.Max, int64(MaxIJSONInteger))
	}
	return nil
}

// Validate refuses a Max beside Unlimited as ErrInvalidRequest, and a Max
// outside 0..MaxNanos as ErrInvalidAmount (Nanos.Validate).
func (c BudgetCeiling) Validate() error {
	if c.Unlimited && c.Max != 0 {
		return invalidRequest("budget: %s (Max %s)", unlimitedWithMax, c.Max)
	}
	if err := c.Max.Validate(); err != nil {
		return fmt.Errorf("budget: %w", err)
	}
	return nil
}

// Admits reports whether n requests in all stay under the ceiling.
func (c RequestCeiling) Admits(n int64) bool { return c.Unlimited || n <= c.Max }

// Admits reports whether amount spent in all stays under the ceiling.
func (c BudgetCeiling) Admits(amount Nanos) bool { return c.Unlimited || amount <= c.Max }

// Within reports whether c allows no more than outer: anything fits under
// an unlimited ceiling, and under a bounded one only a bounded ceiling no
// higher.
func (c RequestCeiling) Within(outer RequestCeiling) bool {
	return outer.Unlimited || (!c.Unlimited && c.Max <= outer.Max)
}

// Within reports whether c allows no more than outer.
func (c BudgetCeiling) Within(outer BudgetCeiling) bool {
	return outer.Unlimited || (!c.Unlimited && c.Max <= outer.Max)
}

// String is the ceiling for logs and errors: its Max, or "unlimited".
func (c RequestCeiling) String() string {
	if c.Unlimited {
		return unlimitedText
	}
	return fmt.Sprint(c.Max)
}

// String is the ceiling for logs and errors: its Max, or "unlimited".
func (c BudgetCeiling) String() string {
	if c.Unlimited {
		return unlimitedText
	}
	return c.Max.String()
}

// unlimitedText is how a lifted ceiling prints.
const unlimitedText = "unlimited"
