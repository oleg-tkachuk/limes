package storetest

import (
	"bytes"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes"
)

// censusDispositions is the disposition of each of censusFixture's records
// for Tenant, by subject, at f.now: revoked through an ancestor
// (user:child), and revoked over expired (agent:revoked-expired), among
// them.
var censusDispositions = map[string]limes.RecordDisposition{
	"agent:root":            limes.RecordDispositionActive,
	"agent:soon":            limes.RecordDispositionActive,
	"agent:edge":            limes.RecordDispositionActive,
	"agent:revoked-expired": limes.RecordDispositionRevoked,
	"svc:old":               limes.RecordDispositionExpired,
	"svc:now":               limes.RecordDispositionExpired,
	"user:root":             limes.RecordDispositionRevoked,
	"user:child":            limes.RecordDispositionRevoked,
}

// listAll is every record of the listing req asks for, a page at a time.
func (f fixture) listRecordsAll(t *testing.T, req limes.ListRecordsRequest) []limes.RecordListing {
	t.Helper()
	var out []limes.RecordListing
	fetched := map[string]bool{}
	for {
		fetched[req.Cursor] = true
		page, err := f.Lister.ListRecords(f.Ctx, req)
		if err != nil {
			t.Fatalf("ListRecords(%+v): %v", req, err)
		}
		out = append(out, page.Items...)
		if page.Next == "" {
			return out
		}
		if fetched[page.Next] {
			t.Fatalf("ListRecords handed back cursor %q again", page.Next)
		}
		req.Cursor = page.Next
	}
}

// listedSubjects is the subjects of a listing, in its order.
func listedSubjects(l []limes.RecordListing) []string {
	out := make([]string, len(l))
	for i, r := range l {
		out[i] = r.Record.Capability.Subject.Subject
	}
	return out
}

// subjectsWhere is censusDispositions' subjects that keep holds for,
// sorted.
func subjectsWhere(keep func(subject string, d limes.RecordDisposition) bool) []string {
	var out []string
	for s, d := range censusDispositions {
		if keep(s, d) {
			out = append(out, s)
		}
	}
	slices.Sort(out)
	return out
}

func (f fixture) skipUnlessLister(t *testing.T) {
	t.Helper()
	if f.Lister == nil || f.Now == nil {
		t.Skip("Env.Lister or Env.Now not set")
	}
}

// A listing gives each record what GetRecord reads back and the disposition
// the census counts it under — revoked through an ancestor on record, and
// over expired — so each disposition numbers what CountRecords counts; it
// lists the tenant's records only.
func checkListRecords(t *testing.T, f fixture) {
	f.skipUnlessLister(t)
	if got := f.listRecordsAll(t, limes.ListRecordsRequest{TenantID: f.Tenant}); len(got) != 0 {
		t.Errorf("listing of a tenant with no record = %+v, want an empty page", got)
	}
	censusFixture(t, f)
	listed := f.listRecordsAll(t, limes.ListRecordsRequest{TenantID: f.Tenant})
	if len(listed) != len(censusDispositions) {
		t.Fatalf("listing of Tenant = %v, want the %d records of Tenant alone", listedSubjects(listed), len(censusDispositions))
	}
	byDisposition := map[limes.RecordDisposition]int64{}
	for _, l := range listed {
		c := l.Record.Capability
		if c.Subject.TenantID != f.Tenant {
			t.Errorf("listing of Tenant lists %s of tenant %s", c.ID, c.Subject.TenantID)
		}
		if want := censusDispositions[c.Subject.Subject]; l.Disposition != want {
			t.Errorf("%s: disposition %q, want %q", c.Subject.Subject, l.Disposition, want)
		}
		byDisposition[l.Disposition]++
		r, err := f.Store.GetRecord(f.Ctx, c.ID)
		if err != nil {
			t.Fatalf("GetRecord %s: %v", c.ID, err)
		}
		if diff := diffCapabilities(l.Record.Capability, r.Capability); diff != "" {
			t.Errorf("%s: listed a capability other than GetRecord's: %s", c.Subject.Subject, diff)
		}
		if !reflect.DeepEqual(l.Record.IssuedBy, r.IssuedBy) || !reflect.DeepEqual(l.Record.Revocation, r.Revocation) {
			t.Errorf("%s: listed issued by %+v, revocation %+v; GetRecord reads %+v, %+v",
				c.Subject.Subject, l.Record.IssuedBy, l.Record.Revocation, r.IssuedBy, r.Revocation)
		}
	}
	if f.Census == nil {
		return
	}
	counts := f.countAll(t, f.Ctx, limes.CountRecordsRequest{TenantID: f.Tenant})
	if len(counts) != 1 {
		t.Fatalf("census of Tenant = %+v, want one row", counts)
	}
	want := map[limes.RecordDisposition]int64{
		limes.RecordDispositionActive:  counts[0].Active,
		limes.RecordDispositionExpired: counts[0].Expired,
		limes.RecordDispositionRevoked: counts[0].Revoked,
	}
	if !reflect.DeepEqual(byDisposition, want) {
		t.Errorf("listed by disposition %v, the census counts %v", byDisposition, want)
	}
}

