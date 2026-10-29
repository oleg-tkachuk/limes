package limes

import (
	"testing"

	"github.com/oleg-tkachuk/limes/internal/errguard"
)

// wrappedHelpers are the functions of this module that construct an error
// naming no sentinel, each because every caller wraps it in a family at the
// API's edge: their text is kept as the cause the family's message names.
// A construction anywhere else names a sentinel, or fails here.
var wrappedHelpers = map[string]map[string]string{
	"internal/jose/jose.go": {
		"UnB64": "decoding helpers: the verifier, inspect and dpop wrap their errors in ErrInvalidToken or dpop.ErrInvalid",
		"Split": "", "PublicJWK": "", "Thumbprint": "", "PublicKey": "", "ed25519Key": "", "p256Key": "", "VerifyEd25519": "",
	},
	"internal/rfc3986/rfc3986.go": {
		"errNotAbsolute": "dpop wraps a URI it cannot normalise in ErrInvalidRequest, or answers the match false",
		"errUserinfo":    "",
	},
	"jwks/remote.go": {
		"sameOrigin": "a fetch's failures: Remote returns them inside ErrUnavailable",
		"fetch":      "", "readKeys": "", "readCapped": "",
	},
	"sealed_form.go": {
		"applyAttenuation": "a Biscuit's blocks: unsealedGrant refuses with ErrBiscuitAttenuation or ErrInvalidToken",
		"copyAxisNarrows":  "", "checkUnsealed": "",
	},
	"signer.go": {
		"priorActors":            "reading claims: readSignedClaims refuses with ErrInvalidToken, inspect.Decode too",
		"unmarshalCanonicalUUID": "", "parseHeader": "", "decodeClaims": "", "readConfirmation": "", "readParentID": "",
		"readActing": "", "checkActingForItself": "", "readOnBehalfOf": "", "canonicalUUID": "",
	},
	"metertest/metertest.go": {
		"checkCallbackRollback": "a conformance check's own fixture: the error a side effect fails with",
	},
}

// Every error this module constructs names a sentinel, so it has a code
// (CodeOf, ProblemOf), but for the helpers listed above.
func TestEveryErrorNamesASentinel(t *testing.T) {
	errguard.Refuse(t, ".", wrappedHelpers)
}
