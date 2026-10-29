// Package inspect reads a capability token for tooling — a CLI that shows a
// token, a debugger, a log viewer — without verifying it. Nothing here
// checks time, audience, revocation or proof of possession, so never
// authorise a request on what it returns: that is limes.Verifier.Verify.
package inspect

import (
	"crypto/ed25519"
	"fmt"

	"github.com/oleg-tkachuk/limes/internal/errs"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/internal/jose"
	"github.com/oleg-tkachuk/limes/internal/link"
)

// MaxTokenBytes is the longest token Decode and VerifySignature read: what a
// verifier reads by default (limes.DefaultMaxTokenBytes). A tool reads what
// a log or a user hands it, and a longer token would be parsed whole first.
const MaxTokenBytes = limes.DefaultMaxTokenBytes

// checkSize refuses a token longer than MaxTokenBytes, unread.
func checkSize(token string) error {
	if len(token) > MaxTokenBytes {
		return fmt.Errorf("%w: %d bytes, over %d", limes.ErrInvalidToken, len(token), MaxTokenBytes)
	}
	return nil
}

// ErrInvalidKey is VerifySignature handed a key that is no Ed25519 public
// key: the caller's mistake, which says nothing about the token; it matches
// limes.ErrInvalidRequest.
var ErrInvalidKey = fmt.Errorf("%w: inspect: not an Ed25519 public key", limes.ErrInvalidRequest)

// Decode reads a compact token's claims without checking its signature. A
// token it cannot read — one whose header names another alg than Ed25519
// (RFC 9864), EdDSA included, as a verifier refuses it — or one longer than
// MaxTokenBytes, is limes.ErrInvalidToken, as VerifySignature reports one.
func Decode(token string) (limes.Capability, error) {
	if err := checkSize(token); err != nil {
		return limes.Capability{}, err
	}
	c, err := link.DecodeToken(token)
	if err != nil {
		return limes.Capability{}, errs.Errorf("%w: %w", limes.ErrInvalidToken, err)
	}
	return c.(limes.Capability), nil
}

// VerifySignature checks the Ed25519 signature on a compact token against
// pub, and nothing else. A forged or altered token, or one longer than
// MaxTokenBytes, is limes.ErrInvalidToken; a pub that is no Ed25519 public
// key is ErrInvalidKey.
func VerifySignature(token string, pub ed25519.PublicKey) error {
	if len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("%w: %d bytes, want %d", ErrInvalidKey, len(pub), ed25519.PublicKeySize)
	}
	if err := checkSize(token); err != nil {
		return err
	}
	parts, err := jose.Split(token)
	if err != nil {
		return errs.Errorf("%w: %w", limes.ErrInvalidToken, err)
	}
	if err := jose.VerifyEd25519(parts, pub); err != nil {
		return errs.Errorf("%w: %w", limes.ErrInvalidToken, err)
	}
	return nil
}