// Each filter lists the records it admits, alone and with the others.
func checkListRecordsFilters(t *testing.T, f fixture) {
	f.skipUnlessLister(t)
	censusFixture(t, f)
	of := func(kind string) func(string, limes.RecordDisposition) bool {
		return func(s string, _ limes.RecordDisposition) bool { return strings.HasPrefix(s, kind) }
	}
	in := func(ds ...limes.RecordDisposition) func(string, limes.RecordDisposition) bool {
		return func(_ string, d limes.RecordDisposition) bool { return slices.Contains(ds, d) }
	}
	both := func(a, b func(string, limes.RecordDisposition) bool) func(string, limes.RecordDisposition) bool {
		return func(s string, d limes.RecordDisposition) bool { return a(s, d) && b(s, d) }
	}
	active, expired, revoked := limes.RecordDispositionActive, limes.RecordDispositionExpired, limes.RecordDispositionRevoked
	for name, c := range map[string]struct {
		req  limes.ListRecordsRequest
		want func(string, limes.RecordDisposition) bool
	}{
		"every record":      {limes.ListRecordsRequest{}, func(string, limes.RecordDisposition) bool { return true }},
		"a principal type":  {limes.ListRecordsRequest{PrincipalType: limes.PrincipalTypeService}, of("svc:")},
		"a subject":         {limes.ListRecordsRequest{Subject: "user:child"}, func(s string, _ limes.RecordDisposition) bool { return s == "user:child" }},
		"a subject prefix":  {limes.ListRecordsRequest{SubjectPrefix: "agent:r"}, of("agent:r")},
		"a prefix of none":  {limes.ListRecordsRequest{SubjectPrefix: "agent:%"}, func(string, limes.RecordDisposition) bool { return false }},
		"active":            {limes.ListRecordsRequest{Dispositions: []limes.RecordDisposition{active}}, in(active)},
		"expired":           {limes.ListRecordsRequest{Dispositions: []limes.RecordDisposition{expired}}, in(expired)},
		"revoked":           {limes.ListRecordsRequest{Dispositions: []limes.RecordDisposition{revoked}}, in(revoked)},
		"expired, revoked":  {limes.ListRecordsRequest{Dispositions: []limes.RecordDisposition{expired, revoked}}, in(expired, revoked)},
		"type, disposition": {limes.ListRecordsRequest{PrincipalType: limes.PrincipalTypeAgent, Dispositions: []limes.RecordDisposition{revoked}}, both(of("agent:"), in(revoked))},
		"type and subject": {
			limes.ListRecordsRequest{PrincipalType: limes.PrincipalTypeUser, Subject: "user:root"},
			func(s string, _ limes.RecordDisposition) bool { return s == "user:root" },
		},
		"another type's subject": {
			limes.ListRecordsRequest{PrincipalType: limes.PrincipalTypeAgent, Subject: "user:root"},
			func(string, limes.RecordDisposition) bool { return false },
		},
		"prefix, disposition": {
			limes.ListRecordsRequest{SubjectPrefix: "agent:", Dispositions: []limes.RecordDisposition{active}, Order: limes.ListOrderDescending},
			both(of("agent:"), in(active)),
		},
		"every filter": {
			limes.ListRecordsRequest{PrincipalType: limes.PrincipalTypeUser, SubjectPrefix: "user:", Dispositions: []limes.RecordDisposition{revoked}, Limit: 1},
			both(of("user:"), in(revoked)),
		},
	} {
		c.req.TenantID = f.Tenant
		got := listedSubjects(f.listRecordsAll(t, c.req))
		slices.Sort(got)
		if want := subjectsWhere(c.want); !slices.Equal(got, want) {
			t.Errorf("%s: listed %v, want %v", name, got, want)
		}
	}
}

