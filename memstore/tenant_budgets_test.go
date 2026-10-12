package memstore

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes"
)

// seedBudget sets a tenant's ceiling and charges spent against it.
func seedBudget(t *testing.T, u *UsageStore[struct{}], ceiling limes.BudgetCeiling, spent limes.Nanos) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	tenant := uuid.New()
	if _, err := u.SetTenantBudget(ctx, limes.SetTenantBudgetRequest{TenantID: tenant, Ceiling: ceiling}); err != nil {
		t.Fatal(err)
	}
	if spent > 0 {
		if _, err := u.Charge(ctx, limes.ChargeRequest{
			Subject: recordedSubject(ctx, t, u, subjectSpec{capID: uuid.New(), tenant: tenant}),
			Amount:  spent,
			Overrun: limes.OverrunRecord,
		}, nil); err != nil {
			t.Fatal(err)
		}
	}
	return tenant
}

func TestListTenantBudgets(t *testing.T) {
	ctx := context.Background()
	u := newTestUsage(newTestStore())
	over := seedBudget(t, u, limes.BudgetCeiling{Max: 10 * unit}, 15*unit) // past its ceiling: clamped to 100%
	half := seedBudget(t, u, limes.BudgetCeiling{Max: 10 * unit}, 5*unit)  // 50%
	low := seedBudget(t, u, limes.BudgetCeiling{Max: 10 * unit}, 1*unit)   // 10%
	unlimited := seedBudget(t, u, limes.BudgetCeiling{Unlimited: true}, 3*unit)

	cases := []struct {
		name string
		req  limes.ListTenantBudgetsRequest
		want []uuid.UUID
	}{
		{"most at risk first", limes.ListTenantBudgetsRequest{}, []uuid.UUID{over, half, low, unlimited}},
		{"threshold", limes.ListTenantBudgetsRequest{Filter: limes.TenantBudgetsAtOrPast, ThresholdPct: 50}, []uuid.UUID{over, half}},
		{"unlimited only", limes.ListTenantBudgetsRequest{Filter: limes.TenantBudgetsUnlimited}, []uuid.UUID{unlimited}},
		{"every ceiling", limes.ListTenantBudgetsRequest{Filter: limes.TenantBudgetsAtOrPast}, []uuid.UUID{over, half, low}},
		{"limit", limes.ListTenantBudgetsRequest{Limit: 2}, []uuid.UUID{over, half}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotPage, err := u.ListTenantBudgets(ctx, tc.req)
			got := gotPage.Items
			if err != nil {
				t.Fatal(err)
			}
			var ids []uuid.UUID
			for _, s := range got {
				ids = append(ids, s.TenantID)
			}
			if len(ids) != len(tc.want) {
				t.Fatalf("listed %v, want %v", ids, tc.want)
			}
			for i := range ids {
				if ids[i] != tc.want[i] {
					t.Fatalf("listed %v, want %v", ids, tc.want)
				}
			}
		})
	}
	page, _ := u.ListTenantBudgets(ctx, limes.ListTenantBudgetsRequest{Limit: 1})
	got := page.Items
	if pct, limited := got[0].Utilization(); !limited || pct != percent {
		t.Errorf("a tenant past its ceiling reads %v%% (limited %v), want %d%%", pct, limited, percent)
	}
}

