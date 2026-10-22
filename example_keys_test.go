package limes_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/jwks"
)

// The PEM block type a PKCS#8 private key is written under.
const pkcs8BlockType = "PRIVATE KEY"

func ExampleGenerateKey() {
	key, err := limes.GenerateKey()
	if err != nil {
		log.Fatal(err)
	}
	// The kid is derived from the public half, as a JWKS publishes it.
	fmt.Println(key.KeyID == jwks.KeyID(key.Public()))
	// The private half never reaches a log line.
	fmt.Println(key.Private)
	// Output:
	// true
	// [redacted]
}

func ExampleLoadKey() {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		log.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		log.Fatal(err)
	}
	dir, err := os.MkdirTemp("", "limes-key")
	if err != nil {
		log.Fatal(err)
	}
	err = func() error {
		defer os.RemoveAll(dir)
		path := filepath.Join(dir, "signing.pem")
		if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: pkcs8BlockType, Bytes: der}), 0o600); err != nil {
			return err
		}
		key, err := limes.LoadKey(path)
		if err != nil {
			return err
		}
		fmt.Println(key.Public().Equal(priv.Public()))

		// A missing file is an error, never a fresh key.
		_, err = limes.LoadKey(filepath.Join(dir, "missing.pem"))
		fmt.Println(err != nil)
		return nil
	}()
	if err != nil {
		log.Fatal(err)
	}
	// Output:
	// true
	// true
}

func ExampleSealKey() {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		log.Fatal(err)
	}
	sealed := limes.SealKey(priv)

	// No verb, no encoder and no slog handler prints the key.
	asJSON, _ := json.Marshal(struct{ Key limes.SealedKey }{sealed})
	fmt.Printf("%v %x %#v %s\n", sealed, sealed, sealed, asJSON)
	fmt.Println(slog.AnyValue(sealed))

	// It still signs, as a crypto.Signer.
	sig, err := sealed.Sign(rand.Reader, []byte("message"), &ed25519.Options{})
	fmt.Println(err, ed25519.Verify(priv.Public().(ed25519.PublicKey), []byte("message"), sig))
	fmt.Println(sealed.IsZero(), limes.SealedKey{}.IsZero())
	// Output:
	// [redacted] [redacted] [redacted] {"Key":"[redacted]"}
	// [redacted]
	// <nil> true
	// false true
}

func ExampleNewSigner() {
	key, err := limes.GenerateKey()
	if err != nil {
		log.Fatal(err)
	}
	signer, err := limes.NewSigner(limes.SignerConfig{KeyID: key.KeyID, Key: key.Private})
	fmt.Println(err, signer != nil)

	// A signer needs a kid for verifiers to find its key by.
	_, err = limes.NewSigner(limes.SignerConfig{Key: key.Private})
	fmt.Println(err)
	// Output:
	// <nil> true
	// limes: SignerConfig.KeyID required
}

func ExampleSignerConfig_LogValue() {
	key, err := limes.GenerateKey()
	if err != nil {
		log.Fatal(err)
	}
	cfg := limes.SignerConfig{KeyID: "k1", Key: key.Private}
	asJSON, _ := json.Marshal(cfg)
	fmt.Println(string(asJSON))

	// The time is dropped only to keep the output stable.
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		},
	}))
	logger.Info("signer configured", "signer", cfg)
	// Output:
	// {"KeyID":"k1","Key":"[redacted]"}
	// level=INFO msg="signer configured" signer="{KeyID:k1 Key:[redacted]}"
}
