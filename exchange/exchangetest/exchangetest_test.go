package exchangetest_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"testing"
	"time"
	"uuid"

	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jws"

	"github.com/oleg-tkachuk/limes/exchange"
	"github.com/oleg-tkachuk/limes/exchange/exchangetest"
	"github.com/oleg-tkachuk/limes/exchange/jwtverify"
)

const (
	issuer   = "https://idp.example.com"
	kid      = "idp-key-1"
	client   = "mcp-client"
	audience = "https://mcp.example.com"
	subject  = "user-1"
	lifetime = 5 * time.Minute
	// jkt is a DPoP key's thumbprint the bound token names.
	jkt = "0ZcOCORZNYy-DWpqq30jZyJGHTN0d2HglBV3uiguA4I"
)

var now = time.Unix(1_800_000_000, 0)

// provider is an identity provider: its key, and its public key set.
type provider struct {
	key  ed25519.PrivateKey
	keys jwk.Set
}

func newProvider(t *testing.T) provider {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := jwk.Import(key.Public())
	if err != nil {
		t.Fatal(err)
	}
	if err := pub.Set(jwk.KeyIDKey, kid); err != nil {
		t.Fatal(err)
	}
	if err := pub.Set(jwk.AlgorithmKey, jwa.EdDSAEd25519()); err != nil {
		t.Fatal(err)
	}
	set := jwk.NewSet()
	if err := set.AddKey(pub); err != nil {
		t.Fatal(err)
	}
	return provider{key: key, keys: set}
}

// sign is an RFC 9068 access token with claims changed by changes, a nil
// value removing a claim.
func (p provider) sign(t *testing.T, changes map[string]any) string {
	t.Helper()
	claims := map[string]any{
		"iss": issuer, "sub": subject, "aud": audience, "client_id": client,
		"iat": now.Unix(), "exp": now.Add(lifetime).Unix(), "jti": uuid.New().String(),
	}
	for k, v := range changes {
		if v == nil {
			delete(claims, k)
		} else {
			claims[k] = v
		}
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	headers := jws.NewHeaders()
	if err := headers.Set(jws.TypeKey, jwtverify.TypeAccessToken); err != nil {
		t.Fatal(err)
	}
	if err := headers.Set(jws.KeyIDKey, kid); err != nil {
		t.Fatal(err)
	}
	token, err := jws.Sign(payload, jws.WithKey(jwa.EdDSAEd25519(), p.key, jws.WithProtectedHeaders(headers)))
	if err != nil {
		t.Fatal(err)
	}
	return string(token)
}

// The module's own JWTVerifier keeps the contract it sets for others.
func TestJWTVerifierKeepsTheContract(t *testing.T) {
	p, other := newProvider(t), newProvider(t)
	v, err := jwtverify.New(jwtverify.Config{
		Issuer: issuer, Keys: jwtverify.StaticKeys(p.keys), Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	exchangetest.TestSubjectVerifier(t, v, exchangetest.Tokens{
		Valid: func(t *testing.T) (string, exchange.Subject) {
			return p.sign(t, nil), exchange.Subject{
				Issuer: issuer, Subject: subject, ClientID: client, Audience: []string{audience}, ExpiresAt: now.Add(lifetime),
			}
		},
		Expired:     func(t *testing.T) string { return p.sign(t, map[string]any{"exp": now.Add(-lifetime).Unix()}) },
		OtherIssuer: func(t *testing.T) string { return p.sign(t, map[string]any{"iss": "https://other.example.com"}) },
		Forged:      func(t *testing.T) string { return other.sign(t, nil) },
		NoClient:    func(t *testing.T) string { return p.sign(t, map[string]any{"client_id": nil}) },
		Bound: func(t *testing.T) (string, string) {
			return p.sign(t, map[string]any{"cnf": map[string]any{"jkt": jkt}}), jkt
		},
	})
}
