package meter

import (
	"context"
	"errors"
	"iter"
	"reflect"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes"
)

// budgetPage is one page a budgetLister serves for a cursor.
type budgetPage struct {
	ids  []uuid.UUID
	next string
}

// budgetLister serves tenant budgets by cursor and records the cursors it was
// asked for.
type budgetLister struct {
	pages map[string]budgetPage
	asked []string
}

func (l *budgetLister) ListTenantBudgets(_ context.Context, req ListTenantBudgetsRequest) (limes.Page[TenantBudget], error) {
	l.asked = append(l.asked, req.Cursor)
	p := l.pages[req.Cursor]
	rows := make([]TenantBudget, 0, len(p.ids))
	for _, id := range p.ids {
		rows = append(rows, TenantBudget{TenantID: id})
	}
	return limes.Page[TenantBudget]{Items: rows, Next: p.next}, nil
}

// The cursors of a three-page listing.
const (
	cursorSecond = "second"
	cursorThird  = "third"
)

var idA, idB, idC, idD = uuid.New(), uuid.New(), uuid.New(), uuid.New()

func threeBudgetPages() map[string]budgetPage {
	return map[string]budgetPage{
		"":           {ids: []uuid.UUID{idA, idB}, next: cursorSecond},
		cursorSecond: {ids: []uuid.UUID{idC}, next: cursorThird},
		cursorThird:  {ids: []uuid.UUID{idD}},
	}
}

func tenantIDs(t *testing.T, rows iter.Seq2[TenantBudget, error]) []uuid.UUID {
	t.Helper()
	var ids []uuid.UUID
	for row, err := range rows {
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, row.TenantID)
	}
	return ids
}

// AllTenantBudgets yields every row of every page, in order, walking
// through limes.AllPages from another package.
func TestAllTenantBudgets(t *testing.T) {
	l := &budgetLister{pages: threeBudgetPages()}
	got := tenantIDs(t, AllTenantBudgets(context.Background(), l, ListTenantBudgetsRequest{}))
	if want := []uuid.UUID{idA, idB, idC, idD}; !slices.Equal(got, want) {
		t.Errorf("tenants = %v, want %v", got, want)
	}
	if want := []string{"", cursorSecond, cursorThird}; !slices.Equal(l.asked, want) {
		t.Errorf("cursors asked = %q, want %q", l.asked, want)
	}
}

// A walk can be ranged again, and from where it was asked to start: each
// range pages with a request of its own, never the cursor a previous range
// reached.
func TestAllTenantBudgetsRangesAgain(t *testing.T) {
	want := []uuid.UUID{idC, idD}
	l := &budgetLister{pages: threeBudgetPages()}
	budgets := AllTenantBudgets(context.Background(), l, ListTenantBudgetsRequest{Cursor: cursorSecond})
	for pass := range 2 {
		if got := tenantIDs(t, budgets); !slices.Equal(got, want) {
			t.Errorf("range %d = %v, want %v", pass+1, got, want)
		}
	}
}

// A listing of tenant budgets refuses a negative page size.
func TestListTenantBudgetsRefusesANegativeLimit(t *testing.T) {
	if err := (ListTenantBudgetsRequest{Limit: -1}).Validate(); !errors.Is(err, limes.ErrInvalidRequest) {
		t.Errorf("ListTenantBudgetsRequest with Limit -1: Validate = %v, want ErrInvalidRequest", err)
	}
}

// The structs that may grow in 1.x are not comparable, so adding a slice or
// a map to one breaks no caller's ==.
func TestWhichStructsCompare(t *testing.T) {
	for _, ty := range []reflect.Type{
		reflect.TypeFor[ChargeRecord](), reflect.TypeFor[ChargeReceipt](), reflect.TypeFor[RefundReceipt](),
		reflect.TypeFor[BumpReceipt](), reflect.TypeFor[TenantBudget](), reflect.TypeFor[Usage](),
		reflect.TypeFor[PurgeOrphansResult](), reflect.TypeFor[ReleaseExpiredResult](), reflect.TypeFor[ChargeSum](),
	} {
		if ty.Comparable() {
			t.Errorf("%s is comparable; a field added in 1.x could break a caller's ==", ty.Name())
		}
	}
}
