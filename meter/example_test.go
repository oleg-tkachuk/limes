package meter_test

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/memstore"
	"github.com/oleg-tkachuk/limes/meter"
)

// exampleStart is the time the examples' charges are taken at.
var exampleStart = time.Date(2026, time.March, 2, 9, 0, 0, 0, time.UTC)

// chargedTenants records one capability in each of n tenants, charges each
// tenant's i+1 USD, and returns the usage store and the tenants.
func chargedTenants(n int) (*memstore.UsageStore, []uuid.UUID) {
	ctx := context.Background()
	clock := func() time.Time { return exampleStart }
	records, err := memstore.NewStore(memstore.StoreConfig{Now: clock})
	if err != nil {
		log.Fatal(err)
	}
	usage, err := memstore.NewUsageStore(memstore.UsageStoreConfig{Records: records, Now: clock})
	if err != nil {
		log.Fatal(err)
	}
	tenants := make([]uuid.UUID, n)
	for i := range tenants {
		tenants[i] = uuid.New()
		c := limes.Capability{
			ID: uuid.New(), IssuedAt: exampleStart, ExpiresAt: exampleStart.Add(time.Hour),
			Subject: limes.Principal{Type: limes.PrincipalTypeService, TenantID: tenants[i], Subject: "svc"},
			Caveats: limes.Caveats{
				Ops: []limes.Op{limes.OpGet}, Requests: limes.RequestCeiling{Unlimited: true},
				Budget: limes.BudgetCeiling{Unlimited: true, UnitCode: "USD"},
			},
		}
		if err := records.Insert(ctx, limes.InsertRequest{Capability: c}); err != nil {
			log.Fatal(err)
		}
		subject, err := limes.StoredMeterSubject(ctx, records, c.ID)
		if err != nil {
			log.Fatal(err)
		}
		if _, err := usage.SetTenantBudget(ctx, meter.SetTenantBudgetRequest{
			TenantID: tenants[i], Ceiling: limes.BudgetCeiling{Max: limes.MustParseAmount("10"), UnitCode: "USD"},
		}); err != nil {
			log.Fatal(err)
		}
		if _, err := usage.Charge(ctx, meter.ChargeRequest{
			Subject: subject, Amount: limes.Nanos(i+1) * limes.NanosPerUnit,
		}, nil); err != nil {
			log.Fatal(err)
		}
	}
	return usage, tenants
}

func ExampleAllChargeSums() {
	usage, _ := chargedTenants(3)
	ledger := memstore.NewLedger(usage)

	// Every tenant's spend, largest first, a page of one at a time.
	for sum, err := range meter.AllChargeSums(context.Background(), ledger, meter.SumChargesRequest{
		AllTenants: true, From: exampleStart, To: exampleStart.Add(time.Hour),
		GroupBy: []meter.ChargeDimension{meter.ChargeDimensionTenant}, Limit: 1,
	}) {
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(sum.Charged, sum.UnitCode)
	}
	// Output:
	// 3 USD
	// 2 USD
	// 1 USD
}

func ExampleAllTenantBudgets() {
	usage, _ := chargedTenants(3)
	// The tenants at or past a fifth of their ceiling, a page of one at a time.
	const fifthBasisPoints = meter.BasisPointsPerWhole / 5
	for budget, err := range meter.AllTenantBudgets(context.Background(), usage, meter.ListTenantBudgetsRequest{
		Filter: meter.TenantBudgetFilterAtOrPast, ThresholdBasisPoints: fifthBasisPoints, Limit: 1,
	}) {
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(budget.SpentAmount, budget.Ceiling)
	}
	// Unordered output:
	// 2 10 USD
	// 3 10 USD
}

func ExampleTenantBudget_Utilization() {
	budget := meter.TenantBudget{
		Ceiling:     limes.BudgetCeiling{Max: limes.MustParseAmount("10"), UnitCode: "USD"},
		SpentAmount: limes.MustParseAmount("2.5"),
	}
	fmt.Println(budget.Utilization())

	// Past the ceiling is clamped to 100; an unlimited tenant reports none.
	budget.SpentAmount = limes.MustParseAmount("12")
	fmt.Println(budget.Utilization())
	fmt.Println(meter.TenantBudget{Ceiling: limes.BudgetCeiling{Unlimited: true, UnitCode: "USD"}}.Utilization())
	// Output:
	// 25 true
	// 100 true
	// 0 false
}

func ExampleChargeBucket_Start() {
	at := time.Date(2026, time.March, 5, 14, 30, 0, 0, time.UTC) // a Thursday
	for _, b := range []meter.ChargeBucket{meter.ChargeBucketHour, meter.ChargeBucketDay, meter.ChargeBucketWeek, meter.ChargeBucketMonth} {
		fmt.Println(b, b.Start(at).Format(time.DateTime))
	}
	// Output:
	// hour 2026-03-05 14:00:00
	// day 2026-03-05 00:00:00
	// week 2026-03-02 00:00:00
	// month 2026-03-01 00:00:00
}

func ExampleSumChargesRequest_Validate() {
	req := meter.SumChargesRequest{
		TenantID: uuid.New(), From: exampleStart, To: exampleStart.Add(24 * time.Hour),
		Bucket: meter.ChargeBucketHour, GroupBy: []meter.ChargeDimension{meter.ChargeDimensionOp},
	}
	fmt.Println(req.Validate(), req.Groups(meter.ChargeDimensionOp), req.Groups(meter.ChargeDimensionActor))

	// A period with no end is refused rather than read as "until now".
	req.To = time.Time{}
	fmt.Println(errors.Is(req.Validate(), limes.ErrInvalidRequest))
	// Output:
	// <nil> true false
	// true
}

func ExampleChargeFilter_Admits() {
	tenant := uuid.New()
	filter := meter.ChargeFilter{Ops: []limes.Op{"index:search"}}
	fmt.Println(filter.Admits(tenant, "index:search", "researcher", uuid.New()))
	fmt.Println(filter.Admits(tenant, "model:embed", "researcher", uuid.New()))
	// An empty filter keeps every charge.
	fmt.Println(meter.ChargeFilter{}.Admits(tenant, "model:embed", "", uuid.New()))
	// Output:
	// true
	// false
	// true
}

func ExampleValidateExternalRef() {
	fmt.Println(meter.ValidateExternalRef("invoice-2026-03-0042"), meter.ValidateExternalRef(""))
	fmt.Println(errors.Is(meter.ValidateExternalRef(strings.Repeat("x", meter.MaxExternalRefBytes+1)), limes.ErrInvalidRequest))
	fmt.Println(errors.Is(meter.ValidateExternalRef("line\nbreak"), limes.ErrInvalidRequest))
	// Output:
	// <nil> <nil>
	// true
	// true
}

func ExampleHousekeepingLimit() {
	fmt.Println(meter.HousekeepingLimit(0), meter.HousekeepingLimit(200))
	// Output:
	// 1000 200
}

func ExampleOverrunPolicy() {
	fmt.Println(meter.OverrunPolicyUnspecified, meter.OverrunPolicyReject, meter.OverrunPolicyRecord)
	// Output:
	// unspecified reject record
}
