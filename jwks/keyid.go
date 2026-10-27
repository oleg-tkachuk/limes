package jwks

import (
	"crypto/ed25519"
	"strings"

	"github.com/oleg-tkachuk/limes/internal/jose"
)

// KeyIDPrefix opens the kid of every key a limes issuer signs with. A
// capability is an at+jwt, a type any authorization server's access tokens
// share, so the kid is what marks a token as signed by a key of limes's: a
// verifier refuses any other before it looks a key up, and a signer will
// not sign under one. It cannot stop a key being copied to another issuer;
// it keeps limes from reading a token signed under that issuer's kid.
const KeyIDPrefix = "limes:"

// KeyID is the kid of a key published with none named: KeyIDPrefix and its
// RFC 7638 thumbprint. The same key always gets the same kid, wherever it is
// derived, and two keys never share one.
func KeyID(pub ed25519.PublicKey) string {
	return KeyIDPrefix + jose.Ed25519Thumbprint(pub)
}

// IsKeyID reports whether kid is in limes's namespace: KeyIDPrefix and a
// name after it.
func IsKeyID(kid string) bool {
	name, ok := strings.CutPrefix(kid, KeyIDPrefix)
	return ok && name != ""
}
