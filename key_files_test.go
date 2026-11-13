package limes

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// The files the key tests write, in each test's own directory.
const (
	privateKeyFile = "signing.pem"
	publicKeyFile  = "retired.pem"
)

// heldElsewhere is a signer over a key it does not hand out, as a KMS's is.
type heldElsewhere struct{ crypto.Signer }

// A key WriteKey writes, LoadKey reads back as the same key under the same
// kid, from a file only its owner can read; its public half, written by
// WritePublicKey, LoadPublicKey reads back alone under that kid.
func TestKeyFilesRoundTrip(t *testing.T) {
	key, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	priv, pub := filepath.Join(dir, privateKeyFile), filepath.Join(dir, publicKeyFile)
	if err := WriteKey(priv, key); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(priv); err != nil || info.Mode().Perm() != privateKeyFileMode {
		t.Errorf("the private key file's mode = %v, %v; want %v", info.Mode().Perm(), err, privateKeyFileMode)
	}
	loaded, err := LoadKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.KeyID != key.KeyID || !loaded.Public().Equal(key.Public()) {
		t.Errorf("LoadKey after WriteKey = %s, want %s", loaded.KeyID, key.KeyID)
	}
	if err := WritePublicKey(pub, key.Public()); err != nil {
		t.Fatal(err)
	}
	got, err := LoadPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	if got.KeyID != key.KeyID || !got.Key.Equal(key.Public()) {
		t.Errorf("LoadPublicKey after WritePublicKey = %s, want %s", got.KeyID, key.KeyID)
	}
	if _, err := LoadKey(pub); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("LoadKey of a public key file: %v, want ErrInvalidConfig", err)
	}
	if _, err := LoadPublicKey(priv); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("LoadPublicKey of a private key file: %v, want ErrInvalidConfig", err)
	}
}

// A key file is never written over one already there, and a key that cannot
// be exported, or a path that is empty, is refused before any file is made.
func TestWriteKeyRefuses(t *testing.T) {
	key, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	taken := filepath.Join(dir, privateKeyFile)
	if err := WriteKey(taken, key); err != nil {
		t.Fatal(err)
	}
	if err := WriteKey(taken, key); !errors.Is(err, os.ErrExist) {
		t.Errorf("WriteKey over a file: %v, want os.ErrExist", err)
	}
	if err := WritePublicKey(taken, key.Public()); !errors.Is(err, os.ErrExist) {
		t.Errorf("WritePublicKey over a file: %v, want os.ErrExist", err)
	}
	elsewhere := SigningKey{KeyID: key.KeyID, Private: SealKey(heldElsewhere{key.Private})}
	for name, tc := range map[string]struct {
		path string
		key  SigningKey
	}{
		"an empty path":        {path: "", key: key},
		"the zero key":         {path: filepath.Join(dir, "zero.pem")},
		"a key held elsewhere": {path: filepath.Join(dir, "kms.pem"), key: elsewhere},
	} {
		if err := WriteKey(tc.path, tc.key); !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("WriteKey of %s: %v, want ErrInvalidConfig", name, err)
		}
		if tc.path != "" {
			if _, err := os.Stat(tc.path); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("WriteKey of %s left a file: %v", name, err)
			}
		}
	}
	if err := WritePublicKey(filepath.Join(dir, "short.pem"), key.Public()[:1]); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("WritePublicKey of a short key: %v, want ErrInvalidConfig", err)
	}
	if err := WritePublicKey("", key.Public()); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("WritePublicKey to an empty path: %v, want ErrInvalidConfig", err)
	}
}

// LoadPublicKey reads only an Ed25519 key under PEM type "PUBLIC KEY".
func TestLoadPublicKeyRefuses(t *testing.T) {
	dir := t.TempDir()
	ec, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(ec.Public())
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string][]byte{
		"no PEM":       []byte("not a key"),
		"another type": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		"a P-256 key":  pem.EncodeToMemory(&pem.Block{Type: pemTypePublicKey, Bytes: der}),
	} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, body, publicKeyFileMode); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadPublicKey(path); !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("LoadPublicKey of %s: %v, want ErrInvalidConfig", name, err)
		}
	}
	if _, err := LoadPublicKey(""); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("LoadPublicKey of an empty path: %v, want ErrInvalidConfig", err)
	}
	if _, err := LoadPublicKey(filepath.Join(dir, "missing.pem")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("LoadPublicKey of a missing file: %v, want os.ErrNotExist", err)
	}
}