// A listing pages in ascending id or descending, one the reverse of the
// other, and hands the same records a page at a time as in one, none
// skipped or repeated.
func checkListRecordsPages(t *testing.T, f fixture) {
	f.skipUnlessLister(t)
	censusFixture(t, f)
	ids := func(l []limes.RecordListing) []uuid.UUID {
		out := make([]uuid.UUID, len(l))
		for i, r := range l {
			out[i] = r.Record.Capability.ID
		}
		return out
	}
	up := ids(f.listRecordsAll(t, limes.ListRecordsRequest{TenantID: f.Tenant}))
	if !slices.IsSortedFunc(up, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) }) {
		t.Errorf("ascending listing is out of id order: %v", up)
	}
	down := ids(f.listRecordsAll(t, limes.ListRecordsRequest{TenantID: f.Tenant, Order: limes.ListOrderDescending}))
	reversed := slices.Clone(up)
	slices.Reverse(reversed)
	if !slices.Equal(down, reversed) {
		t.Errorf("descending listing = %v, want the ascending one reversed, %v", down, reversed)
	}
	for _, order := range []limes.ListOrder{limes.ListOrderAscending, limes.ListOrderDescending} {
		whole := up
		if order == limes.ListOrderDescending {
			whole = down
		}
		for _, limit := range []int{1, 2, 3, len(censusDispositions)} {
			req := limes.ListRecordsRequest{TenantID: f.Tenant, Order: order, Limit: limit}
			if paged := ids(f.listRecordsAll(t, req)); !slices.Equal(paged, whole) {
				t.Errorf("%s listing %d at a time = %v, want %v", order, limit, paged, whole)
			}
			page, err := f.Lister.ListRecords(f.Ctx, req)
			if err != nil {
				t.Fatalf("ListRecords: %v", err)
			}
			if wantNext := limit < len(whole); len(page.Items) != min(limit, len(whole)) || (page.Next != "") != wantNext {
				t.Errorf("%s first page of %d: %d records, next %q; want %d and a next page %v",
					order, limit, len(page.Items), page.Next, min(limit, len(whole)), wantNext)
			}
		}
	}
	// A filter pages too: the active records a record at a time.
	req := limes.ListRecordsRequest{TenantID: f.Tenant, Dispositions: []limes.RecordDisposition{limes.RecordDispositionActive}, Order: limes.ListOrderDescending, Limit: 1}
	got := listedSubjects(f.listRecordsAll(t, req))
	slices.Sort(got)
	if want := subjectsWhere(func(_ string, d limes.RecordDisposition) bool { return d == limes.RecordDispositionActive }); !slices.Equal(got, want) {
		t.Errorf("active records a record at a time = %v, want %v", got, want)
	}
}

