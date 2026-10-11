package jwks

import (
	"crypto/ed25519"

	"github.com/oleg-tkachuk/limes/internal/jose"
)

// KeyID is the kid of a key published with none named: its RFC 7638
// thumbprint. The same key always gets the same kid, wherever it is derived,
// and two keys never share one.
func KeyID(pub ed25519.PublicKey) string {
	return jose.Ed25519Thumbprint(pub)
}
