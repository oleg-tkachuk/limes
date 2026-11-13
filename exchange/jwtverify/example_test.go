package jwtverify_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/lestrrat-go/jwx/v4/jwa"
	"github.com/lestrrat-go/jwx/v4/jwk"
	"github.com/lestrrat-go/jwx/v4/jws"

	"github.com/oleg-tkachuk/limes/exchange"
	"github.com/oleg-tkachuk/limes/exchange/jwtverify"
)

const (
	// exampleIssuer is the identity provider, and exampleKID its signing
	// key's kid.
	exampleIssuer = "https://idp.example.com"
	exampleKID    = "idp-key-1"
	// exampleLifetime is the access token's lifetime.
	exampleLifetime = 5 * time.Minute
)

// exampleNow is the example's fixed clock.
var exampleNow = time.Date(2026, time.March, 2, 9, 0, 0, 0, time.UTC)

// exampleIDP stands in for the identity provider: its JWK Set document, and
// an RFC 9068 access token it signed with claims.
func exampleIDP(claims map[string]any) (jwksDoc []byte, token string) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		log.Fatal(err)
	}
	pub, err := jwk.Import[jwk.Key](key.Public())
	if err != nil {
		log.Fatal(err)
	}
	if err := pub.Set(jwk.KeyIDKey, exampleKID); err != nil {
		log.Fatal(err)
	}
	set := jwk.NewSet()
	if err := set.AddKey(pub); err != nil {
		log.Fatal(err)
	}
	if jwksDoc, err = json.Marshal(set); err != nil {
		log.Fatal(err)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		log.Fatal(err)
	}
	headers := jws.NewHeaders()
	if err := headers.Set(jws.TypeKey, jwtverify.TypeAccessToken); err != nil {
		log.Fatal(err)
	}
	if err := headers.Set(jws.KeyIDKey, exampleKID); err != nil {
		log.Fatal(err)
	}
	signed, err := jws.Sign(payload, jws.WithKey(jwa.EdDSAEd25519(), key, jws.WithProtectedHeaders(headers)))
	if err != nil {
		log.Fatal(err)
	}
	return jwksDoc, string(signed)
}

// A Verifier is the exchange.SubjectVerifier for JWT access tokens. In
// production its keys come from the provider's JWKS URL, through RemoteKeys;
// a fixed set stands in here.
func ExampleNew() {
	ctx := context.Background()
	doc, token := exampleIDP(map[string]any{
		"iss": exampleIssuer, "sub": "user-1", "aud": "https://mcp.example.com", "client_id": "mcp-client",
		"iat": exampleNow.Unix(), "exp": exampleNow.Add(exampleLifetime).Unix(), "jti": "token-1",
		"scope": "tools:read tools:write",
	})
	keys, err := jwtverify.StaticKeys(doc)
	if err != nil {
		log.Fatal(err)
	}
	v, err := jwtverify.New(jwtverify.Config{
		Issuer: exampleIssuer, Keys: keys,
		Now: func() time.Time { return exampleNow },
	})
	if err != nil {
		log.Fatal(err)
	}

	s, err := v.VerifySubject(ctx, token)
	fmt.Println(err, s.Subject, s.ClientID, s.Audience, s.Scopes, s.ExpiresAt.Sub(exampleNow))

	// A tampered token is refused with ErrInvalidGrant: the client's fault.
	_, err = v.VerifySubject(ctx, token+"x")
	fmt.Println(errors.Is(err, exchange.ErrInvalidGrant))
	// Output:
	// <nil> user-1 mcp-client [https://mcp.example.com] [tools:read tools:write] 5m0s
	// true
}
