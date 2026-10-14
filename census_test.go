package limes

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

// A census names one tenant or every tenant, never both nor neither, and
// refuses a negative window or page size.
func TestCountRecordsRequestValidate(t *testing.T) {
	tenant := uuid.New()
	for name, tc := range map[string]struct {
		req   CountRecordsRequest
		valid bool
	}{
		"one tenant":        {CountRecordsRequest{TenantID: tenant}, true},
		"every tenant":      {CountRecordsRequest{AllTenants: true, ExpiringWithin: time.Hour, Limit: 1}, true},
		"neither":           {CountRecordsRequest{}, false},
		"both":              {CountRecordsRequest{TenantID: tenant, AllTenants: true}, false},
		"a negative window": {CountRecordsRequest{TenantID: tenant, ExpiringWithin: -time.Nanosecond}, false},
		"a negative limit":  {CountRecordsRequest{TenantID: tenant, Limit: -1}, false},
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
