package dpop_test

import (
	"testing"

	"github.com/oleg-tkachuk/limes/dpop"
)

// The codes are RFC 9449's, which a client reads off the wire.
func TestErrorCodesAreRFC9449s(t *testing.T) {
	for got, want := range map[string]string{
		dpop.ErrorCodeInvalidProof: "invalid_dpop_proof",
		dpop.ErrorCodeUseNonce:     "use_dpop_nonce",
	} {
		if got != want {
			t.Errorf("code %q, want RFC 9449's %q", got, want)
		}
	}
}
