package limes

import (
	"errors"
	"testing"
	"uuid"
)

// Each request a store takes refuses what names nothing it could act on, and
// accepts the rest.
func TestRequestsValidate(t *testing.T) {
	id := uuid.New()
	for name, tc := range map[string]struct {
		req interface{ Validate() error }
		ok  bool
	}{
		"revoke":                  {RevokeRequest{CapabilityID: id}, true},
		"revoke of no capability": {RevokeRequest{}, false},
	} {
		err := tc.req.Validate()
		if tc.ok && err != nil {
			t.Errorf("%s: %v, want accepted", name, err)
		}
		if !tc.ok && !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("%s: err = %v, want ErrInvalidRequest", name, err)
		}
	}
}
