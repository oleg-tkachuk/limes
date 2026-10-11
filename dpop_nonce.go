package limes

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"time"
)

// Server-provided DPoP nonces, after RFC 9449 §8 and §9.
//
// A verifier configured with DPoPNonces refuses a proof that carries no nonce
// it issued, with ErrDPoPNonceRequired — "use_dpop_nonce" on the wire — and
// the server sends a fresh one in the DPoP-Nonce header for the client to
// retry with. A proof must then carry a nonce the server issued, so it
// cannot be signed ahead of time; its iat must still fall within the
// DPoPVerifier's window. HMACNonces needs no state shared between replicas,
// only the same keys, and clocks within HMACNonceClockSkew of each other.

// DPoPNonceHeader is the HTTP response header a nonce travels in (RFC 9449
// §8).
const DPoPNonceHeader = "DPoP-Nonce"

// ErrDPoPNonceRequired — the verifier requires a server-provided nonce and
// the proof carried none, or one it did not issue or that has expired. The
// response carries a fresh one (StandardVerifier.DPoPNonce). Matches
// ErrProofOfPossession, as every proof-of-possession refusal does.
var ErrDPoPNonceRequired error = &familyError{"limes: DPoP nonce required", ErrProofOfPossession}

// DPoPNonces issues the nonces a verifier hands out and recognises them when
// a proof carries one back.
type DPoPNonces interface {
	// Nonce returns a nonce to send in the DPoP-Nonce header.
	Nonce(ctx context.Context) (string, error)
	// Valid reports whether nonce is one Nonce issued that is still current.
	Valid(ctx context.Context, nonce string) bool
}

// HMACNonce sizes and defaults.
const (
	// HMACNonceKeyBytes is the least key length NewHMACNonces accepts: the
	// output size of the HMAC's hash.
	HMACNonceKeyBytes = sha256.Size
	// DefaultDPoPNonceLifetime is how long a nonce is accepted when
	// NewHMACNonces is given no lifetime.
	DefaultDPoPNonceLifetime = 5 * time.Minute
	// HMACNonceClockSkew is how far ahead of a replica's clock a nonce's
	// issuance may lie: another replica's clock may run ahead, and the
	// issuance is kept to the second.
	HMACNonceClockSkew = 5 * time.Second
	// nonceTimeBytes holds a nonce's issuance time, in Unix seconds.
	nonceTimeBytes = 8
)

// HMACNonces is a DPoPNonces that keeps no state: a nonce is its issuance
// time and an HMAC of it, so every replica holding the same keys issues and
// accepts the same nonces. Rotate by putting the new key first: it issues,
// and the old ones are still accepted until their nonces expire.
type HMACNonces struct {
	keys     [][]byte
	lifetime time.Duration
	now      func() time.Time
}

// NewHMACNonces builds an HMACNonces. keys[0] signs new nonces; every key
// checks. Each key is at least HMACNonceKeyBytes long; lifetime ≤ 0 is
// DefaultDPoPNonceLifetime.
func NewHMACNonces(lifetime time.Duration, keys ...[]byte) (*HMACNonces, error) {
	if len(keys) == 0 {
		return nil, errors.New("limes: HMACNonces needs a key")
	}
	for i, k := range keys {
		if len(k) < HMACNonceKeyBytes {
			return nil, fmt.Errorf("limes: HMACNonces key %d is %d bytes, at least %d", i, len(k), HMACNonceKeyBytes)
		}
	}
	if lifetime <= 0 {
		lifetime = DefaultDPoPNonceLifetime
	}
	cloned := make([][]byte, len(keys))
	for i, k := range keys {
		cloned[i] = append([]byte(nil), k...)
	}
	return &HMACNonces{keys: cloned, lifetime: lifetime, now: time.Now}, nil
}

// Nonce implements DPoPNonces.
func (n *HMACNonces) Nonce(context.Context) (string, error) {
	var issued [nonceTimeBytes]byte
	binary.BigEndian.PutUint64(issued[:], uint64(n.now().Unix())) //nolint:gosec // G115: a time after 1970 is positive
	return base64.RawURLEncoding.EncodeToString(append(issued[:], mac(n.keys[0], issued[:])...)), nil
}

// Valid implements DPoPNonces: a nonce one of the keys signed, issued no
// longer than the lifetime ago and no more than HMACNonceClockSkew ahead.
func (n *HMACNonces) Valid(_ context.Context, nonce string) bool {
	raw, err := base64.RawURLEncoding.DecodeString(nonce)
	if err != nil || len(raw) != nonceTimeBytes+sha256.Size {
		return false
	}
	issuedBytes, sum := raw[:nonceTimeBytes], raw[nonceTimeBytes:]
	signed := false
	for _, k := range n.keys {
		signed = signed || hmac.Equal(sum, mac(k, issuedBytes))
	}
	if !signed {
		return false
	}
	issued := time.Unix(int64(binary.BigEndian.Uint64(issuedBytes)), 0) //nolint:gosec // G115: signed by us, so in range
	age := n.now().Sub(issued)
	return age >= -HMACNonceClockSkew && age <= n.lifetime
}

func mac(key, msg []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(msg)
	return h.Sum(nil)
}
