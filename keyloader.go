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
	if block.Type != "PRIVATE KEY" {
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

// LoadOrGenerateKey reads the key at path (LoadPrivateKey) or, when path is
// empty, generates an ephemeral one. kid names it; empty derives one
// (DeriveKeyID). A path that cannot be read is an error, never a reason to
// fall back to a fresh key. Log a generated key loudly: an operator who
// forgot to mount the file finds out on the first restart otherwise.
func LoadOrGenerateKey(path, kid string) (SigningKey, error) {
	var priv ed25519.PrivateKey
	generated := path == ""
	if generated {
		var err error
		if _, _, priv, err = GenerateEd25519Keypair(); err != nil {
			return SigningKey{}, err
		}
	} else {
		var err error
		if priv, err = LoadPrivateKey(path); err != nil {
			return SigningKey{}, err
		}
	}
	pub := priv.Public().(ed25519.PublicKey)
	if kid == "" {
		kid = DeriveKeyID(pub)
	}
	return SigningKey{KeyID: kid, Public: pub, Private: priv, Generated: generated}, nil
}
