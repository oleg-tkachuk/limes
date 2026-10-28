package exchange

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"

	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwk"
)

// The kids of the keys the alg tests put beside the provider's.
const (
	kidHMAC   = "hmac-key"
	kidHS256  = "hs256-key"
	kidP384   = "p384-key"
	kidRSA256 = "rs256-key"
	kidNoAlg  = "ed25519-no-alg"
	// hmacSecretBytes is the length of the test's HMAC secret.
	hmacSecretBytes = 32
	// rsaTestBits is the size of the test's RSA key.
	rsaTestBits = 2048
)

// rawJWS is claims under header, signed over the signing input by sign:
// whatever header the test asks for, whatever the signature.
func rawJWS(t *testing.T, header, claims map[string]any, sign func([]byte) []byte) string {
	t.Helper()
	h, err := json.Marshal(header)
	if err != nil {
		t.Fatal(err)
	}
	c, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	input := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(c)
	return input + "." + base64.RawURLEncoding.EncodeToString(sign([]byte(input)))
}

// addKey adds raw to set under kid, naming alg when it is not empty.
func addKey(t *testing.T, set jwk.Set, raw any, kid string, alg jwa.KeyAlgorithm) {
	t.Helper()
	key, err := jwk.Import(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := key.Set(jwk.KeyIDKey, kid); err != nil {
		t.Fatal(err)
	}
	if alg != nil {
		if err := key.Set(jwk.AlgorithmKey, alg); err != nil {
			t.Fatal(err)
		}
	}
	if err := set.AddKey(key); err != nil {
		t.Fatal(err)
	}
}

// A token is verified under the algorithm its header names, and only with a
// key of that algorithm's kind: a header naming one algorithm over a
// signature made with a key of another kind, an HMAC secret above all, is
// refused, whatever the algorithm the key itself would verify with.
func TestJWTVerifierHoldsTheKeyToTheHeaderAlg(t *testing.T) {
	p := newIDP(t)
	secret := make([]byte, hmacSecretBytes)
	if _, err := rand.Read(secret); err != nil {
		t.Fatal(err)
	}
	p384, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	rsaKey, err := rsa.GenerateKey(rand.Reader, rsaTestBits)
	if err != nil {
		t.Fatal(err)
	}
	_, edNoAlg, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	addKey(t, p.keys, secret, kidHMAC, nil)
	addKey(t, p.keys, secret, kidHS256, jwa.HS256())
	addKey(t, p.keys, p384.Public(), kidP384, nil)
	addKey(t, p.keys, rsaKey.Public(), kidRSA256, jwa.RS256())
	addKey(t, p.keys, edNoAlg.Public(), kidNoAlg, nil)
	v := newJWTVerifier(t, JWTVerifierConfig{Issuer: idpIssuer, Keys: StaticKeys(p.keys), Now: clock})
	claims := accessClaims()
	header := func(alg, kid string) map[string]any {
		return map[string]any{"alg": alg, "kid": kid, "typ": TypeAccessToken}
	}
	hmacSHA256 := func(in []byte) []byte { m := hmac.New(sha256.New, secret); m.Write(in); return m.Sum(nil) }
	es256OnP384 := func(in []byte) []byte {
		d := sha256.Sum256(in)
		r, sv, err := ecdsa.Sign(rand.Reader, p384, d[:])
		if err != nil {
			t.Fatal(err)
		}
		size := (p384.Curve.Params().BitSize + 7) / 8
		return append(r.FillBytes(make([]byte, size)), sv.FillBytes(make([]byte, size))...)
	}
	ps256 := func(in []byte) []byte {
		d := sha256.Sum256(in)
		sig, err := rsa.SignPSS(rand.Reader, rsaKey, crypto.SHA256, d[:], &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash})
		if err != nil {
			t.Fatal(err)
		}
		return sig
	}
	for name, token := range map[string]string{
		"an HMAC signature headed RS256":       rawJWS(t, header("RS256", kidHMAC), claims, hmacSHA256),
		"an HMAC signature headed EdDSA":       rawJWS(t, header("EdDSA", kidHMAC), claims, hmacSHA256),
		"an Ed25519 signature headed ES256":    rawJWS(t, header("ES256", kidNoAlg), claims, func(in []byte) []byte { return ed25519.Sign(edNoAlg, in) }),
		"ES256 under a P-384 key":              rawJWS(t, header("ES256", kidP384), claims, es256OnP384),
		"PS256 under a key named for RS256":    rawJWS(t, header("PS256", kidRSA256), claims, ps256),
		"the provider's key under another alg": rawJWS(t, header("ES256", idpKID), claims, func(in []byte) []byte { return ed25519.Sign(p.key, in) }),
		"no kid":                               rawJWS(t, map[string]any{"alg": "Ed25519", "typ": TypeAccessToken}, claims, func(in []byte) []byte { return ed25519.Sign(p.key, in) }),
	} {
		if _, err := v.VerifySubject(t.Context(), token); !errors.Is(err, ErrInvalidGrant) {
			t.Errorf("%s: %v, want ErrInvalidGrant", name, err)
		}
	}
	if _, err := v.VerifySubject(t.Context(), rawJWS(t, header("Ed25519", kidNoAlg), claims, func(in []byte) []byte { return ed25519.Sign(edNoAlg, in) })); err != nil {
		t.Errorf("an Ed25519 key naming no alg, headed Ed25519: %v", err)
	}
}
