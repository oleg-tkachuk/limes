package limes

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
)

// LoadPrivateKey reads an Ed25519 private key from a PEM-encoded
// PKCS#8 file at the supplied path. Production wires this against a
// Secret-mounted RO volume; dev calls GenerateEd25519Keypair instead
// and skips this path entirely.
//
// We accept only PKCS#8 (not legacy PEM "PRIVATE KEY" with raw bytes)
// because that's the OpenSSL-default output and what kubectl-managed
// secrets typically contain. Wrong format is loud — better than
// silently working with one form and breaking on the other.
func LoadPrivateKey(path string) (ed25519.PrivateKey, error) {
	if path == "" {
		return nil, errors.New("capability: empty private key path")
	}
	raw, err := os.ReadFile(path) // #nosec G304 — operator-supplied path
	if err != nil {
		return nil, fmt.Errorf("capability: read private key %q: %w", path, err)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, fmt.Errorf("capability: %q is not PEM-encoded", path)
	}
	if block.Type != "PRIVATE KEY" {
		return nil, fmt.Errorf("capability: %q has PEM type %q (want PRIVATE KEY)",
			path, block.Type)
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("capability: parse PKCS#8 in %q: %w", path, err)
	}
	priv, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("capability: %q holds %T, not ed25519.PrivateKey",
			path, parsed)
	}
	return priv, nil
}

// MustLoadOrGenerate returns either the on-disk keypair (when path is
// set and readable) or a freshly-generated ephemeral one. The boolean
// `generated` lets callers log a warning so an operator who forgot to
// mount the Secret notices on the first restart instead of silently
// running on a key that vanishes when the pod recycles.
func MustLoadOrGenerate(path, kid string) (resolvedKID string, pub ed25519.PublicKey, priv ed25519.PrivateKey, generated bool, err error) {
	if path != "" {
		priv, err = LoadPrivateKey(path)
		if err != nil {
			return "", nil, nil, false, err
		}
		pub = priv.Public().(ed25519.PublicKey)
		if kid == "" {
			kid = fmt.Sprintf("%x", pub[:8])
		}
		return kid, pub, priv, false, nil
	}
	resolvedKID, pub, priv, err = GenerateEd25519Keypair()
	if err != nil {
		return "", nil, nil, false, err
	}
	if kid != "" {
		resolvedKID = kid
	}
	return resolvedKID, pub, priv, true, nil
}
