package dpop

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"time"

	"github.com/oleg-tkachuk/limes/internal/errs"
	"github.com/oleg-tkachuk/limes/internal/jose"
)

// Server-provided DPoP nonces, after RFC 9449 §8 and §9.
//
// A verifier configured with Nonces refuses a proof that carries no nonce
// it issued, with ErrNonceRequired — "use_dpop_nonce" on the wire — and
// the server sends a fresh one in the DPoP-Nonce header for the client to
// retry with. A proof must then carry a nonce the server issued, so it
// cannot be signed earlier than the nonce's lifetime before it is used; its
// iat must still fall within the Verifier's window.
//
// What a nonce bounds is that lifetime, and no more. An HMACNonces nonce is
// its issuance time and an HMAC of it: it names no client and no key, every
// client asking within the same second gets the same one, and any of them
// may carry it in any number of proofs until it expires — DefaultNonceLifetime
// unless configured shorter. Replay of one proof is the ReplayCache's to
// stop, not the nonce's. HMACNonces needs no state shared between replicas,
// only the same keys, and clocks within HMACNonceClockSkew of each other.

// NonceHeader is the HTTP response header a nonce travels in (RFC 9449
// §8).
const NonceHeader = "DPoP-Nonce"

// ErrNonceRequired — the verifier requires a server-provided nonce and
// the proof carried none, or one it did not issue or that has expired. The
// response carries a fresh one (Verifier.Nonce).
var ErrNonceRequired = errors.New("dpop: nonce required")

// Nonces issues the nonces a verifier hands out and recognises them when
// a proof carries one back.
type Nonces interface {
	// Nonce returns a nonce to send in the DPoP-Nonce header.
	Nonce(ctx context.Context) (string, error)
	// Valid reports whether nonce is one Nonce issued that is still current.
	// An error is a store that cannot answer; Verify refuses the proof with
	// it.
	Valid(ctx context.Context, nonce string) (bool, error)
}

// HMACNonce sizes and defaults.
const (
	// HMACNonceKeyBytes is the least key length NewHMACNonces accepts: the
	// output size of the HMAC's hash.
	HMACNonceKeyBytes = sha256.Size
	// DefaultNonceLifetime is how long a nonce is accepted when the
	// configuration names no lifetime.
	DefaultNonceLifetime = 5 * time.Minute
	// HMACNonceClockSkew is how far ahead of a replica's clock a nonce's
	// issuance may lie: another replica's clock may run ahead, and the
	// issuance is kept to the second.
	HMACNonceClockSkew = 5 * time.Second
	// nonceTimeBytes holds a nonce's issuance time, in Unix seconds.
	nonceTimeBytes = 8
)

// HMACNonces is a Nonces that keeps no state: a nonce is its issuance
// time and an HMAC of it, so every replica holding the same keys issues and
// accepts the same nonces. A nonce is not bound to the client it was sent
// to, nor used up by a proof: it limits how long before use a proof can be
// signed to its lifetime, so keep Lifetime as short as the clients' round
// trip allows. Rotate by putting the new key first: it issues,
// and the old ones are still accepted until their nonces expire.
type HMACNonces struct {
	keys     *sealedKeys
	lifetime time.Duration
	now      func() time.Time
}

// HMACNoncesConfig configures HMACNonces.
type HMACNoncesConfig struct {
	// Keys check nonces; the first also signs new ones. Each is at least
	// HMACNonceKeyBytes long. Required; SealNonceKeys makes them.
	Keys NonceKeys
	// Lifetime is how long a nonce is accepted. Zero: DefaultNonceLifetime.
	Lifetime time.Duration
	// Now is the clock. Nil: time.Now.
	Now func() time.Time
}

// NewHMACNonces builds HMACNonces, refusing no key, a short one, or a
// negative lifetime.
func NewHMACNonces(cfg HMACNoncesConfig) (*HMACNonces, error) {
	given := cfg.Keys.k.get()
	if len(given) == 0 {
		return nil, errs.Config("dpop: HMACNonces needs a key")
	}
	for i, k := range given {
		if len(k) < HMACNonceKeyBytes {
			return nil, errs.Config("dpop: HMACNonces key %d is %d bytes, at least %d", i, len(k), HMACNonceKeyBytes)
		}
	}
	if cfg.Lifetime < 0 {
		return nil, errs.Config("dpop: nonce lifetime %s is negative", cfg.Lifetime)
	}
	if cfg.Lifetime == 0 {
		cfg.Lifetime = DefaultNonceLifetime
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &HMACNonces{keys: cfg.Keys.k, lifetime: cfg.Lifetime, now: cfg.Now}, nil
}

// errNoNonceKey is Nonce of an HMACNonces holding no key: the zero value,
// not one NewHMACNonces made.
var errNoNonceKey = errs.Config("dpop: HMACNonces holds no key; make it with NewHMACNonces")

// Nonce implements Nonces. The zero HMACNonces holds no key and issues none.
func (n *HMACNonces) Nonce(context.Context) (string, error) {
	keys := n.keys.get()
	if len(keys) == 0 || n.now == nil {
		return "", errNoNonceKey
	}
	var issued [nonceTimeBytes]byte
	binary.BigEndian.PutUint64(issued[:], uint64(n.now().Unix())) //nolint:gosec // G115: a time after 1970 is positive
	return base64.RawURLEncoding.EncodeToString(append(issued[:], mac(keys[0], issued[:])...)), nil
}

// Valid implements Nonces: a nonce one of the keys signed, issued no
// longer than the lifetime ago and no more than HMACNonceClockSkew ahead.
func (n *HMACNonces) Valid(_ context.Context, nonce string) (bool, error) {
	// A nonce that does not decode is one this never issued: not valid, and
	// no failure of the check.
	raw, ok := decodeNonce(nonce)
	if !ok {
		return false, nil
	}
	issuedBytes, sum := raw[:nonceTimeBytes], raw[nonceTimeBytes:]
	signed := false
	for _, k := range n.keys.get() {
		signed = signed || hmac.Equal(sum, mac(k, issuedBytes))
	}
	if !signed {
		return false, nil
	}
	issued := time.Unix(int64(binary.BigEndian.Uint64(issuedBytes)), 0) //nolint:gosec // G115: signed by us, so in range
	age := n.now().Sub(issued)
	return age >= -HMACNonceClockSkew && age <= n.lifetime, nil
}

// decodeNonce is nonce's bytes, if it has a nonce's encoding and length.
func decodeNonce(nonce string) ([]byte, bool) {
	raw, err := jose.UnB64(nonce)
	return raw, err == nil && len(raw) == nonceTimeBytes+sha256.Size
}

func mac(key, msg []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(msg)
	return h.Sum(nil)
}