// A malformed request, a cursor the lister did not hand out or handed out
// for the other order, and a listing under another tenant's scope are
// refused.
func checkListRecordsMalformed(t *testing.T, f fixture) {
	f.skipUnlessLister(t)
	censusFixture(t, f)
	first, err := f.Lister.ListRecords(f.Ctx, limes.ListRecordsRequest{TenantID: f.Tenant, Limit: 1})
	if err != nil || first.Next == "" {
		t.Fatalf("first page = %+v, %v; want a next page", first, err)
	}
	for name, bad := range map[string]limes.ListRecordsRequest{
		"no tenant":                 {},
		"an unknown principal type": {TenantID: f.Tenant, PrincipalType: "robot"},
		"a subject and a prefix":    {TenantID: f.Tenant, Subject: "agent:root", SubjectPrefix: "agent:"},
		"an unknown disposition":    {TenantID: f.Tenant, Dispositions: []limes.RecordDisposition{"lost"}},
		"no disposition":            {TenantID: f.Tenant, Dispositions: []limes.RecordDisposition{limes.RecordDispositionUnspecified}},
		"an unknown order":          {TenantID: f.Tenant, Order: limes.ListOrderDescending + 1},
		"a negative limit":          {TenantID: f.Tenant, Limit: -1},
		"a foreign cursor":          {TenantID: f.Tenant, Cursor: "not-a-cursor"},
		"an id for a cursor":        {TenantID: f.Tenant, Cursor: uuid.NewString()},
		"a cursor of the other way": {TenantID: f.Tenant, Cursor: first.Next, Order: limes.ListOrderDescending},
		"a truncated cursor":        {TenantID: f.Tenant, Cursor: first.Next[:len(first.Next)/2]},
	} {
		if _, err := f.Lister.ListRecords(f.Ctx, bad); !errors.Is(err, limes.ErrInvalidRequest) {
			t.Errorf("ListRecords with %s: err = %v, want ErrInvalidRequest", name, err)
		}
	}
	if !f.ScopesTenants {
		return
	}
	other, err := f.newTenant()
	if err != nil {
		t.Fatalf("NewTenant: %v", err)
	}
	if _, err := f.Lister.ListRecords(f.tenantCtx(other), limes.ListRecordsRequest{TenantID: f.Tenant}); !errors.Is(err, limes.ErrScopeMismatch) {
		t.Errorf("ListRecords of Tenant under another tenant's context: err = %v, want ErrScopeMismatch", err)
	}
}

// A cursor holds the filters it was handed out for: handed back with
// another principal type, subject, subject prefix or set of dispositions, it
// is refused rather than paged on from its id under them, which would give a
// page of neither listing. The same dispositions in another order, or one
// repeated, are the same filters.
func checkListRecordsCursorFilters(t *testing.T, f fixture) {
	f.skipUnlessLister(t)
	censusFixture(t, f)
	active, expired, revoked := limes.RecordDispositionActive, limes.RecordDispositionExpired, limes.RecordDispositionRevoked
	req := limes.ListRecordsRequest{
		TenantID: f.Tenant, PrincipalType: limes.PrincipalTypeAgent, SubjectPrefix: "agent:",
		Dispositions: []limes.RecordDisposition{active, revoked}, Limit: 1,
	}
	first, err := f.Lister.ListRecords(f.Ctx, req)
	if err != nil || first.Next == "" {
		t.Fatalf("first page = %+v, %v; want a next page", first, err)
	}
	req.Cursor = first.Next
	for name, change := range map[string]func(*limes.ListRecordsRequest){
		"another principal type": func(r *limes.ListRecordsRequest) { r.PrincipalType = limes.PrincipalTypeUser },
		"no principal type":      func(r *limes.ListRecordsRequest) { r.PrincipalType = limes.PrincipalTypeUnspecified },
		"a subject":              func(r *limes.ListRecordsRequest) { r.SubjectPrefix, r.Subject = "", "agent:root" },
		"another subject prefix": func(r *limes.ListRecordsRequest) { r.SubjectPrefix = "agent:r" },
		"no subject prefix":      func(r *limes.ListRecordsRequest) { r.SubjectPrefix = "" },
		"another disposition":    func(r *limes.ListRecordsRequest) { r.Dispositions = []limes.RecordDisposition{active, expired} },
		"one disposition fewer":  func(r *limes.ListRecordsRequest) { r.Dispositions = []limes.RecordDisposition{active} },
		"every disposition":      func(r *limes.ListRecordsRequest) { r.Dispositions = nil },
	} {
		other := req
		change(&other)
		if _, err := f.Lister.ListRecords(f.Ctx, other); !errors.Is(err, limes.ErrInvalidRequest) {
			t.Errorf("ListRecords with the cursor and %s: err = %v, want ErrInvalidRequest", name, err)
		}
	}
	for name, dispositions := range map[string][]limes.RecordDisposition{
		"the same filters":                  {active, revoked},
		"the dispositions in another order": {revoked, active},
		"a disposition repeated":            {active, revoked, active},
	} {
		same := req
		same.Dispositions = dispositions
		if _, err := f.Lister.ListRecords(f.Ctx, same); err != nil {
			t.Errorf("ListRecords with the cursor and %s: %v", name, err)
		}
	}
}
