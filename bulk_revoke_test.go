package limes

import (
	"errors"
	"testing"
	"uuid"
)

func TestRevokeMatchingRequestValidate(t *testing.T) {
	tenant := uuid.New()
	for _, tc := range []struct {
		name string
		req  RevokeMatchingRequest
		ok   bool
	}{
		{"a tenant and a selector", RevokeMatchingRequest{TenantID: tenant, Subject: "user"}, true},
		{"every tenant by client", RevokeMatchingRequest{AllTenants: true, ClientID: "client"}, true},
		{"every tenant by session", RevokeMatchingRequest{AllTenants: true, Session: "sid"}, true},
		{"every tenant by client and session", RevokeMatchingRequest{AllTenants: true, ClientID: "client", Session: "sid"}, true},
		{"neither a tenant nor every tenant", RevokeMatchingRequest{ClientID: "client"}, false},
		{"a tenant beside every tenant", RevokeMatchingRequest{TenantID: tenant, AllTenants: true, ClientID: "client"}, false},
		{"no selector", RevokeMatchingRequest{TenantID: tenant}, false},
		{"every tenant with no selector", RevokeMatchingRequest{AllTenants: true}, false},
		{"every tenant by subject", RevokeMatchingRequest{AllTenants: true, ClientID: "client", Subject: "user"}, false},
		{"every tenant by on-behalf-of", RevokeMatchingRequest{AllTenants: true, OnBehalfOf: "user"}, false},
		{"a tenant by agent run", RevokeMatchingRequest{TenantID: tenant, RunID: uuid.New()}, true},
		{"every tenant by agent run", RevokeMatchingRequest{AllTenants: true, ClientID: "client", RunID: uuid.New()}, false},
		{"a negative limit", RevokeMatchingRequest{TenantID: tenant, Subject: "user", Limit: -1}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.req.Validate()
			if tc.ok && err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
			if !tc.ok && !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("Validate() = %v, want ErrInvalidRequest", err)
			}
		})
	}
}
