package exchange

import (
	"testing"

	"github.com/oleg-tkachuk/limes/internal/errguard"
)

// wrappedHelpers are the functions of this module that construct an error
// naming no sentinel, each because every caller wraps it in a family at the
// API's edge. A construction anywhere else names a sentinel, or fails here.
var wrappedHelpers = map[string]map[string]string{}

// Every error this module constructs names a sentinel, so it has a code
// (docs/errors.md), but for the helpers listed above.
func TestEveryErrorNamesASentinel(t *testing.T) {
	errguard.Refuse(t, ".", wrappedHelpers)
}
