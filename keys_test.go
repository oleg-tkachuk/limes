package limes

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/oleg-tkachuk/limes/jwks"
)

// keyFileMode is how the tests write a key file: readable by its owner only.
const keyFileMode = 0o600

// writePEM writes der as a PEM block of type typ and returns the file's path.
func writePEM(t *testing.T, typ string, der []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "key.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der}), keyFileMode); err != nil {
		t.Fatal(err)
	}
	return path
}

func ed25519PKCS8(t *testing.T) (ed25519.PrivateKey, []byte) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return priv, der
}

func TestLoadPrivateKey(t *testing.T) {
	priv, der := ed25519PKCS8(t)
	got, err := LoadPrivateKey(writePEM(t, "PRIVATE KEY", der))
	if err != nil || !got.Equal(priv) {
		t.Fatalf("LoadPrivateKey = %v; want the key written", err)
	}

	ec, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ecDER, err := x509.MarshalPKCS8PrivateKey(ec)
	if err != nil {
		t.Fatal(err)
	}
	notPEM := filepath.Join(t.TempDir(), "raw")
	if err := os.WriteFile(notPEM, []byte("not a key"), keyFileMode); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{
		"empty path":         "",
		"missing file":       filepath.Join(t.TempDir(), "absent.pem"),
		"not PEM":            notPEM,
		"wrong PEM type":     writePEM(t, "EC PRIVATE KEY", der),
		"not PKCS#8":         writePEM(t, "PRIVATE KEY", []byte("garbage")),
		"not an Ed25519 key": writePEM(t, "PRIVATE KEY", ecDER),
	} {
		if _, err := LoadPrivateKey(path); err == nil {
			t.Errorf("%s: LoadPrivateKey accepted it", name)
		}
	}
}

func TestLoadKey(t *testing.T) {
	priv, der := ed25519PKCS8(t)
	path := writePEM(t, "PRIVATE KEY", der)

	k, err := LoadKey(path, "")
	if err != nil || k.Generated || !k.Private.Equal(priv) || !k.Public.Equal(priv.Public()) ||
		k.KeyID != jwks.KeyID(k.Public) {
		t.Fatalf("from a file = %+v, %v; want the file's key under its derived kid", k, err)
	}
	if k, _ := LoadKey(path, "named"); k.KeyID != "named" {
		t.Errorf("a named kid read back as %q", k.KeyID)
	}
	for name, p := range map[string]string{"an absent file": filepath.Join(t.TempDir(), "absent.pem"), "no path": ""} {
		if _, err := LoadKey(p, ""); err == nil {
			t.Errorf("%s: LoadKey succeeded; it must fail, not fall back to a fresh key", name)
		}
	}
}

// The derived kid is the key's RFC 7638 thumbprint — the RFC 8037 §A.3
// example — so any JOSE tool computes the same one.
func TestDeriveKeyIDIsTheThumbprint(t *testing.T) {
	x, err := base64.RawURLEncoding.DecodeString("11qYAYKxCrfVS_7TyWQHOg7hcvPapiMlrwIaaPcHURo")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := jwks.KeyID(ed25519.PublicKey(x)), "kPrK_qmxVWaYVA9wwBF6Iuo3vVzz7TxHCTwXBygrS4k"; got != want {
		t.Errorf("jwks.KeyID = %s, want %s", got, want)
	}
	key, err := GenerateKey()
	if err != nil || key.KeyID != jwks.KeyID(key.Public) || !key.Generated {
		t.Errorf("GenerateKey = %+v, %v; want jwks.KeyID's kid, marked generated", key, err)
	}
}

func TestStaticKeyResolverRotation(t *testing.T) {
	ctx := context.Background()
	keyA, _ := GenerateKey()
	keyB, _ := GenerateKey()
	pubA, pubB := keyA.Public, keyB.Public
	r := jwks.NewStatic(map[string]ed25519.PublicKey{"a": pubA})

	r.SetKey("b", pubB)
	if keys := r.Keys(); len(keys) != 2 || !keys["b"].Equal(pubB) {
		t.Fatalf("Keys after SetKey = %v", keys)
	}
	keys := r.Keys()
	delete(keys, "a")
	if _, err := r.PublicKey(ctx, "a"); err != nil {
		t.Error("Keys must return a copy: deleting from it removed a key")
	}

	r.RemoveKey("a")
	if _, err := r.PublicKey(ctx, "a"); !errors.Is(err, jwks.ErrUnknownKID) {
		t.Errorf("a removed kid: err = %v, want jwks.ErrUnknownKID", err)
	}
	if got, err := r.PublicKey(ctx, "b"); err != nil || !got.Equal(pubB) {
		t.Errorf("the kid left = %v, %v", got, err)
	}
}

func TestNewEd25519Signer(t *testing.T) {
	key, err := GenerateKey()
	priv := key.Private
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewEd25519Signer("k1", priv)
	if err != nil || s.KeyID() != "k1" {
		t.Fatalf("NewEd25519Signer = %v, kid %q", err, s.KeyID())
	}
	if _, err := NewEd25519Signer("", priv); err == nil {
		t.Error("an empty key id was accepted")
	}
	if _, err := NewEd25519Signer("k1", priv[:10]); err == nil {
		t.Error("a short key was accepted")
	}
}
