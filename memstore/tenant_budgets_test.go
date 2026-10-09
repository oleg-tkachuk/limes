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
func seedBudget(t *testing.T, u *UsageStore[struct{}], ceiling, spent limes.Nanos) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	tenant := uuid.New()
	if _, err := u.SetTenantBudget(ctx, limes.SetTenantBudgetRequest{TenantID: tenant, MaxBudgetAmount: ceiling}); err != nil {
		t.Fatal(err)
	}
	if spent > 0 {
		if _, err := u.Charge(ctx, limes.ChargeRequest{
			CapabilityID: uuid.New(), TenantID: tenant, Amount: spent, Overrun: limes.OverrunRecord,
		}, nil); err != nil {
			t.Fatal(err)
		}
	}
	return tenant
}

func TestListTenantBudgets(t *testing.T) {
	ctx := context.Background()
	u := NewUsage[struct{}](nil)
	over := seedBudget(t, u, 10*unit, 15*unit) // past its ceiling: clamped to 100%
	half := seedBudget(t, u, 10*unit, 5*unit)  // 50%
	low := seedBudget(t, u, 10*unit, 1*unit)   // 10%
	unlimited := seedBudget(t, u, 0*unit, 3*unit)

	cases := []struct {
		name string
		req  limes.ListTenantBudgetsRequest
		want []uuid.UUID
	}{
		{"most at risk first", limes.ListTenantBudgetsRequest{}, []uuid.UUID{over, half, low, unlimited}},
		{"threshold", limes.ListTenantBudgetsRequest{ThresholdPct: 50}, []uuid.UUID{over, half}},
		{"unlimited only", limes.ListTenantBudgetsRequest{UnlimitedOnly: true}, []uuid.UUID{unlimited}},
		{"limit", limes.ListTenantBudgetsRequest{Limit: 2}, []uuid.UUID{over, half}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _, err := u.ListTenantBudgets(ctx, tc.req)
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
	got, _, _ := u.ListTenantBudgets(ctx, limes.ListTenantBudgetsRequest{Limit: 1})
	if got[0].UtilisationPct != maxUtilisationPct {
		t.Errorf("a tenant past its ceiling reads %v%%, want %d%%", got[0].UtilisationPct, maxUtilisationPct)
	}
}

// ResetSpend closes the period: the spend goes to zero and the period starts
// again; without it the ceiling changes and the spend stands.
func TestSetTenantBudgetResetSpend(t *testing.T) {
	ctx := context.Background()
	u := NewUsage[struct{}](nil)
	tenant := seedBudget(t, u, 10*unit, 4*unit)
	b, _ := u.GetTenantBudget(ctx, tenant)

	b, err := u.SetTenantBudget(ctx, limes.SetTenantBudgetRequest{
		TenantID: tenant, MaxBudgetAmount: limes.MustParseAmount("20"), ExpectedVersion: b.ResourceVersion,
	})
	if err != nil || b.SpentAmount != 4*unit || b.MaxBudgetAmount != 20*unit {
		t.Fatalf("raising the ceiling = %+v, %v; want 20 with 4 still spent", b, err)
	}
	start := b.PeriodStart
	b, err = u.SetTenantBudget(ctx, limes.SetTenantBudgetRequest{
		TenantID: tenant, MaxBudgetAmount: limes.MustParseAmount("20"), ResetSpend: true, ExpectedVersion: b.ResourceVersion,
	})
	if err != nil || b.SpentAmount != 0 || b.PeriodStart.Before(start) {
		t.Fatalf("closing the period = %+v, %v; want nothing spent and a new period", b, err)
	}
	if _, err := u.SetTenantBudget(ctx, limes.SetTenantBudgetRequest{
		TenantID: tenant, MaxBudgetAmount: limes.MustParseAmount("30"), ExpectedVersion: b.ResourceVersion - 1,
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
	u := NewUsage[struct{}](nil)
	further := seedBudget(t, u, 10*unit, 20*unit) // 200%
	past := seedBudget(t, u, 10*unit, 12*unit)    // 120%
	half := seedBudget(t, u, 10*unit, 5*unit)
	tie := []uuid.UUID{seedBudget(t, u, 10*unit, 1*unit), seedBudget(t, u, 10*unit, 1*unit)}
	slices.SortFunc(tie, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
	want := append([]uuid.UUID{further, past, half}, tie...)

	const pageSize = 2
	var got []uuid.UUID
	req := limes.ListTenantBudgetsRequest{Limit: pageSize}
	for pages := 0; ; pages++ {
		page, next, err := u.ListTenantBudgets(ctx, req)
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

	for _, bad := range []string{"garbage", limes.TenantBudgetCursor{Utilisation: "x", TenantID: half}.Encode()} {
		if _, _, err := u.ListTenantBudgets(ctx, limes.ListTenantBudgetsRequest{Cursor: bad}); !errors.Is(err, limes.ErrInvalidRequest) {
			t.Errorf("cursor %q: err = %v, want ErrInvalidRequest", bad, err)
		}
	}
}
