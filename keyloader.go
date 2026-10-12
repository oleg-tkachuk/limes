package limes

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"

	"github.com/oleg-tkachuk/limes/jwks"
)

// pemTypePrivateKey is the PEM type of a PKCS#8 private key (RFC 7468 §10).
const pemTypePrivateKey = "PRIVATE KEY"

// LoadPrivateKey reads an Ed25519 private key from a PEM-encoded
// PKCS#8 file at the supplied path. Production wires this against a
// Secret-mounted RO volume; dev calls GenerateKey instead
// and skips this path entirely.
//
// Only PKCS#8 under PEM type "PRIVATE KEY" is read — not a raw seed, not
// another PEM type — because that is OpenSSL's default output. A wrong
// format is loud rather than read one way here and another there.
func LoadPrivateKey(path string) (ed25519.PrivateKey, error) {
	if path == "" {
		return nil, errors.New("limes: empty private key path")
	}
	raw, err := os.ReadFile(path) // #nosec G304 — operator-supplied path
	if err != nil {
		return nil, fmt.Errorf("limes: read private key %q: %w", path, err)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, fmt.Errorf("limes: %q is not PEM-encoded", path)
	}
	if block.Type != pemTypePrivateKey {
		return nil, fmt.Errorf("limes: %q has PEM type %q (want PRIVATE KEY)",
			path, block.Type)
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("limes: parse PKCS#8 in %q: %w", path, err)
	}
	priv, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("limes: %q holds %T, not ed25519.PrivateKey",
			path, parsed)
	}
	return priv, nil
}

// SigningKey is an Ed25519 key and the kid it is published under.
type SigningKey struct {
	KeyID   string
	Public  ed25519.PublicKey
	Private ed25519.PrivateKey
	// Generated reports that the key was made fresh rather than read from a
	// file: it vanishes with the process, and every token signed with it
	// stops verifying once it does.
	Generated bool
}

// LoadKey reads the signing key at path (LoadPrivateKey), under the kid
// jwks.KeyID derives from it; a deployment that names its kids sets
// SigningKey.KeyID on the result. For a key that lives
// only as long as the process, GenerateKey; a deployment that may have no
// key file chooses between the two itself, so a missing mount is never
// silently a fresh key.
func LoadKey(path string) (SigningKey, error) {
	priv, err := LoadPrivateKey(path)
	if err != nil {
		return SigningKey{}, err
	}
	pub := priv.Public().(ed25519.PublicKey)
	return SigningKey{KeyID: jwks.KeyID(pub), Public: pub, Private: priv}, nil
}
