package memstore

import (
	"testing"
	"uuid"

	"github.com/oleg-tkachuk/limes"
)

// pinnedTenantKey is filtersKey of pinnedTenantRequest as it was before
// AllTenants existed: a tenant's cursor handed out then still pages.
const pinnedTenantKey = "ba1iHdRcjfPP7d1JZcGm5A"

// pinnedTenantRequest is a tenant's listing with every filter set.
var pinnedTenantRequest = limes.ListRecordsRequest{
	TenantID: uuid.MustParse("0195f3a0-0000-7000-8000-000000000001"), PrincipalType: limes.PrincipalTypeAgent,
	SubjectPrefix: "agent:", Dispositions: []limes.RecordDisposition{limes.RecordDispositionRevoked, limes.RecordDispositionActive},
	ClientID: "client", Session: "sid", OnBehalfOf: "user", RunID: uuid.MustParse("0195f3a0-0000-7000-8000-000000000002"),
}

func TestFiltersKeyOfATenantIsUnchanged(t *testing.T) {
	if got := filtersKey(pinnedTenantRequest); got != pinnedTenantKey {
		t.Errorf("filtersKey of a tenant's listing = %q, want %q: a cursor handed out before would be refused", got, pinnedTenantKey)
	}
	every := pinnedTenantRequest
	every.TenantID, every.AllTenants = uuid.Nil(), true
	if filtersKey(every) == filtersKey(limes.ListRecordsRequest{PrincipalType: every.PrincipalType, SubjectPrefix: every.SubjectPrefix, Dispositions: every.Dispositions, ClientID: every.ClientID, Session: every.Session, OnBehalfOf: every.OnBehalfOf, RunID: every.RunID}) {
		t.Error("filtersKey does not tell a listing of every tenant from one of no tenant")
	}
}
