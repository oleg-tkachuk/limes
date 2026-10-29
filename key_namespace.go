package limes

import (
	"encoding/json/v2"

	"github.com/oleg-tkachuk/limes/internal/jose"
	"github.com/oleg-tkachuk/limes/jwks"
)

// HasLimesKeyID reports whether token is a compact JWT whose header names a
// kid in limes's namespace (jwks.KeyIDPrefix): the one thing that tells a
// capability from an identity provider's access token, since both are typed
// at+jwt (RFC 9068). It reads the header alone, verifies nothing, and is
// never an authorization decision: a forged token can name any kid. Use it
// to route a bearer to the verifier that can decide — Verifier.Verify when
// it answers true, the identity provider's own verifier when it answers
// false — which then verifies everything. Verifier.Verify refuses a kid
// outside the namespace before it looks a key up, so an access token routed
// to it by mistake is refused, never read; a Biscuit, being no JWT, answers
// false.
func HasLimesKeyID(token string) bool {
	parts, err := jose.Split(token)
	if err != nil {
		return false
	}
	raw, err := jose.UnB64(parts.Header)
	if err != nil {
		return false
	}
	// Only the kid is read, and the header is not held to a capability's:
	// an identity provider's carries members a capability's does not.
	var header struct {
		Kid string `json:"kid"`
	}
	if err := json.Unmarshal(raw, &header); err != nil {
		return false
	}
	return jwks.IsKeyID(header.Kid)
}
