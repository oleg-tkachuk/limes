package pgstore

import (
	"errors"
	"testing"

	"github.com/oleg-tkachuk/limes"
)

// Every filter limes defines is one this store names, each as what it
// keeps; one limes adds later fails here until the store names it, rather
// than list every tenant for a filter that narrows.
func TestKeepOfNamesEveryFilter(t *testing.T) {
	want := map[limes.TenantBudgetFilter]string{
		limes.TenantBudgetFilterUnspecified: keepEvery,
		limes.TenantBudgetFilterAll:         keepEvery,
		limes.TenantBudgetFilterAtOrPast:    keepAtOrPast,
		limes.TenantBudgetFilterUnlimited:   keepUnlimited,
	}
	f := limes.TenantBudgetFilter(0)
	for ; f.Validate() == nil; f++ {
		got, err := keepOf(f)
		if w, ok := want[f]; !ok || err != nil || got != w {
			t.Errorf("keepOf(%s) = %q, %v; want %q", f, got, err, w)
		}
	}
	if len(want) != int(f) {
		t.Errorf("limes defines %d filters, the test names %d", f, len(want))
	}
	if _, err := keepOf(f); !errors.Is(err, limes.ErrInvalidRequest) {
		t.Errorf("keepOf(%s), a filter limes lacks: err = %v, want ErrInvalidRequest", f, err)
	}
}

// Every bucket limes defines is one this store names, each as its
// date_trunc field; one limes adds later fails here until the store names
// it, rather than sum by another.
func TestTruncOfNamesEveryBucket(t *testing.T) {
	want := map[limes.ChargeBucket]string{
		limes.ChargeBucketUnspecified: "",
		limes.ChargeBucketNone:        "",
		limes.ChargeBucketHour:        truncHour,
		limes.ChargeBucketDay:         truncDay,
		limes.ChargeBucketMonth:       truncMonth,
		limes.ChargeBucketWeek:        truncWeek,
	}
	b := limes.ChargeBucket(0)
	for ; b.Validate() == nil; b++ {
		got, bucketed, err := truncOf(b)
		if w, ok := want[b]; !ok || err != nil || got != w || bucketed != (w != "") {
			t.Errorf("truncOf(%s) = %q, %v, %v; want %q", b, got, bucketed, err, w)
		}
	}
	if len(want) != int(b) {
		t.Errorf("limes defines %d buckets, the test names %d", b, len(want))
	}
	if _, _, err := truncOf(b); !errors.Is(err, limes.ErrInvalidRequest) {
		t.Errorf("truncOf(%s), a bucket limes lacks: err = %v, want ErrInvalidRequest", b, err)
	}
}
