package pgstore

import (
	"fmt"
	"testing"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/internal/errguard"
)

// wrappedHelpers are the functions of this module that construct an error
// naming no sentinel, each because every caller wraps it in a family at the
// API's edge. A construction anywhere else names a sentinel, or fails here.
var wrappedHelpers = map[string]map[string]string{}

// Every error this module constructs names a sentinel, so it has a code
// (docs/errors.md), but for the helpers listed above.
func TestEveryErrorNamesASentinel(t *testing.T) {
	errguard.Refuse(t, ".", wrappedHelpers, "internal/pgtest")
}

// The store's own sentinels belong to limes's families, so each has a code.
func TestSentinelsHaveACode(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want limes.Code
	}{
		"ErrSchemaMismatch":    {ErrSchemaMismatch, limes.CodeInvalidConfig},
		"ErrRuntimeRole":       {ErrRuntimeRole, limes.CodeInvalidConfig},
		"ErrChargeTransaction": {ErrChargeTransaction, limes.CodeInvalidRequest},
	} {
		if got, ok := limes.CodeOf(fmt.Errorf("context: %w", tc.err)); !ok || got != tc.want {
			t.Errorf("%s: CodeOf = %q, %v; want %q", name, got, ok, tc.want)
		}
	}
}
