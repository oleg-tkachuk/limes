package storetest

import (
	"bytes"
	"errors"
	"slices"
	"strings"
	"testing"
	"uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/internal/expect"
)

// theirSubject is the one record censusFixture writes for the other tenant,
// active.
const theirSubject = "agent:theirs"

// listedIDs is the ids of a listing, in its order.
func listedIDs(l []limes.RecordListing) []uuid.UUID {
	out := make([]uuid.UUID, len(l))
	for i, r := range l {
		out[i] = r.Record.Capability.ID
	}
	return out
}

// A listing across every tenant gives every tenant's records in one id
// order, both ways and a record at a time alike, each naming its tenant and
// with the disposition it has in its own tenant's listing; each filter
// holds across tenants as within one.
func checkListRecordsAllTenants(t *testing.T, f fixture) {
	f.skipUnlessLister(t)
	other := censusFixture(t, f)
	ctx := f.allTenantsCtx()
	all := limes.ListRecordsRequest{AllTenants: true}
	up := f.listRecordsAllIn(t, ctx, all)
	expect.Must(t, len(up) == len(censusDispositions)+1, "listing of every tenant = %v, want Tenant's %d records and the other tenant's", listedSubjects(up), len(censusDispositions))
	ids := listedIDs(up)
	expect.That(t, slices.IsSortedFunc(ids, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) }), "listing of every tenant is not in ascending id order: %v", listedSubjects(up))
	for _, l := range up {
		c := l.Record.Capability
		want, tenant := censusDispositions[c.Subject.Subject], f.Tenant
		if c.Subject.Subject == theirSubject {
			want, tenant = limes.RecordDispositionActive, other
		}
		expect.That(t, c.Subject.TenantID == tenant, "%s: tenant %s, want %s", c.Subject.Subject, c.Subject.TenantID, tenant)
		expect.That(t, l.Disposition == want, "%s: disposition %q across tenants, want %q", c.Subject.Subject, l.Disposition, want)
	}
	down := all
	down.Order = limes.ListOrderDescending
	got := listedIDs(f.listRecordsAllIn(t, ctx, down))
	slices.Reverse(got)
	expect.That(t, slices.Equal(got, ids), "listing of every tenant newest first is not the oldest first reversed")
	for _, order := range []limes.ListOrder{limes.ListOrderAscending, limes.ListOrderDescending} {
		paged := all
		paged.Order, paged.Limit = order, 1
		got := listedIDs(f.listRecordsAllIn(t, ctx, paged))
		if order == limes.ListOrderDescending {
			slices.Reverse(got)
		}
		expect.That(t, slices.Equal(got, ids), "listing of every tenant a record at a time, %s, is not the listing whole", order)
	}
	agents := f.listRecordsAllIn(t, ctx, limes.ListRecordsRequest{AllTenants: true, PrincipalType: limes.PrincipalTypeAgent, Dispositions: []limes.RecordDisposition{limes.RecordDispositionActive}})
	gotSubjects := listedSubjects(agents)
	slices.Sort(gotSubjects)
	wantSubjects := append(subjectsWhere(func(s string, d limes.RecordDisposition) bool {
		return d == limes.RecordDispositionActive && strings.HasPrefix(s, "agent:")
	}), theirSubject)
	slices.Sort(wantSubjects)
	expect.That(t, slices.Equal(gotSubjects, wantSubjects), "active agents of every tenant = %v, want %v", gotSubjects, wantSubjects)
	theirs := f.listRecordsAllIn(t, ctx, limes.ListRecordsRequest{AllTenants: true, Subject: theirSubject})
	expect.That(t, len(theirs) == 1 && theirs[0].Record.Capability.Subject.TenantID == other, "listing of every tenant by subject %s = %v, want the other tenant's record", theirSubject, listedSubjects(theirs))
}

// A cursor of a tenant's listing is refused by a listing of every tenant,
// and one of every tenant by a tenant's; a request naming both a tenant and
// every tenant, or neither, is malformed; and under one tenant's scope, a
// store that keeps tenants apart refuses a listing of every tenant rather
// than list that tenant's alone.
func checkListRecordsAllTenantsRefusals(t *testing.T, f fixture) {
	f.skipUnlessLister(t)
	censusFixture(t, f)
	ctx := f.allTenantsCtx()
	mine, err := f.Lister.ListRecords(f.Ctx, limes.ListRecordsRequest{TenantID: f.Tenant, Limit: 1})
	expect.Must(t, err == nil && mine.Next != "", "first page of Tenant = %+v, %v; want a next page", mine, err)
	every, err := f.Lister.ListRecords(ctx, limes.ListRecordsRequest{AllTenants: true, Limit: 1})
	expect.Must(t, err == nil && every.Next != "", "first page of every tenant = %+v, %v; want a next page", every, err)
	_, err = f.Lister.ListRecords(ctx, limes.ListRecordsRequest{AllTenants: true, Cursor: mine.Next, Limit: 1})
	expect.That(t, errors.Is(err, limes.ErrCursorMismatch), "a tenant's cursor in a listing of every tenant: err = %v, want ErrCursorMismatch", err)
	_, err = f.Lister.ListRecords(f.Ctx, limes.ListRecordsRequest{TenantID: f.Tenant, Cursor: every.Next, Limit: 1})
	expect.That(t, errors.Is(err, limes.ErrCursorMismatch), "a cursor of every tenant in a tenant's listing: err = %v, want ErrCursorMismatch", err)
	for name, bad := range map[string]limes.ListRecordsRequest{
		"a tenant and every tenant": {TenantID: f.Tenant, AllTenants: true},
		"neither":                   {},
	} {
		_, err := f.Lister.ListRecords(ctx, bad)
		expect.That(t, errors.Is(err, limes.ErrInvalidRequest) && !errors.Is(err, limes.ErrCursorMismatch), "ListRecords with %s: err = %v, want ErrInvalidRequest alone", name, err)
	}
	if !f.ScopesTenants {
		return
	}
	_, err = f.Lister.ListRecords(f.Ctx, limes.ListRecordsRequest{AllTenants: true})
	expect.That(t, errors.Is(err, limes.ErrScopeMismatch), "ListRecords of every tenant under one tenant's context: err = %v, want ErrScopeMismatch", err)
}
