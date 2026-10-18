package pgstore

import (
	"errors"
	"testing"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/meter"
)

// Every filter limes defines is one this store names, each as what it
// keeps; one limes adds later fails here until the store names it, rather
// than list every tenant for a filter that narrows.
func TestKeepOfNamesEveryFilter(t *testing.T) {
	want := map[meter.TenantBudgetFilter]string{
		meter.TenantBudgetFilterUnspecified: keepEvery,
		meter.TenantBudgetFilterAll:         keepEvery,
		meter.TenantBudgetFilterAtOrPast:    keepAtOrPast,
		meter.TenantBudgetFilterUnlimited:   keepUnlimited,
	}
	f := meter.TenantBudgetFilter(0)
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
	want := map[meter.ChargeBucket]string{
		meter.ChargeBucketNone:  "",
		meter.ChargeBucketHour:  truncHour,
		meter.ChargeBucketDay:   truncDay,
		meter.ChargeBucketWeek:  truncWeek,
		meter.ChargeBucketMonth: truncMonth,
	}
	b := meter.ChargeBucket(0)
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
