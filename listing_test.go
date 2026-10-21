package limes

import (
	"errors"
	"testing"

	"github.com/google/uuid"
)

// A listing names its tenant, at most one of a subject and a subject
// prefix, and only the principal types, dispositions and orders this
// package defines; it refuses a negative page size.
func TestListRecordsRequestValidate(t *testing.T) {
	tenant := uuid.New()
	for name, tc := range map[string]struct {
		req   ListRecordsRequest
		valid bool
	}{
		"a tenant": {ListRecordsRequest{TenantID: tenant}, true},
		"every filter": {ListRecordsRequest{
			TenantID: tenant, PrincipalType: PrincipalTypeAgent, SubjectPrefix: "agent:",
			Dispositions: []RecordDisposition{RecordDispositionActive, RecordDispositionExpired, RecordDispositionRevoked},
			Order:        ListOrderDescending, Cursor: "c", Limit: MaxListLimit + 1,
		}, true},
		"a subject":                 {ListRecordsRequest{TenantID: tenant, Subject: "agent:a"}, true},
		"no tenant":                 {ListRecordsRequest{Subject: "agent:a"}, false},
		"an unknown principal type": {ListRecordsRequest{TenantID: tenant, PrincipalType: "robot"}, false},
		"a subject and a prefix":    {ListRecordsRequest{TenantID: tenant, Subject: "agent:a", SubjectPrefix: "agent:"}, false},
		"an unknown disposition":    {ListRecordsRequest{TenantID: tenant, Dispositions: []RecordDisposition{RecordDispositionActive, "lost"}}, false},
		"an unspecified one":        {ListRecordsRequest{TenantID: tenant, Dispositions: []RecordDisposition{RecordDispositionUnspecified}}, false},
		"an unknown order":          {ListRecordsRequest{TenantID: tenant, Order: ListOrderDescending + 1}, false},
		"a negative limit":          {ListRecordsRequest{TenantID: tenant, Limit: -1}, false},
	} {
		err := tc.req.Validate()
		if tc.valid && err != nil {
			t.Errorf("%s: Validate = %v, want nil", name, err)
		}
		if !tc.valid && !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("%s: Validate = %v, want ErrInvalidRequest", name, err)
		}
	}
}

// A request naming no disposition admits every one; one naming some admits
// those.
func TestListRecordsRequestAdmits(t *testing.T) {
	every := ListRecordsRequest{}
	some := ListRecordsRequest{Dispositions: []RecordDisposition{RecordDispositionExpired, RecordDispositionRevoked}}
	for d, want := range map[RecordDisposition]bool{
		RecordDispositionActive: false, RecordDispositionExpired: true, RecordDispositionRevoked: true,
	} {
		if !every.Admits(d) {
			t.Errorf("a request of no disposition refuses %q", d)
		}
		if got := some.Admits(d); got != want {
			t.Errorf("a request of expired and revoked: Admits(%q) = %v, want %v", d, got, want)
		}
	}
}

func TestListOrderString(t *testing.T) {
	for o, want := range map[ListOrder]string{
		ListOrderAscending: "ascending", ListOrderDescending: "descending", ListOrderDescending + 1: "ListOrder(2)",
	} {
		if got := o.String(); got != want {
			t.Errorf("%d.String() = %q, want %q", uint8(o), got, want)
		}
	}
}
