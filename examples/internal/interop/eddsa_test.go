package interop

import (
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"testing"

	gojose "github.com/go-jose/go-jose/v4"
	gojosejwt "github.com/go-jose/go-jose/v4/jwt"
	gojwt "github.com/golang-jwt/jwt/v5"
	"github.com/lestrrat-go/jwx/v3/jwt"

	"github.com/oleg-tkachuk/limes"
)

// fetchGoJOSEKeys is the issuer's JWKS as go-jose reads it, fetched over
// HTTP: go-jose has no fetcher of its own.
func (f *fixture) fetchGoJOSEKeys(t *testing.T) gojose.JSONWebKeySet {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, f.jwksURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var set gojose.JSONWebKeySet
	if err := json.NewDecoder(resp.Body).Decode(&set); err != nil {
		t.Fatalf("go-jose reading the JWKS: %v", err)
	}
	return set
}

// goJOSEVerify verifies token as a resource server using go-jose does:
// the signature under keys, then iss, aud and exp.
func goJOSEVerify(token string, keys any, aud string) error {
	tok, err := gojosejwt.ParseSigned(token, []gojose.SignatureAlgorithm{gojose.EdDSA})
	if err != nil {
		return err
	}
	var claims gojosejwt.Claims
	if err := tok.Claims(keys, &claims); err != nil {
		return err
	}
	return claims.Validate(gojosejwt.Expected{Issuer: issuerName, AnyAudience: gojosejwt.Audience{aud}})
}

// A signer configured for EdDSA mints tokens go-jose verifies through the
// issuer's JWKS, which still names Ed25519. go-jose v4.1.6 knows the
// algorithm only as EdDSA and refuses a token naming Ed25519: once it reads
// that name, its users no longer need the EdDSA signer.
func TestGoJOSEVerifiesAnEdDSACapability(t *testing.T) {
	f := newFixtureNaming(t, limes.SigningAlgorithmEdDSA)
	token := f.issue(t, "").Token
	keys := f.fetchGoJOSEKeys(t)
	if err := goJOSEVerify(token, keys, audience); err != nil {
		t.Fatalf("go-jose refused an EdDSA capability: %v", err)
	}
	for name, refused := range map[string]error{
		"another audience":   goJOSEVerify(token, keys, otherAudience),
		"a tampered payload": goJOSEVerify(tamper(t, token), keys, audience),
		"another key":        goJOSEVerify(token, otherEd25519Key(t).Public(), audience),
	} {
		if refused == nil {
			t.Errorf("go-jose accepted the capability with %s", name)
		}
	}

	ed25519Token := newFixture(t).issue(t, "").Token
	if goJOSEVerify(ed25519Token, keys, audience) == nil {
		t.Error("go-jose reads a token naming Ed25519: the EdDSA signer is no longer needed for it")
	}
}

// golang-jwt verifies an EdDSA capability with the method it has, with no
// alias registered.
func TestGolangJWTVerifiesAnEdDSACapability(t *testing.T) {
	f := newFixtureNaming(t, limes.SigningAlgorithmEdDSA)
	token := f.issue(t, "").Token
	parse := func(pub ed25519.PublicKey, aud string) error {
		_, err := gojwt.Parse(token, func(*gojwt.Token) (any, error) { return pub, nil },
			gojwt.WithValidMethods([]string{gojwt.SigningMethodEdDSA.Alg()}), gojwt.WithIssuer(issuerName), gojwt.WithAudience(aud))
		return err
	}
	if err := parse(f.pub, audience); err != nil {
		t.Fatalf("golang-jwt refused an EdDSA capability: %v", err)
	}
	if parse(otherEd25519Key(t).Public().(ed25519.PublicKey), audience) == nil {
		t.Error("golang-jwt accepted the capability under another key")
	}
	if parse(f.pub, otherAudience) == nil {
		t.Error("golang-jwt accepted the capability for another audience")
	}
}

// jwx, which knows both names, verifies an EdDSA capability through the
// same JWKS.
func TestJWXVerifiesAnEdDSACapability(t *testing.T) {
	f := newFixtureNaming(t, limes.SigningAlgorithmEdDSA)
	if _, err := jwt.ParseString(f.issue(t, "").Token, jwt.WithKeySet(f.fetchKeys(t)), jwt.WithIssuer(issuerName), jwt.WithAudience(audience)); err != nil {
		t.Fatalf("jwx refused an EdDSA capability: %v", err)
	}
}
