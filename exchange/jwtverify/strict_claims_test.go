package jwtverify

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/lestrrat-go/jwx/v4/jwa"
	"github.com/lestrrat-go/jwx/v4/jws"

	"github.com/oleg-tkachuk/limes/exchange"
)

// jsonNull is a claim's value given as JSON null.
var jsonNull = json.RawMessage("null")

// blankClaim is a claim's value of white space alone.
const blankClaim = " \t "

// A token whose sub, jti or client id is null, empty or white space alone
// names no user, token or client, and is refused rather than read as a name.
func TestVerifierRefusesAnEmptySubjectOrID(t *testing.T) {
	p := newIDP(t)
	v := newVerifier(t, Config{Issuer: idpIssuer, Keys: staticKeys(t, p.keys), Now: clock})
	for name, change := range map[string]map[string]any{
		"sub null":        {claimSubject: jsonNull},
		"sub empty":       {claimSubject: ""},
		"sub blank":       {claimSubject: blankClaim},
		"jti null":        {claimJWTID: jsonNull},
		"jti empty":       {claimJWTID: ""},
		"jti blank":       {claimJWTID: blankClaim},
		"client id blank": {ClaimClientID: blankClaim},
	} {
		if s, err := v.VerifySubject(t.Context(), p.sign(t, TypeAccessToken, with(accessClaims(), change))); !errors.Is(err, exchange.ErrInvalidGrant) {
			t.Errorf("%s: subject %+v, err %v; want ErrInvalidGrant", name, s, err)
		}
	}
}

// A token whose payload is not a claims set, such as a second JWS signed by
// the same provider, is refused before it is verified.
func TestVerifierRefusesANestedJWS(t *testing.T) {
	p := newIDP(t)
	v := newVerifier(t, Config{Issuer: idpIssuer, Keys: staticKeys(t, p.keys), Now: clock})
	inner := p.sign(t, TypeAccessToken, accessClaims())
	headers := jws.NewHeaders()
	for name, value := range map[string]any{jws.TypeKey: TypeAccessToken, jws.KeyIDKey: p.kid} {
		if err := headers.Set(name, value); err != nil {
			t.Fatal(err)
		}
	}
	outer, err := jws.Sign([]byte(inner), jws.WithKey(jwa.EdDSAEd25519(), p.key, jws.WithProtectedHeaders(headers)))
	if err != nil {
		t.Fatal(err)
	}
	if s, err := v.VerifySubject(t.Context(), string(outer)); !errors.Is(err, exchange.ErrInvalidGrant) {
		t.Errorf("a JWS whose payload is a JWS: subject %+v, err %v; want ErrInvalidGrant", s, err)
	}
	if _, err := v.VerifySubject(t.Context(), inner); err != nil {
		t.Errorf("the inner token alone: %v", err)
	}
}

// A token in the JWS JSON serialization is refused as not a compact JWS,
// where the verifier says it is, not later by the claims parser.
func TestVerifierRefusesAJSONSerializedJWS(t *testing.T) {
	p := newIDP(t)
	v := newVerifier(t, Config{Issuer: idpIssuer, Keys: staticKeys(t, p.keys), Now: clock})
	payload, err := json.Marshal(accessClaims())
	if err != nil {
		t.Fatal(err)
	}
	headers := jws.NewHeaders()
	for name, value := range map[string]any{jws.TypeKey: TypeAccessToken, jws.KeyIDKey: p.kid} {
		if err := headers.Set(name, value); err != nil {
			t.Fatal(err)
		}
	}
	flattened, err := jws.Sign(payload, jws.WithJSON(), jws.WithKey(jwa.EdDSAEd25519(), p.key, jws.WithProtectedHeaders(headers)))
	if err != nil {
		t.Fatal(err)
	}
	_, err = v.VerifySubject(t.Context(), string(flattened))
	if !errors.Is(err, exchange.ErrInvalidGrant) || !strings.Contains(err.Error(), notCompact) {
		t.Errorf("a JSON-serialized JWS: err %v; want ErrInvalidGrant, %q", err, notCompact)
	}
}
