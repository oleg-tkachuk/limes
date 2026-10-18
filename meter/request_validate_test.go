package meter

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes"
)

// Each request a store takes refuses what names nothing it could act on, and
// accepts the rest.
func TestRequestsValidate(t *testing.T) {
	id := uuid.New()
	for name, tc := range map[string]struct {
		req interface{ Validate() error }
		ok  bool
	}{
		"delete usage":                  {DeleteUsageRequest{CapabilityID: id}, true},
		"delete usage of no capability": {DeleteUsageRequest{}, false},
		"charge by ref":                 {ChargeByRefRequest{CapabilityID: id, ExternalRef: "call-1"}, true},
		"charge by ref, no capability":  {ChargeByRefRequest{ExternalRef: "call-1"}, false},
		"charge by ref, no ref":         {ChargeByRefRequest{CapabilityID: id}, false},
		"charge by ref, a ref too long": {ChargeByRefRequest{CapabilityID: id, ExternalRef: strings.Repeat("r", MaxExternalRefBytes+1)}, false},
		"purge orphans":                 {PurgeOrphansRequest{}, true},
		"purge orphans, negative":       {PurgeOrphansRequest{Limit: -1}, false},
		"release expired":               {ReleaseExpiredRequest{Limit: 1}, true},
		"release expired, negative":     {ReleaseExpiredRequest{Limit: -1}, false},
	} {
		err := tc.req.Validate()
		if tc.ok && err != nil {
			t.Errorf("%s: %v, want accepted", name, err)
		}
		if !tc.ok && !errors.Is(err, limes.ErrInvalidRequest) {
			t.Errorf("%s: err = %v, want ErrInvalidRequest", name, err)
		}
	}
}

// A housekeeping Limit of zero takes the default; any other is itself.
func TestHousekeepingLimit(t *testing.T) {
	const asked = 7
	if got := HousekeepingLimit(0); got != limes.DefaultHousekeepingLimit {
		t.Errorf("HousekeepingLimit(0) = %d, want %d", got, limes.DefaultHousekeepingLimit)
	}
	if got := HousekeepingLimit(asked); got != asked {
		t.Errorf("HousekeepingLimit(%d) = %d", asked, got)
	}
}
