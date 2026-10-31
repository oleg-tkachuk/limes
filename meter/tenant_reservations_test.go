package meter

import (
	"errors"
	"testing"
	"uuid"

	"github.com/oleg-tkachuk/limes"
)

func TestListTenantReservationsRequestValidate(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  ListTenantReservationsRequest
		ok   bool
	}{
		{"a tenant", ListTenantReservationsRequest{TenantID: uuid.New()}, true},
		{"a tenant and a page", ListTenantReservationsRequest{TenantID: uuid.New(), Limit: 1, Cursor: "c"}, true},
		{"no tenant", ListTenantReservationsRequest{}, false},
		{"a negative limit", ListTenantReservationsRequest{TenantID: uuid.New(), Limit: -1}, false},
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
