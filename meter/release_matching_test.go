package meter

import (
	"errors"
	"testing"
	"uuid"

	"github.com/oleg-tkachuk/limes"
)

func TestReleaseMatchingRequestValidate(t *testing.T) {
	tenant := uuid.New()
	for _, tc := range []struct {
		name string
		req  ReleaseMatchingRequest
		ok   bool
	}{
		{"a tenant by client", ReleaseMatchingRequest{TenantID: tenant, ClientID: "client"}, true},
		{"a tenant by every selector", ReleaseMatchingRequest{TenantID: tenant, Subject: "s", OnBehalfOf: "u", ClientID: "c", Session: "sid", RunID: uuid.New()}, true},
		{"a tenant by agent run", ReleaseMatchingRequest{TenantID: tenant, RunID: uuid.New()}, true},
		{"no tenant", ReleaseMatchingRequest{ClientID: "client"}, false},
		{"no selector", ReleaseMatchingRequest{TenantID: tenant}, false},
		{"a negative limit", ReleaseMatchingRequest{TenantID: tenant, ClientID: "client", Limit: -1}, false},
		{"every tenant by client", ReleaseMatchingRequest{AllTenants: true, ClientID: "client"}, true},
		{"every tenant by session", ReleaseMatchingRequest{AllTenants: true, Session: "sid"}, true},
		{"every tenant by client and session", ReleaseMatchingRequest{AllTenants: true, ClientID: "client", Session: "sid"}, true},
		{"a tenant beside every tenant", ReleaseMatchingRequest{TenantID: tenant, AllTenants: true, ClientID: "client"}, false},
		{"every tenant with no selector", ReleaseMatchingRequest{AllTenants: true}, false},
		{"every tenant by subject", ReleaseMatchingRequest{AllTenants: true, ClientID: "client", Subject: "s"}, false},
		{"every tenant by on-behalf-of", ReleaseMatchingRequest{AllTenants: true, OnBehalfOf: "u"}, false},
		{"every tenant by agent run", ReleaseMatchingRequest{AllTenants: true, Session: "sid", RunID: uuid.New()}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.req.Validate()
			if tc.ok && err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
			if !tc.ok && !errors.Is(err, limes.ErrInvalidRequest) {
				t.Fatalf("Validate() = %v, want ErrInvalidRequest", err)
			}
		})
	}
}
