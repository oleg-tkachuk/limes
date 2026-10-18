package limes

import (
	"context"
	"errors"
	"iter"
)

// Page is one page of a list: its items, and the cursor that fetches the
// next page — empty on the last one. The cursor is the store's own, opaque
// to the caller.
type Page[T any] struct {
	Items []T
	Next  string
}

// ErrCursorRepeated is a page that hands back a cursor the walk has already
// fetched — its own, or one further back. Following it would fetch the same
// pages forever, so the walk stops there.
var ErrCursorRepeated = errors.New("limes: a page returned a cursor already fetched")

// CapabilityLister lists a principal's capabilities a page at a time. Every
// Store is one.
type CapabilityLister interface {
	// ListByPrincipal returns one page (Store.ListByPrincipal).
	ListByPrincipal(ctx context.Context, req ListByPrincipalRequest) (Page[Capability], error)
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
		page := req // each range walks from req.Cursor with a request of its own
		AllPages(ctx, req.Cursor, func(ctx context.Context, cursor string) (Page[Capability], error) {
			page.Cursor = cursor
			return l.ListByPrincipal(ctx, page)
		})(yield)
	}
}

// AllPages walks a paged list from cursor, fetching each page with page
// until one returns an empty cursor, and yields every item in its order, for
// a lister of a package other than this one; AllByPrincipal is one walk.
//
// An error is yielded once, with a zero T, and ends the walk; so does a
// page handing back a cursor already fetched (ErrCursorRepeated). Breaking
// out of the loop fetches no further page.
func AllPages[T any](
	ctx context.Context,
	cursor string,
	page func(ctx context.Context, cursor string) (Page[T], error),
) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) { walkPages(ctx, cursor, page, yield) }
}

func walkPages[T any](
	ctx context.Context,
	cursor string,
	page func(ctx context.Context, cursor string) (Page[T], error),
	yield func(T, error) bool,
) {
	var zero T
	fetched := map[string]bool{}
	for {
		if err := ctx.Err(); err != nil {
			yield(zero, err)
			return
		}
		fetched[cursor] = true
		p, err := page(ctx, cursor)
		if err != nil {
			yield(zero, err)
			return
		}
		next := p.Next
		for _, item := range p.Items {
			if !yield(item, nil) {
				return
			}
		}
		if next == "" {
			return
		}
		if fetched[next] {
			yield(zero, ErrCursorRepeated)
			return
		}
		cursor = next
	}
}
