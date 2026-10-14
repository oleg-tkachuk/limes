package limes

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Every bucket this module defines has a name of its own and starts where
// its UTC boundary falls, whatever zone the time is read in; one past them
// is refused.
func TestChargeBucketStart(t *testing.T) {
	zone := time.FixedZone("UTC+3", 3*int(time.Hour/time.Second))
	at := time.Date(2026, time.March, 1, 1, 30, 15, 0, zone) // 2026-02-28 22:30:15 UTC
	want := map[ChargeBucket]struct {
		name  string
		start time.Time
	}{
		ChargeBucketNone:  {"none", time.Time{}},
		ChargeBucketHour:  {"hour", time.Date(2026, time.February, 28, 22, 0, 0, 0, time.UTC)},
		ChargeBucketDay:   {"day", time.Date(2026, time.February, 28, 0, 0, 0, 0, time.UTC)},
		ChargeBucketMonth: {"month", time.Date(2026, time.February, 1, 0, 0, 0, 0, time.UTC)},
		ChargeBucketWeek:  {"week", time.Date(2026, time.February, 23, 0, 0, 0, 0, time.UTC)},
	}
	b := ChargeBucket(0)
	for ; b.Validate() == nil; b++ {
		w, ok := want[b]
		if !ok {
			t.Errorf("bucket %s is not one the test names", b)
			continue
		}
		if b.String() != w.name {
			t.Errorf("ChargeBucket(%d).String() = %q, want %q", uint8(b), b.String(), w.name)
		}
		if got := b.Start(at); !got.Equal(w.start) {
			t.Errorf("%s.Start(%s) = %s, want %s", b, at, got, w.start)
		}
	}
	if int(b) != len(want) {
		t.Errorf("the module defines %d buckets, the test names %d", b, len(want))
	}
	if !errors.Is(b.Validate(), ErrInvalidRequest) {
		t.Errorf("%s.Validate() = %v, want ErrInvalidRequest", b, b.Validate())
	}
}

// A week starts on the Monday at or before the day, at 00:00 UTC, across a
// month's and a year's end alike.
func TestChargeBucketWeekStart(t *testing.T) {
	monday := time.Date(2026, time.December, 28, 0, 0, 0, 0, time.UTC)
	for name, tc := range map[string]struct {
		at, want time.Time
	}{
		"Monday at midnight":           {monday, monday},
		"a nanosecond before it":       {monday.Add(-time.Nanosecond), monday.AddDate(0, 0, -7)},
		"Sunday, the week's last day":  {time.Date(2027, time.January, 3, 23, 59, 59, 0, time.UTC), monday},
		"the next year's first day":    {time.Date(2027, time.January, 1, 12, 0, 0, 0, time.UTC), monday},
		"the next Monday":              {time.Date(2027, time.January, 4, 0, 0, 0, 0, time.UTC), time.Date(2027, time.January, 4, 0, 0, 0, 0, time.UTC)},
		"a Monday east of UTC, Sunday": {time.Date(2027, time.January, 4, 1, 0, 0, 0, time.FixedZone("UTC+2", 2*int(time.Hour/time.Second))), monday},
	} {
		if got := ChargeBucketWeek.Start(tc.at); !got.Equal(tc.want) || got.Location() != time.UTC {
			t.Errorf("%s: Start(%s) = %s, want %s", name, tc.at, got, tc.want)
		}
	}
}

// A request is refused for what it lacks or gets wrong, each as
// ErrInvalidRequest.
func TestSumChargesRequestValidate(t *testing.T) {
	from := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	ok := SumChargesRequest{TenantID: uuid.New(), From: from, To: from.Add(time.Hour)}
	if err := ok.Validate(); err != nil {
		t.Fatalf("a whole request: %v", err)
	}
	for name, change := range map[string]func(*SumChargesRequest){
		"no tenant":         func(r *SumChargesRequest) { r.TenantID = uuid.Nil },
		"no From":           func(r *SumChargesRequest) { r.From = time.Time{} },
		"no To":             func(r *SumChargesRequest) { r.To = time.Time{} },
		"an empty period":   func(r *SumChargesRequest) { r.To = r.From },
		"a negative limit":  func(r *SumChargesRequest) { r.Limit = -1 },
		"an unknown bucket": func(r *SumChargesRequest) { r.Bucket = chargeBucketEnd },
	} {
		r := ok
		change(&r)
		if err := r.Validate(); !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("%s: Validate() = %v, want ErrInvalidRequest", name, err)
		}
	}
}
