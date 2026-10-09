package limes

import (
	"context"
	"errors"
	"iter"
)

// ErrCursorRepeated is a page that hands back the cursor it was fetched with.
// Following it would fetch the same page forever, so the walk stops there.
var ErrCursorRepeated = errors.New("limes: a page returned the cursor it was fetched with")

// CapabilityLister lists a principal's capabilities a page at a time. Every
// Store is one.
type CapabilityLister interface {
	ListByPrincipal(ctx context.Context, req ListByPrincipalRequest) ([]Capability, string, error)
}

// TenantBudgetLister lists tenant budgets a page at a time. Every
// TenantBudgets is one.
type TenantBudgetLister interface {
	ListTenantBudgets(ctx context.Context, req ListTenantBudgetsRequest) ([]TenantBudgetSummary, string, error)
}

// AllByPrincipal walks ListByPrincipal page by page, from req.Cursor, and
// yields every capability in its order. req.Limit sets the page size, not how
// many are yielded.
//
// An error is yielded once, with a zero Capability, and ends the walk;
// breaking out of the loop fetches no further page. The cursor is the
// contract for paging across requests — an API handing a page token to its
// client — and this is for a caller that wants the whole list in one go.
func AllByPrincipal(ctx context.Context, l CapabilityLister, req ListByPrincipalRequest) iter.Seq2[Capability, error] {
	return func(yield func(Capability, error) bool) {
		walkPages(ctx, req.Cursor, func(ctx context.Context, cursor string) ([]Capability, string, error) {
			req.Cursor = cursor
			return l.ListByPrincipal(ctx, req)
		}, yield)
	}
}

// AllTenantBudgets walks ListTenantBudgets page by page, from req.Cursor, and
// yields every row in its order, as AllByPrincipal does. Spend moves between
// pages, so a tenant may be yielded twice or not at all; see
// TenantBudgets.ListTenantBudgets.
func AllTenantBudgets(ctx context.Context, l TenantBudgetLister, req ListTenantBudgetsRequest) iter.Seq2[TenantBudgetSummary, error] {
	return func(yield func(TenantBudgetSummary, error) bool) {
		walkPages(ctx, req.Cursor, func(ctx context.Context, cursor string) ([]TenantBudgetSummary, string, error) {
			req.Cursor = cursor
			return l.ListTenantBudgets(ctx, req)
		}, yield)
	}
}

// walkPages fetches pages from cursor until one returns an empty cursor,
// yielding each item, and yields the first error it meets instead.
func walkPages[T any](
	ctx context.Context,
	cursor string,
	page func(ctx context.Context, cursor string) ([]T, string, error),
	yield func(T, error) bool,
) {
	var zero T
	for {
		if err := ctx.Err(); err != nil {
			yield(zero, err)
			return
		}
		items, next, err := page(ctx, cursor)
		if err != nil {
			yield(zero, err)
			return
		}
		for _, item := range items {
			if !yield(item, nil) {
				return
			}
		}
		switch next {
		case "":
			return
		case cursor:
			yield(zero, ErrCursorRepeated)
			return
		}
		cursor = next
	}
}
