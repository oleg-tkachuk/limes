package jwtverify

import (
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"maps"
	"testing"
	"time"
	"uuid"

	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jws"
)

const (
	// The identity provider, its signing key's kid, and the client its
	// tokens are issued to.
	idpIssuer = "https://idp.example.com"
	idpKID    = "idp-key-1"
	idpClient = "mcp-client"
	// mcpServer is the audience of the tokens.
	mcpServer = "https://mcp.example.com"
	// userSubject is the user the tokens name.
	userSubject = "user-1"
	// subjectTTL is a token's lifetime.
	subjectTTL = 5 * time.Minute
	// jsonContentType is a JWKS document's media type.
	jsonContentType = "application/json"
)

// testNow is the fixed clock of every test.
var testNow = time.Unix(1_800_000_000, 0)

func clock() time.Time { return testNow }

// idp is an identity provider: a key and the key set it publishes.
type idp struct {
	key  ed25519.PrivateKey
	keys jwk.Set
}

func newIDP(t testing.TB) *idp {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := jwk.Import(key.Public())
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]any{jwk.KeyIDKey: idpKID, jwk.AlgorithmKey: jwa.EdDSAEd25519()} {
		if err := pub.Set(name, value); err != nil {
			t.Fatal(err)
		}
	}
	set := jwk.NewSet()
	if err := set.AddKey(pub); err != nil {
		t.Fatal(err)
	}
	return &idp{key: key, keys: set}
}

// accessClaims are an RFC 9068 access token's claims, issued at testNow.
func accessClaims() map[string]any {
	return map[string]any{
		claimIssuer: idpIssuer, claimSubject: userSubject, claimAudience: mcpServer, ClaimClientID: idpClient,
		claimIssuedAt: testNow.Unix(), claimExpiry: testNow.Add(subjectTTL).Unix(), claimJWTID: uuid.New().String(),
		claimScope: "tools:read tools:write",
	}
}

// sign is claims signed by the identity provider under typ.
func (p *idp) sign(t testing.TB, typ string, claims map[string]any) string {
	t.Helper()
	return signWith(t, p.key, jwa.EdDSAEd25519(), typ, claims)
}

// signWith is claims signed by key under alg and typ, with the provider's kid.
func signWith(t testing.TB, key crypto.Signer, alg jwa.SignatureAlgorithm, typ string, claims map[string]any) string {
	t.Helper()
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	headers := jws.NewHeaders()
	for name, value := range map[string]any{jws.TypeKey: typ, jws.KeyIDKey: idpKID} {
		if err := headers.Set(name, value); err != nil {
			t.Fatal(err)
		}
	}
	token, err := jws.Sign(payload, jws.WithKey(alg, key, jws.WithProtectedHeaders(headers)))
	if err != nil {
		t.Fatal(err)
	}
	return string(token)
}

// with is claims with changes applied: a nil value removes the claim.
func with(claims map[string]any, changes map[string]any) map[string]any {
	out := maps.Clone(claims)
	for k, v := range changes {
		if v == nil {
			delete(out, k)
		} else {
			out[k] = v
		}
	}
	return out
}
