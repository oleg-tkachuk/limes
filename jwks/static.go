package jwks

import (
	"context"
	"crypto/ed25519"
	"errors"
	"maps"
	"sync"
)

// ErrUnknownKID is the typed not-found return from KeyResolver. The
// verifier wraps it in ErrInvalidToken for caller-facing errors, and it
// stays matchable, so ops dashboards can tell "we don't know this kid" from
// "signature mismatch".
var ErrUnknownKID = errors.New("jwks: unknown kid")

// Static is the in-memory implementation used in tests and
// single-node deploys. Maps kid → public key; concurrency-safe via a
// sync.RWMutex so rotation can swap a kid without restarting callers.
type Static struct {
	mu   sync.RWMutex
	keys map[string]ed25519.PublicKey
}

// NewStatic builds a resolver from a kid → public key map.
// The map is copied to defend against caller mutation.
func NewStatic(keys map[string]ed25519.PublicKey) *Static {
	return &Static{keys: maps.Clone(keys)}
}

// PublicKey implements KeyResolver.
func (r *Static) PublicKey(_ context.Context, kid string) (ed25519.PublicKey, error) {
	r.mu.RLock()
	k, ok := r.keys[kid]
	r.mu.RUnlock()
	if !ok {
		return nil, ErrUnknownKID
	}
	return k, nil
}

// SetKey adds or replaces a kid → key mapping. Rotation step 1: publish
// the new key alongside the old.
func (r *Static) SetKey(kid string, key ed25519.PublicKey) {
	r.mu.Lock()
	if r.keys == nil {
		r.keys = make(map[string]ed25519.PublicKey)
	}
	r.keys[kid] = key
	r.mu.Unlock()
}

// RemoveKey withdraws a kid. Rotation step 3: call it only once the longest
// outstanding TTL signed under that key has elapsed.
func (r *Static) RemoveKey(kid string) {
	r.mu.Lock()
	delete(r.keys, kid)
	r.mu.Unlock()
}

// Keys returns a snapshot of the current key set, e.g. for Marshal.
func (r *Static) Keys() map[string]ed25519.PublicKey {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return maps.Clone(r.keys)
}
