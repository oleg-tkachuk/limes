package limes_test

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/oleg-tkachuk/limes"
)

func ExampleAmountFromFloat() {
	// A float price written with at most nine decimals converts exactly.
	price, err := limes.AmountFromFloat(0.35)
	fmt.Println(price, int64(price), err)

	// A float sum, 0.30000000000000004, rounds to the nearest nano.
	a, b := 0.1, 0.2
	sum, _ := limes.AmountFromFloat(a + b)
	fmt.Println(sum)

	_, err = limes.AmountFromFloat(-1)
	fmt.Println(errors.Is(err, limes.ErrInvalidAmount))
	// Output:
	// 0.35 350000000 <nil>
	// 0.3
	// true
}

func ExampleMustParseAmount() {
	// For an amount written into the program: it panics on a malformed one.
	price := limes.MustParseAmount("0.002")
	fmt.Println(price, price*3, int64(price))
	// Output:
	// 0.002 0.006 2000000
}

func ExampleNanos_String() {
	fmt.Println(limes.Nanos(1_500_000_000), limes.Nanos(1), limes.Nanos(25*limes.NanosPerUnit))
	// Float64 is for display and metrics only.
	fmt.Println(limes.MustParseAmount("1.25").Float64())
	// Output:
	// 1.5 0.000000001 25
	// 1.25
}

func ExampleNanos_MarshalJSON() {
	type price struct {
		Amount limes.Nanos `json:"amount"`
	}
	b, _ := json.Marshal(price{Amount: limes.MustParseAmount("0.1")})
	fmt.Println(string(b))

	// A JSON number is refused: it may already have passed through a float.
	var p price
	fmt.Println(json.Unmarshal([]byte(`{"amount":"12.5"}`), &p), p.Amount)
	fmt.Println(json.Unmarshal([]byte(`{"amount":12.5}`), &p) != nil)
	// Output:
	// {"amount":"0.1"}
	// <nil> 12.5
	// true
}

func ExampleNanos_UnmarshalText() {
	// Configuration — a YAML scalar, an environment variable — reads exactly.
	var budget limes.Nanos
	fmt.Println(budget.UnmarshalText([]byte("25.00")), budget)
	fmt.Println(errors.Is(budget.UnmarshalText([]byte("-1")), limes.ErrInvalidAmount))
	// Output:
	// <nil> 25
	// true
}

func ExampleNanos_Validate() {
	fmt.Println(limes.MaxNanos.Validate())
	fmt.Println(errors.Is(limes.Nanos(-1).Validate(), limes.ErrInvalidAmount))
	// Output:
	// <nil>
	// true
}

func ExampleIsUnitCode() {
	fmt.Println(limes.IsUnitCode("USD"), limes.IsUnitCode(limes.AbstractUnitCode))
	fmt.Println(limes.IsUnitCode("usd"), limes.IsUnitCode(""))
	// Output:
	// true true
	// false false
}

func ExampleBudgetCeiling() {
	ceiling := limes.BudgetCeiling{Max: limes.MustParseAmount("25"), UnitCode: "USD"}
	fmt.Println(ceiling, ceiling.Validate())
	fmt.Println(ceiling.Admits(limes.MustParseAmount("25")), ceiling.Admits(limes.MustParseAmount("25.01")))

	unlimited := limes.BudgetCeiling{Unlimited: true, UnitCode: "USD"}
	fmt.Println(unlimited, ceiling.Within(unlimited), unlimited.Within(ceiling))

	// A ceiling that lets something be spent names its unit.
	err := limes.BudgetCeiling{Max: limes.MustParseAmount("1")}.Validate()
	fmt.Println(errors.Is(err, limes.ErrInvalidRequest))
	// Output:
	// 25 USD <nil>
	// true false
	// unlimited USD true false
	// true
}

func ExampleRequestCeiling() {
	ceiling := limes.RequestCeiling{Max: 500}
	fmt.Println(ceiling, ceiling.Validate(), ceiling.Admits(500), ceiling.Admits(501))

	// The zero ceiling allows nothing; only Unlimited lifts it.
	fmt.Println(limes.RequestCeiling{}.Admits(1), limes.RequestCeiling{Unlimited: true}.Admits(1))
	fmt.Println(ceiling.Within(limes.RequestCeiling{Unlimited: true}), limes.RequestCeiling{Unlimited: true}.Within(ceiling))
	fmt.Println(errors.Is(limes.RequestCeiling{Max: 1, Unlimited: true}.Validate(), limes.ErrInvalidRequest))
	// Output:
	// 500 <nil> true false
	// false true
	// true false
	// true
}

func ExampleCopyQuota() {
	quota := limes.CopyQuota{Max: 3}
	fmt.Println(quota, quota.Validate(), quota.Admits(3), quota.Admits(4))

	// A store that holds at most two copies per capability applies two.
	const storeBound = 2
	fmt.Println(quota.Bounded(storeBound), limes.CopyQuota{Unlimited: true}.Bounded(storeBound))
	fmt.Println(quota.Within(limes.CopyQuota{Max: 5}), quota.Within(limes.CopyQuota{}))
	// Output:
	// 3 <nil> true false
	// 2 2
	// true false
}

func ExampleCopyCeiling() {
	requests := limes.RequestCeiling{Max: 10}
	copyLimit := limes.CopyCeiling{RevocationID: []byte{1}, Requests: &requests}
	fmt.Println(copyLimit.AdmitsRequests(10), copyLimit.AdmitsRequests(11))
	// With no budget of its own, the copy is bounded by the capability's alone.
	fmt.Println(copyLimit.AdmitsBudget(limes.MaxNanos))
	// Output:
	// true false
	// true
}
