package jose

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"testing"
)

// RFC 8037 Appendix A.3: the thumbprint of the Ed25519 key of Appendix A.2.
const (
	rfc8037X          = "11qYAYKxCrfVS_7TyWQHOg7hcvPapiMlrwIaaPcHURo"
	rfc8037Thumbprint = "kPrK_qmxVWaYVA9wwBF6Iuo3vVzz7TxHCTwXBygrS4k"
)

func TestThumbprintMatchesRFC8037(t *testing.T) {
	got, err := JWK{Kty: KtyOKP, Crv: CrvEd25519, X: rfc8037X}.Thumbprint()
	if err != nil || got != rfc8037Thumbprint {
		t.Fatalf("Thumbprint = %q, %v; want %q", got, err, rfc8037Thumbprint)
	}
	raw, err := base64.RawURLEncoding.DecodeString(rfc8037X)
	if err != nil {
		t.Fatal(err)
	}
	if got := Ed25519Thumbprint(ed25519.PublicKey(raw)); got != rfc8037Thumbprint {
		t.Errorf("Ed25519Thumbprint = %q, want %q", got, rfc8037Thumbprint)
	}
}

// A key written as a JWK reads back as the same key, for both kinds.
func TestPublicJWKRoundTrips(t *testing.T) {
	edPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	for name, pub := range map[string]interface{ Equal(x crypto.PublicKey) bool }{
		"Ed25519": edPub, "P-256": &ecKey.PublicKey,
	} {
		j, err := PublicJWK(pub)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		back, err := j.PublicKey()
		if err != nil || !pub.Equal(back) {
			t.Errorf("%s: read back %v, %v", name, back, err)
		}
	}
	p384, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := PublicJWK(&p384.PublicKey); err == nil {
		t.Error("PublicJWK accepted a P-384 key")
	}
}

func TestSplit(t *testing.T) {
	p, err := Split("h.p.s")
	if err != nil || p != (Parts{Header: "h", Payload: "p", Signature: "s"}) || p.SigningInput() != "h.p" {
		t.Fatalf("Split = %+v, %v", p, err)
	}
	for _, bad := range []string{"", "h", "h.p", "h.p.s.x"} {
		if _, err := Split(bad); err == nil {
			t.Errorf("Split(%q) accepted", bad)
		}
	}
}
