package jwks

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"maps"
	"slices"
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

// StaticConfig configures a Static resolver.
type StaticConfig struct {
	// Keys maps each kid to its Ed25519 public key. Copied: changing the
	// map afterwards changes nothing the resolver serves.
	Keys map[string]ed25519.PublicKey
}

// NewStatic builds a resolver over cfg.Keys, refusing an empty kid or a key
// that is not an Ed25519 public key's length — which the verifier would
// otherwise read, token by token, as the tokens' fault.
func NewStatic(cfg StaticConfig) (*Static, error) {
	keys := make(map[string]ed25519.PublicKey, len(cfg.Keys))
	for kid, key := range cfg.Keys {
		if err := validateKey(kid, key); err != nil {
			return nil, err
		}
		keys[kid] = slices.Clone(key)
	}
	return &Static{keys: keys}, nil
}

// validateKey refuses an empty kid, or a key of the wrong length.
func validateKey(kid string, key ed25519.PublicKey) error {
	switch {
	case kid == "":
		return errors.New("jwks: a key needs a kid")
	case len(key) != ed25519.PublicKeySize:
		return fmt.Errorf("jwks: key %q is %d bytes, not an Ed25519 public key's %d", kid, len(key), ed25519.PublicKeySize)
	}
	return nil
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
// the new key alongside the old. It refuses what NewStatic refuses.
func (r *Static) SetKey(kid string, key ed25519.PublicKey) error {
	if err := validateKey(kid, key); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.keys == nil {
		r.keys = make(map[string]ed25519.PublicKey)
	}
	r.keys[kid] = slices.Clone(key)
	return nil
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
