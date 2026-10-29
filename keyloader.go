package limes

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"

	"github.com/oleg-tkachuk/limes/jwks"
)

// pemTypePrivateKey is the PEM type of a PKCS#8 private key (RFC 7468 §10).
const pemTypePrivateKey = "PRIVATE KEY"

// loadPrivateKey reads an Ed25519 private key from a PEM-encoded
// PKCS#8 file at the supplied path. Production wires this against a
// Secret-mounted RO volume; dev calls GenerateKey instead
// and skips this path entirely.
//
// Only PKCS#8 under PEM type "PRIVATE KEY" is read — not a raw seed, not
// another PEM type — because that is OpenSSL's default output. A wrong
// format is loud rather than read one way here and another there.
func loadPrivateKey(path string) (ed25519.PrivateKey, error) {
	if path == "" {
		return nil, invalidConfig("limes: empty private key path")
	}
	raw, err := os.ReadFile(path) // #nosec G304 — operator-supplied path
	if err != nil {
		return nil, fmt.Errorf("limes: read private key %q: %w", path, err)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, invalidConfig("limes: %q is not PEM-encoded", path)
	}
	if block.Type != pemTypePrivateKey {
		return nil, invalidConfig("limes: %q has PEM type %q (want PRIVATE KEY)",
			path, block.Type)
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("limes: parse PKCS#8 in %q: %w", path, err)
	}
	priv, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return nil, invalidConfig("limes: %q holds %T, not ed25519.PrivateKey",
			path, parsed)
	}
	return priv, nil
}

// SigningKey is an Ed25519 key and the kid it is published under. It holds
// the private key alone, and Public derives the public half from it, so the
// two cannot disagree.
type SigningKey struct {
	KeyID string
	// Private is the private key, sealed: SignerConfig.Key takes it as it
	// is, and no formatting of a SigningKey reaches it.
	Private SealedKey
}

// Public is the public half of Private; nil when Private holds no Ed25519
// key.
func (k SigningKey) Public() ed25519.PublicKey {
	pub, _ := k.Private.Public().(ed25519.PublicKey)
	return pub
}

// LoadKey reads the PKCS#8 PEM signing key at path, under the kid
// jwks.KeyID derives from it; a deployment that names its kids sets
// SigningKey.KeyID on the result. For a key that lives
// only as long as the process, GenerateKey; a deployment that may have no
// key file chooses between the two itself, so a missing mount is never
// silently a fresh key.
func LoadKey(path string) (SigningKey, error) {
	priv, err := loadPrivateKey(path)
	if err != nil {
		return SigningKey{}, err
	}
	key := SigningKey{Private: SealKey(priv)}
	// SealKey keeps a copy; the key is held there alone.
	clear(priv)
	key.KeyID = jwks.KeyID(key.Public())
	return key, nil
}
