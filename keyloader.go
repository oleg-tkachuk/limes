package limes

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"os"

	"github.com/oleg-tkachuk/limes/internal/errs"
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
		return nil, errs.Errorf("limes: read private key %q: %w", path, err)
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
		return nil, errs.Errorf("limes: parse PKCS#8 in %q: %w", path, err)
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
// SigningKey.KeyID on the result. It refuses, as ErrInvalidConfig, an empty
// path, a file that is not PEM, a PEM type other than "PRIVATE KEY" and a
// key that is not Ed25519; a file it cannot read, or PKCS#8 it cannot parse,
// returns that error wrapped. For a key that lives only as long as the
// process, GenerateKey; a deployment that may have no key file chooses
// between the two itself, so a missing mount is never silently a fresh key.
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

// pemTypePublicKey is the PEM type of a PKIX public key (RFC 7468 §13).
const pemTypePublicKey = "PUBLIC KEY"

// The modes a key file is created with: the private key readable by its
// owner alone, the public key by anyone.
const (
	privateKeyFileMode os.FileMode = 0o600
	publicKeyFileMode  os.FileMode = 0o644
)

// createKeyFile is the flags a key file is opened with: created, and never
// over a file already there, so a key is not lost to a mistyped path.
const createKeyFile = os.O_WRONLY | os.O_CREATE | os.O_EXCL

// WriteKey writes key's private key to a new file at path, PKCS#8 under
// PEM type "PRIVATE KEY" as LoadKey reads it, readable by its owner alone
// (0600). It refuses, as ErrInvalidConfig, an empty path and a key it
// cannot export — the zero key, or a crypto.Signer over a key held
// elsewhere, such as a KMS's; a file already at path, or one it cannot
// create or write, returns that error wrapped, and a file it began is
// removed. The kid is not written: LoadKey derives it again.
func WriteKey(path string, key SigningKey) error {
	if path == "" {
		return invalidConfig("limes: empty private key path")
	}
	priv, ok := key.Private.signer().(ed25519.PrivateKey)
	if !ok {
		return invalidConfig("limes: the key cannot be exported: it holds no ed25519.PrivateKey")
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return errs.Errorf("limes: encode private key: %w", err)
	}
	defer clear(der)
	block := pem.EncodeToMemory(&pem.Block{Type: pemTypePrivateKey, Bytes: der})
	defer clear(block)
	return writeKeyFile(path, block, privateKeyFileMode)
}

// PublicKey is an Ed25519 public key and the kid it is published under: a
// key a verifier trusts, such as one retired from signing, kept without its
// private half.
type PublicKey struct {
	KeyID string
	Key   ed25519.PublicKey
}

// WritePublicKey writes pub to a new file at path, PKIX under PEM type
// "PUBLIC KEY" as LoadPublicKey reads it (0644). It refuses, as
// ErrInvalidConfig, an empty path and a key that is not an Ed25519 public
// key; a file already at path, or one it cannot create or write, returns
// that error wrapped, and a file it began is removed.
func WritePublicKey(path string, pub ed25519.PublicKey) error {
	if path == "" {
		return invalidConfig("limes: empty public key path")
	}
	if len(pub) != ed25519.PublicKeySize {
		return invalidConfig("limes: a public key of %d bytes is no Ed25519 key", len(pub))
	}
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return errs.Errorf("limes: encode public key: %w", err)
	}
	return writeKeyFile(path, pem.EncodeToMemory(&pem.Block{Type: pemTypePublicKey, Bytes: der}), publicKeyFileMode)
}

// LoadPublicKey reads the PKIX PEM public key at path, under the kid
// jwks.KeyID derives from it, as LoadKey does for a private key: a key a
// verifier keeps trusting once it no longer signs needs no private half on
// disk. It refuses, as ErrInvalidConfig, an empty path, a file that is not
// PEM, a PEM type other than "PUBLIC KEY" and a key that is not Ed25519; a
// file it cannot read, or PKIX it cannot parse, returns that error wrapped.
func LoadPublicKey(path string) (PublicKey, error) {
	if path == "" {
		return PublicKey{}, invalidConfig("limes: empty public key path")
	}
	raw, err := os.ReadFile(path) // #nosec G304 — operator-supplied path
	if err != nil {
		return PublicKey{}, errs.Errorf("limes: read public key %q: %w", path, err)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return PublicKey{}, invalidConfig("limes: %q is not PEM-encoded", path)
	}
	if block.Type != pemTypePublicKey {
		return PublicKey{}, invalidConfig("limes: %q has PEM type %q (want PUBLIC KEY)", path, block.Type)
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return PublicKey{}, errs.Errorf("limes: parse PKIX in %q: %w", path, err)
	}
	pub, ok := parsed.(ed25519.PublicKey)
	if !ok {
		return PublicKey{}, invalidConfig("limes: %q holds %T, not ed25519.PublicKey", path, parsed)
	}
	return PublicKey{KeyID: jwks.KeyID(pub), Key: pub}, nil
}

// writeKeyFile writes data to a new file at path under mode, removing what
// it began when the write fails.
func writeKeyFile(path string, data []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, createKeyFile, mode) // #nosec G304 — operator-supplied path
	if err != nil {
		return errs.Errorf("limes: create key file %q: %w", path, err)
	}
	_, err = f.Write(data)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(path)
		return errs.Errorf("limes: write key file %q: %w", path, err)
	}
	return nil
}
