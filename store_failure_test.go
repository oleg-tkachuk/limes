package limes

import (
	"errors"
	"fmt"
	"net/http"
	"testing"
)

// A store's own failure answers with its code, 503 or 504, in category
// unavailable; one under a revocation lookup answers as the lookup that
// failed, revocation_unavailable, the store's code kept in the chain; and
// a code of limes's a lookup returns otherwise is kept as it is.
func TestStoreFailureCodes(t *testing.T) {
	driver := errors.New("the driver's error")
	for name, tc := range map[string]struct {
		err    error
		code   Code
		status int
	}{
		"unavailable": {fmt.Errorf("%w: %w", ErrStoreUnavailable, driver), CodeStoreUnavailable, http.StatusServiceUnavailable},
		"timeout":     {fmt.Errorf("%w: %w", ErrStoreTimeout, driver), CodeStoreTimeout, http.StatusGatewayTimeout},
	} {
		p := ProblemFor(tc.err)
		if p.Code != tc.code || p.Status != tc.status || p.Category != CategoryUnavailable || !errors.Is(tc.err, driver) {
			t.Errorf("%s: %s %d %s, want %s %d unavailable", name, p.Code, p.Status, p.Category, tc.code, tc.status)
		}
		lookup := revocationLookupFailed("revocation lookup", tc.err)
		if code, _ := CodeOf(lookup); code != CodeRevocationUnavailable || !errors.Is(lookup, tc.err) {
			t.Errorf("%s under a lookup: %s, want revocation_unavailable over it", name, code)
		}
	}
	if code, _ := CodeOf(revocationLookupFailed("revocation lookup", ErrDelegationTooDeep)); code != CodeDelegationTooDeep {
		t.Errorf("a lineage too deep under a lookup: %s, want its own code kept", code)
	}
	if code, _ := CodeOf(revocationLookupFailed("revocation lookup", driver)); code != CodeRevocationUnavailable {
		t.Errorf("a store's error with no code under a lookup: %s, want revocation_unavailable", code)
	}
}