// ResetSpend closes the period: the spend goes to zero and the period starts
// again; without it the ceiling changes and the spend stands.
func TestSetTenantBudgetResetSpend(t *testing.T) {
	ctx := context.Background()
	u := newTestUsage(newTestStore())
	tenant := seedBudget(t, u, limes.BudgetCeiling{Max: 10 * unit}, 4*unit)
	b, _ := u.GetTenantBudget(ctx, tenant)

	b, err := u.SetTenantBudget(ctx, limes.SetTenantBudgetRequest{
		TenantID: tenant, Ceiling: limes.BudgetCeiling{Max: limes.MustParseAmount("20")}, ExpectedVersion: b.ResourceVersion,
	})
	if err != nil || b.SpentAmount != 4*unit || b.Ceiling != (limes.BudgetCeiling{Max: 20 * unit}) {
		t.Fatalf("raising the ceiling = %+v, %v; want 20 with 4 still spent", b, err)
	}
	start := b.PeriodStart
	b, err = u.SetTenantBudget(ctx, limes.SetTenantBudgetRequest{
		TenantID: tenant, Ceiling: limes.BudgetCeiling{Max: limes.MustParseAmount("20")}, ResetSpend: true, ExpectedVersion: b.ResourceVersion,
	})
	if err != nil || b.SpentAmount != 0 || b.PeriodStart.Before(start) {
		t.Fatalf("closing the period = %+v, %v; want nothing spent and a new period", b, err)
	}
	if _, err := u.SetTenantBudget(ctx, limes.SetTenantBudgetRequest{
		TenantID: tenant, Ceiling: limes.BudgetCeiling{Max: limes.MustParseAmount("30")}, ExpectedVersion: b.ResourceVersion - 1,
	}); !errors.Is(err, limes.ErrTenantBudgetVersionMismatch) {
		t.Errorf("a stale version: err = %v, want ErrTenantBudgetVersionMismatch", err)
	}
	if _, err := u.GetTenantBudget(ctx, uuid.New()); !errors.Is(err, limes.ErrTenantBudgetNotFound) {
		t.Errorf("an unknown tenant: err = %v, want ErrTenantBudgetNotFound", err)
	}
}

// Pages cover every tenant once, in the order, and the further past its
// ceiling of two leads although both read 100%.
func TestListTenantBudgetsPages(t *testing.T) {
	ctx := context.Background()
	u := newTestUsage(newTestStore())
	further := seedBudget(t, u, limes.BudgetCeiling{Max: 10 * unit}, 20*unit) // 200%
	past := seedBudget(t, u, limes.BudgetCeiling{Max: 10 * unit}, 12*unit)    // 120%
	half := seedBudget(t, u, limes.BudgetCeiling{Max: 10 * unit}, 5*unit)
	tie := []uuid.UUID{seedBudget(t, u, limes.BudgetCeiling{Max: 10 * unit}, 1*unit), seedBudget(t, u, limes.BudgetCeiling{Max: 10 * unit}, 1*unit)}
	slices.SortFunc(tie, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
	want := append([]uuid.UUID{further, past, half}, tie...)

	const pageSize = 2
	var got []uuid.UUID
	req := limes.ListTenantBudgetsRequest{Limit: pageSize}
	for pages := 0; ; pages++ {
		pagePage, err := u.ListTenantBudgets(ctx, req)
		page, next := pagePage.Items, pagePage.Next
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range page {
			got = append(got, s.TenantID)
		}
		if next == "" {
			break
		}
		if pages > len(want) {
			t.Fatal("the cursor never reached the last page")
		}
		req.Cursor = next
	}
	if !slices.Equal(got, want) {
		t.Fatalf("paged %v, want %v", got, want)
	}

	var walked []uuid.UUID
	for s, err := range limes.AllTenantBudgets(ctx, u, limes.ListTenantBudgetsRequest{Limit: pageSize}) {
		if err != nil {
			t.Fatal(err)
		}
		walked = append(walked, s.TenantID)
	}
	if !slices.Equal(walked, want) {
		t.Fatalf("AllTenantBudgets walked %v, want %v", walked, want)
	}

	for _, bad := range []string{"garbage", tenantBudgetCursor{class: classBounded, tenantID: half}.encode()} {
		if _, err := u.ListTenantBudgets(ctx, limes.ListTenantBudgetsRequest{Cursor: bad}); !errors.Is(err, limes.ErrInvalidRequest) {
			t.Errorf("cursor %q: err = %v, want ErrInvalidRequest", bad, err)
		}
	}
}
