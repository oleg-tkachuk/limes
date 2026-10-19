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

// BudgetCeiling bounds an amount spent: at most Max of UnitCode, or any
// amount of it when Unlimited. The unit is part of the bound, as a currency
// is part of an amount: a ceiling that lets anything be spent names what,
// and the zero ceiling, which lets nothing be, needs none.
type BudgetCeiling struct {
	// Max is the most that may be spent; 0 allows nothing. Within
	// 0..MaxNanos, and 0 when Unlimited.
	Max Nanos
	// Unlimited lifts the ceiling.
	Unlimited bool
	// UnitCode is the unit Max counts in: an ISO 4217 code (IsUnitCode),
	// AbstractUnitCode for a budget that is not money. Required when the
	// ceiling lets anything be spent; may be empty on the zero ceiling.
	UnitCode string
}

// CopyQuota bounds how many Biscuit copies of a capability a meter counts
// — the copies that carry limits of their own, each a counter it keeps: at
// most Max, or as many as the store allows when Unlimited. It counts the
// copies of the capability and of every capability delegated from it, as
// its request and budget ceilings count theirs, so a delegated child does
// not multiply them, and goes on counting them once a child's usage is
// dropped. A store counts no more than its own bound per
// capability either (meter.DefaultMaxCopiesPerCapability), whatever the
// quota.
type CopyQuota struct {
	// Max is the most copies counted; 0 counts none, so a copy with limits
	// of its own is refused. Within 0..MaxIJSONInteger, and 0 when
	// Unlimited.
	Max int64
	// Unlimited lifts the quota to the store's bound.
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

// Validate refuses a negative or out-of-range Max, and a Max beside
// Unlimited, as ErrInvalidRequest.
func (q CopyQuota) Validate() error {
	switch {
	case q.Unlimited && q.Max != 0:
		return invalidRequest("copies: %s (Max %d)", unlimitedWithMax, q.Max)
	case q.Max < 0 || q.Max > MaxIJSONInteger:
		return invalidRequest("copies: Max %d is outside 0..%d", q.Max, int64(MaxIJSONInteger))
	}
	return nil
}

// Validate refuses a Max beside Unlimited, a unit that is not an ISO 4217
// code, and a ceiling that lets something be spent without naming its unit,
// as ErrInvalidRequest; a Max outside 0..MaxNanos is ErrInvalidAmount
// (Nanos.Validate), which matches ErrInvalidRequest too.
func (c BudgetCeiling) Validate() error {
	if c.Unlimited && c.Max != 0 {
		return invalidRequest("budget: %s (Max %s)", unlimitedWithMax, c.Max)
	}
	if err := c.Max.Validate(); err != nil {
		return fmt.Errorf("budget: %w", err)
	}
	if err := validUnitCode(c.UnitCode); err != nil {
		return fmt.Errorf("budget: %w", err)
	}
	if c.permitsSpend() && c.UnitCode == "" {
		return invalidRequest("budget %s names no unit_code", c)
	}
	return nil
}

// permitsSpend reports whether the ceiling lets anything be spent, which
// only a ceiling in a unit can.
func (c BudgetCeiling) permitsSpend() bool { return c.Unlimited || c.Max > 0 }

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

// Within reports whether q allows no more copies than outer.
func (q CopyQuota) Within(outer CopyQuota) bool {
	return outer.Unlimited || (!q.Unlimited && q.Max <= outer.Max)
}

// Admits reports whether n copies in all stay within the quota.
func (q CopyQuota) Admits(n int64) bool { return q.Unlimited || n <= q.Max }

// Bounded is the quota a store holding at most bound copies applies: q, or
// bound when q allows more.
func (q CopyQuota) Bounded(bound int64) int64 {
	if q.Unlimited || q.Max > bound {
		return bound
	}
	return q.Max
}

// String is the quota for logs and errors: its Max, or "unlimited".
func (q CopyQuota) String() string {
	if q.Unlimited {
		return unlimitedText
	}
	return fmt.Sprint(q.Max)
}

// Within reports whether c allows no more than outer, by amount alone:
// whether the two count in one unit is the caller's to check, and to refuse
// as ErrUnitCodeMismatch rather than as a wider ceiling.
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

// String is the ceiling for logs and errors: its Max, or "unlimited", and
// its unit when it names one.
func (c BudgetCeiling) String() string {
	amount := c.Max.String()
	if c.Unlimited {
		amount = unlimitedText
	}
	if c.UnitCode == "" {
		return amount
	}
	return amount + " " + c.UnitCode
}

// unlimitedText is how a lifted ceiling prints.
const unlimitedText = "unlimited"
