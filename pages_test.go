package limes

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"
)

// fakePage is one page a fakeLister serves for a cursor.
type fakePage struct {
	ids  []uuid.UUID
	next string
	err  error
}

// fakeLister serves pages by cursor and records the cursors it was asked for.
type fakeLister struct {
	pages map[string]fakePage
	asked []string
	reqs  []ListByPrincipalRequest
}

func (f *fakeLister) ListByPrincipal(_ context.Context, req ListByPrincipalRequest) ([]Capability, string, error) {
	f.asked = append(f.asked, req.Cursor)
	f.reqs = append(f.reqs, req)
	p := f.pages[req.Cursor]
	caps := make([]Capability, 0, len(p.ids))
	for _, id := range p.ids {
		caps = append(caps, Capability{ID: id})
	}
	return caps, p.next, p.err
}

func (f *fakeLister) ListTenantBudgets(_ context.Context, req ListTenantBudgetsRequest) ([]TenantBudgetSummary, string, error) {
	f.asked = append(f.asked, req.Cursor)
	p := f.pages[req.Cursor]
	rows := make([]TenantBudgetSummary, 0, len(p.ids))
	for _, id := range p.ids {
		rows = append(rows, TenantBudgetSummary{TenantID: id})
	}
	return rows, p.next, p.err
}

// The cursors of a three-page listing.
const (
	cursorSecond = "second"
	cursorThird  = "third"
)

var (
	idA, idB, idC, idD = uuid.New(), uuid.New(), uuid.New(), uuid.New()
	errStore           = errors.New("store unavailable")
)

func threePages() map[string]fakePage {
	return map[string]fakePage{
		"":           {ids: []uuid.UUID{idA, idB}, next: cursorSecond},
		cursorSecond: {ids: []uuid.UUID{idC}, next: cursorThird},
		cursorThird:  {ids: []uuid.UUID{idD}},
	}
}

// walk collects what AllByPrincipal yields: the ids, and the errors.
func walk(ctx context.Context, l CapabilityLister, req ListByPrincipalRequest) ([]uuid.UUID, []error) {
	var ids []uuid.UUID
	var errs []error
	for c, err := range AllByPrincipal(ctx, l, req) {
		if err != nil {
			errs = append(errs, err)
			continue
		}
		ids = append(ids, c.ID)
	}
	return ids, errs
}

func TestAllByPrincipal(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	tests := map[string]struct {
		ctx       context.Context
		pages     map[string]fakePage
		cursor    string
		wantIDs   []uuid.UUID
		wantErr   error
		wantAsked []string
	}{
		"every page in order": {
			pages:     threePages(),
			wantIDs:   []uuid.UUID{idA, idB, idC, idD},
			wantAsked: []string{"", cursorSecond, cursorThird},
		},
		"from the request's cursor": {
			pages:     threePages(),
			cursor:    cursorSecond,
			wantIDs:   []uuid.UUID{idC, idD},
			wantAsked: []string{cursorSecond, cursorThird},
		},
		"an empty listing": {
			pages:     map[string]fakePage{},
			wantAsked: []string{""},
		},
		"an error ends the walk, after what came before it": {
			pages: map[string]fakePage{
				"":           {ids: []uuid.UUID{idA}, next: cursorSecond},
				cursorSecond: {err: errStore},
			},
			wantIDs:   []uuid.UUID{idA},
			wantErr:   errStore,
			wantAsked: []string{"", cursorSecond},
		},
		"a page that repeats its cursor": {
			pages: map[string]fakePage{
				"":           {ids: []uuid.UUID{idA}, next: cursorSecond},
				cursorSecond: {ids: []uuid.UUID{idB}, next: cursorSecond},
			},
			wantIDs:   []uuid.UUID{idA, idB},
			wantErr:   ErrCursorRepeated,
			wantAsked: []string{"", cursorSecond},
		},
		"a cancelled context fetches nothing": {
			ctx:     cancelled,
			pages:   threePages(),
			wantErr: context.Canceled,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			ctx := tc.ctx
			if ctx == nil {
				ctx = context.Background()
			}
			l := &fakeLister{pages: tc.pages}
			ids, errs := walk(ctx, l, ListByPrincipalRequest{Cursor: tc.cursor})

			if !slices.Equal(ids, tc.wantIDs) {
				t.Errorf("ids = %v, want %v", ids, tc.wantIDs)
			}
			if tc.wantErr == nil && len(errs) > 0 {
				t.Errorf("errors = %v, want none", errs)
			}
			if tc.wantErr != nil && (len(errs) != 1 || !errors.Is(errs[0], tc.wantErr)) {
				t.Errorf("errors = %v, want exactly %v", errs, tc.wantErr)
			}
			if !slices.Equal(l.asked, tc.wantAsked) {
				t.Errorf("cursors asked = %q, want %q", l.asked, tc.wantAsked)
			}
		})
	}
}

// Every page is asked with the caller's request, only the cursor moving.
func TestAllByPrincipalKeepsTheRequest(t *testing.T) {
	const pageSize = 2
	req := ListByPrincipalRequest{
		TenantID: uuid.New(), PrincipalType: PrincipalAgent, Subject: "agent",
		IncludeExpired: true, Limit: pageSize,
	}
	l := &fakeLister{pages: threePages()}
	walk(context.Background(), l, req)

	for i, got := range l.reqs {
		want := req
		want.Cursor = l.asked[i]
		if got != want {
			t.Errorf("page %d asked with %+v, want %+v", i, got, want)
		}
	}
}

// Breaking out of the loop fetches no further page.
func TestAllByPrincipalStopsWhenTheLoopBreaks(t *testing.T) {
	l := &fakeLister{pages: threePages()}
	for c, err := range AllByPrincipal(context.Background(), l, ListByPrincipalRequest{}) {
		if err != nil || c.ID != idA {
			t.Fatalf("first = %v, %v; want %s", c.ID, err, idA)
		}
		break
	}
	if !slices.Equal(l.asked, []string{""}) {
		t.Errorf("cursors asked = %q, want only the first page's", l.asked)
	}
}

func TestAllTenantBudgets(t *testing.T) {
	l := &fakeLister{pages: threePages()}
	var ids []uuid.UUID
	for row, err := range AllTenantBudgets(context.Background(), l, ListTenantBudgetsRequest{}) {
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, row.TenantID)
	}
	if want := []uuid.UUID{idA, idB, idC, idD}; !slices.Equal(ids, want) {
		t.Errorf("tenants = %v, want %v", ids, want)
	}
}
