package meter

import (
	"context"
	"iter"

	"github.com/oleg-tkachuk/limes"
)

// TenantBudgetLister lists tenant budgets a page at a time. Every
// TenantBudgets is one.
type TenantBudgetLister interface {
	// ListTenantBudgets returns one page (TenantBudgets.ListTenantBudgets).
	ListTenantBudgets(ctx context.Context, req ListTenantBudgetsRequest) (limes.Page[TenantBudget], error)
}

// AllTenantBudgets walks ListTenantBudgets page by page, from req.Cursor, and
// yields every row in its order, as AllByPrincipal does. Spend moves between
// pages, so a tenant may be yielded twice or not at all; see
// TenantBudgets.ListTenantBudgets.
func AllTenantBudgets(ctx context.Context, l TenantBudgetLister, req ListTenantBudgetsRequest) iter.Seq2[TenantBudget, error] {
	return func(yield func(TenantBudget, error) bool) {
		page := req // each range walks from req.Cursor with a request of its own
		limes.AllPages(ctx, req.Cursor, func(ctx context.Context, cursor string) (limes.Page[TenantBudget], error) {
			page.Cursor = cursor
			return l.ListTenantBudgets(ctx, page)
		})(yield)
	}
}

// AllChargeSums walks SumCharges page by page, from req.Cursor, and yields
// every sum in its order, as AllTenantBudgets does. A charge or a refund
// made during the walk moves a sum between pages, so a group may be yielded
// twice or not at all; see LedgerReader.SumCharges.
func AllChargeSums(ctx context.Context, l LedgerReader, req SumChargesRequest) iter.Seq2[ChargeSum, error] {
	return func(yield func(ChargeSum, error) bool) {
		page := req // each range walks from req.Cursor with a request of its own
		limes.AllPages(ctx, req.Cursor, func(ctx context.Context, cursor string) (limes.Page[ChargeSum], error) {
			page.Cursor = cursor
			return l.SumCharges(ctx, page)
		})(yield)
	}
}
