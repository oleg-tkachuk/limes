package limes

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"testing"
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

func TestMustLoadOrGenerate(t *testing.T) {
	priv, der := ed25519PKCS8(t)
	path := writePEM(t, "PRIVATE KEY", der)

	kid, pub, got, generated, err := MustLoadOrGenerate(path, "")
	if err != nil || generated || !got.Equal(priv) || !pub.Equal(priv.Public()) || kid == "" {
		t.Fatalf("from a file = %q, generated %v, %v; want the file's key under a derived kid", kid, generated, err)
	}
	if kid, _, _, _, _ := MustLoadOrGenerate(path, "named"); kid != "named" {
		t.Errorf("a named kid read back as %q", kid)
	}
	if _, _, _, _, err := MustLoadOrGenerate(filepath.Join(t.TempDir(), "absent.pem"), ""); err == nil {
		t.Error("a path that cannot be read must fail, not fall back to a fresh key")
	}

	kid, pub, got, generated, err = MustLoadOrGenerate("", "eph")
	if err != nil || !generated || kid != "eph" || len(got) != ed25519.PrivateKeySize || !pub.Equal(got.Public()) {
		t.Fatalf("without a path = %q, generated %v, %v; want a fresh key under the kid given", kid, generated, err)
	}
	if kid, _, _, _, _ := MustLoadOrGenerate("", ""); kid == "" {
		t.Error("a generated key must get a kid of its own")
	}
}

func TestStaticKeyResolverRotation(t *testing.T) {
	ctx := context.Background()
	_, pubA, _, _ := GenerateEd25519Keypair()
	_, pubB, _, _ := GenerateEd25519Keypair()
	r := NewStaticKeyResolver(map[string]ed25519.PublicKey{"a": pubA})

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
	if _, err := r.PublicKey(ctx, "a"); !errors.Is(err, ErrUnknownKID) {
		t.Errorf("a removed kid: err = %v, want ErrUnknownKID", err)
	}
	if got, err := r.PublicKey(ctx, "b"); err != nil || !got.Equal(pubB) {
		t.Errorf("the kid left = %v, %v", got, err)
	}
}

func TestNewEd25519Signer(t *testing.T) {
	_, _, priv, err := GenerateEd25519Keypair()
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
