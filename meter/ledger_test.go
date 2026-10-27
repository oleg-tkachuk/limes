package meter

import (
	"errors"
	"slices"
	"testing"
	"time"
	"uuid"

	"github.com/oleg-tkachuk/limes"
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
		ChargeBucketWeek:  {"week", time.Date(2026, time.February, 23, 0, 0, 0, 0, time.UTC)},
		ChargeBucketMonth: {"month", time.Date(2026, time.February, 1, 0, 0, 0, 0, time.UTC)},
	}
	if ChargeBucket(0) != ChargeBucketNone {
		t.Errorf("the zero bucket is %s, want %s", ChargeBucket(0), ChargeBucketNone)
	}
	// Shortest first: a bucket's number orders it by its length.
	shortestFirst := []ChargeBucket{ChargeBucketNone, ChargeBucketHour, ChargeBucketDay, ChargeBucketWeek, ChargeBucketMonth}
	if !slices.IsSorted(shortestFirst) {
		t.Errorf("the buckets are numbered %v, want shortest first", shortestFirst)
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
	if !errors.Is(b.Validate(), limes.ErrInvalidRequest) {
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
		"no tenant":                 func(r *SumChargesRequest) { r.TenantID = uuid.Nil() },
		"no From":                   func(r *SumChargesRequest) { r.From = time.Time{} },
		"no To":                     func(r *SumChargesRequest) { r.To = time.Time{} },
		"an empty period":           func(r *SumChargesRequest) { r.To = r.From },
		"a negative limit":          func(r *SumChargesRequest) { r.Limit = -1 },
		"an unknown bucket":         func(r *SumChargesRequest) { r.Bucket = chargeBucketEnd },
		"a tenant and every tenant": func(r *SumChargesRequest) { r.AllTenants = true },
		"a tenant filter beside a tenant": func(r *SumChargesRequest) {
			r.Filter.Tenants = []uuid.UUID{r.TenantID}
		},
		"no dimension":         func(r *SumChargesRequest) { r.GroupBy = []ChargeDimension{ChargeDimensionUnspecified} },
		"an unknown dimension": func(r *SumChargesRequest) { r.GroupBy = []ChargeDimension{chargeDimensionEnd} },
		"a dimension named twice": func(r *SumChargesRequest) {
			r.GroupBy = []ChargeDimension{ChargeDimensionOp, ChargeDimensionActor, ChargeDimensionOp}
		},
		"a nil tenant in a filter": func(r *SumChargesRequest) {
			r.TenantID, r.AllTenants, r.Filter.Tenants = uuid.Nil(), true, []uuid.UUID{uuid.Nil()}
		},
		"a nil capability in a filter":   func(r *SumChargesRequest) { r.Filter.Capabilities = []uuid.UUID{uuid.Nil()} },
		"an empty operation in a filter": func(r *SumChargesRequest) { r.Filter.Ops = []limes.Op{""} },
		"an empty actor in a filter":     func(r *SumChargesRequest) { r.Filter.Actors = []string{""} },
		"a filter past its bound": func(r *SumChargesRequest) {
			r.Filter.Ops = slices.Repeat([]limes.Op{limes.OpGet}, MaxChargeFilterValues+1)
		},
	} {
		r := ok
		change(&r)
		if err := r.Validate(); !errors.Is(err, limes.ErrInvalidRequest) {
			t.Errorf("%s: Validate() = %v, want ErrInvalidRequest", name, err)
		}
	}
}

// A request across tenants, with every dimension and a filter at its bound,
// is whole.
func TestSumChargesRequestAcrossTenants(t *testing.T) {
	from := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	r := SumChargesRequest{
		AllTenants: true, From: from, To: from.Add(time.Hour),
		GroupBy: []ChargeDimension{ChargeDimensionCapability, ChargeDimensionTenant, ChargeDimensionActor, ChargeDimensionOp},
		Filter:  ChargeFilter{Tenants: slices.Repeat([]uuid.UUID{uuid.New()}, MaxChargeFilterValues)},
	}
	if err := r.Validate(); err != nil {
		t.Fatalf("a request across tenants: %v", err)
	}
	if !r.Groups(ChargeDimensionTenant) || (SumChargesRequest{}).Groups(ChargeDimensionTenant) {
		t.Error("Groups does not report the dimensions GroupBy names")
	}
}

// Every dimension this module defines has a name of its own and is
// accepted; the zero value and one past the last are refused.
func TestChargeDimension(t *testing.T) {
	want := map[ChargeDimension]string{
		ChargeDimensionTenant: "tenant", ChargeDimensionOp: "op",
		ChargeDimensionActor: "actor", ChargeDimensionCapability: "capability",
	}
	if len(want) != int(chargeDimensionEnd)-1 {
		t.Fatalf("%d dimensions named here, %d defined", len(want), int(chargeDimensionEnd)-1)
	}
	for d, name := range want {
		if d.String() != name || d.Validate() != nil {
			t.Errorf("%d: String() = %q, Validate() = %v; want %q and accepted", d, d.String(), d.Validate(), name)
		}
	}
	for _, d := range []ChargeDimension{ChargeDimensionUnspecified, chargeDimensionEnd} {
		if err := d.Validate(); !errors.Is(err, limes.ErrInvalidRequest) {
			t.Errorf("%s: Validate() = %v, want ErrInvalidRequest", d, err)
		}
	}
}

// A filter keeps a charge only when every list it sets names the charge's
// value; an empty list keeps every value of its field.
func TestChargeFilterAdmits(t *testing.T) {
	tenant, capID := uuid.New(), uuid.New()
	for name, tc := range map[string]struct {
		f    ChargeFilter
		want bool
	}{
		"no filter":               {ChargeFilter{}, true},
		"its tenant":              {ChargeFilter{Tenants: []uuid.UUID{uuid.New(), tenant}}, true},
		"another tenant":          {ChargeFilter{Tenants: []uuid.UUID{uuid.New()}}, false},
		"its operation":           {ChargeFilter{Ops: []limes.Op{limes.OpGet}}, true},
		"another operation":       {ChargeFilter{Ops: []limes.Op{limes.OpPut}}, false},
		"its actor":               {ChargeFilter{Actors: []string{"a"}}, true},
		"another actor":           {ChargeFilter{Actors: []string{"b"}}, false},
		"its capability":          {ChargeFilter{Capabilities: []uuid.UUID{capID}}, true},
		"another capability":      {ChargeFilter{Capabilities: []uuid.UUID{uuid.New()}}, false},
		"one list keeps, one not": {ChargeFilter{Ops: []limes.Op{limes.OpGet}, Actors: []string{"b"}}, false},
	} {
		if got := tc.f.Admits(tenant, limes.OpGet, "a", capID); got != tc.want {
			t.Errorf("%s: Admits = %v, want %v", name, got, tc.want)
		}
	}
}
