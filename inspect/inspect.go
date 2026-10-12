// Package inspect reads a capability token for tooling — a CLI that shows a
// token, a debugger, a log viewer — without verifying it. Nothing here
// checks time, audience, revocation or proof of possession, so never
// authorise a request on what it returns: that is limes.Verifier.Verify.
package inspect

import (
	"crypto/ed25519"
	"fmt"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/internal/jose"
	"github.com/oleg-tkachuk/limes/internal/link"
)

// Decode reads a compact token's claims without checking its signature. A
// token it cannot read is limes.ErrInvalidToken, as VerifySignature reports
// one.
func Decode(token string) (limes.Capability, error) {
	c, err := link.DecodeToken(token)
	if err != nil {
		return limes.Capability{}, fmt.Errorf("%w: %w", limes.ErrInvalidToken, err)
	}
	return c.(limes.Capability), nil
}

// VerifySignature checks the Ed25519 signature on a compact token against
// pub, and nothing else. A forged or altered token is limes.ErrInvalidToken.
func VerifySignature(token string, pub ed25519.PublicKey) error {
	parts, err := jose.Split(token)
	if err != nil {
		return fmt.Errorf("%w: %w", limes.ErrInvalidToken, err)
	}
	if err := jose.VerifyEd25519(parts, pub); err != nil {
		return fmt.Errorf("%w: %w", limes.ErrInvalidToken, err)
	}
	return nil
}
