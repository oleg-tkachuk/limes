package interop

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jws"
	"github.com/lestrrat-go/jwx/v3/jwt"

	"github.com/oleg-tkachuk/limes"
)

// RFC 9068 (JWT profile for OAuth 2.0 access tokens): the types §2.1 allows
// in the header, and the claims §2.2 requires.
const (
	typAccessToken      = "at+jwt"
	typAccessTokenMedia = "application/at+jwt"

	claimIssuer    = "iss"
	claimExpiry    = "exp"
	claimAudience  = "aud"
	claimSubject   = "sub"
	claimClientID  = "client_id"
	claimIssuedAt  = "iat"
	claimJWTID     = "jti"
	headerType     = "typ"
	accessClientID = "interop-client"
)

// rfc9068Claims are the claims RFC 9068 §2.2 requires of an access token.
var rfc9068Claims = []string{claimIssuer, claimExpiry, claimAudience, claimSubject, claimClientID, claimIssuedAt, claimJWTID}

// rfc9068Faults is what a resource server validating an RFC 9068 access token
// (§4) finds wrong with token, verified by jwx under set: its
// type, then each required claim it lacks; none for a valid access token.
func rfc9068Faults(t *testing.T, token string, set jwk.Set) []string {
	t.Helper()
	msg, err := jws.ParseString(token)
	if err != nil {
		t.Fatal(err)
	}
	var faults []string
	if typ, _ := msg.Signatures()[0].ProtectedHeaders().Type(); !strings.EqualFold(typ, typAccessToken) && !strings.EqualFold(typ, typAccessTokenMedia) {
		faults = append(faults, headerType)
	}
	tok, err := jwt.ParseString(token, jwt.WithKeySet(set), jwt.WithIssuer(issuerName), jwt.WithAudience(audience))
	if err != nil {
		t.Fatalf("jwx refused the token's signature or standard claims: %v", err)
	}
	for _, claim := range rfc9068Claims {
		if !tok.Has(claim) {
			faults = append(faults, claim)
		}
	}
	return faults
}

// accessTokenClaims is an RFC 9068 access token's payload, with nothing of
// limes's in it.
func accessTokenClaims() map[string]any {
	now := time.Now()
	return map[string]any{
		claimIssuer: issuerName, claimAudience: audience, claimSubject: readerSubject,
		claimClientID: accessClientID, claimIssuedAt: now.Unix(), claimExpiry: now.Add(tokenTTL).Unix(),
		claimJWTID: uuid.New().String(),
	}
}

// signAs is payload signed with the issuer's key by jwx, under typ.
func (f *fixture) signAs(t *testing.T, typ string, payload []byte) string {
	t.Helper()
	headers := jws.NewHeaders()
	for name, value := range map[string]any{jws.TypeKey: typ, jws.KeyIDKey: f.kid} {
		if err := headers.Set(name, value); err != nil {
			t.Fatal(err)
		}
	}
	token, err := jws.Sign(payload, jws.WithKey(jwa.EdDSAEd25519(), f.key, jws.WithProtectedHeaders(headers)))
	if err != nil {
		t.Fatalf("jwx signing a token: %v", err)
	}
	return string(token)
}

// The validator below accepts an RFC 9068 access token, so what it finds in
// a capability is the capability's, not the validator's.
func TestRFC9068ValidatorAcceptsAnAccessToken(t *testing.T) {
	f := newFixture(t)
	payload, err := json.Marshal(accessTokenClaims())
	if err != nil {
		t.Fatal(err)
	}
	for _, typ := range []string{typAccessToken, typAccessTokenMedia} {
		if faults := rfc9068Faults(t, f.signAs(t, typ, payload), f.fetchKeys(t)); len(faults) != 0 {
			t.Errorf("an access token typed %s: faults %q", typ, faults)
		}
	}
}

// A capability is not an RFC 9068 access token, and differs from one in two
// things alone: its typ, which names the capability format, and the
// client_id it has no use for. Every other claim RFC 9068 requires it
// carries. A format that closes the gap changes this list.
func TestACapabilityIsNotAnRFC9068AccessToken(t *testing.T) {
	f := newFixture(t)
	faults := rfc9068Faults(t, f.issue(t, "").Token, f.fetchKeys(t))
	if want := []string{headerType, claimClientID}; !slices.Equal(faults, want) {
		t.Errorf("a capability's faults as an RFC 9068 access token: %q, want %q", faults, want)
	}
}

// An access token signed with the issuer's own key does not pass as a
// capability (RFC 8725 §3.11): limes reads the type before any claim.
// The payload is a capability's own, so the type is all that differs; the
// same payload under the capability type verifies.
func TestLimesRefusesAnAccessToken(t *testing.T) {
	f := newFixture(t)
	token := f.issue(t, "").Token
	payload, err := base64.RawURLEncoding.DecodeString(strings.Split(token, ".")[1])
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		typ      string
		accepted bool
	}{
		{string(limes.TokenTypeV6), true},
		{typAccessToken, false},
		{typAccessTokenMedia, false},
	} {
		_, err := f.verifier.Verify(t.Context(), limes.VerifyRequest{Token: f.signAs(t, tc.typ, payload), Audience: audience})
		switch {
		case tc.accepted && err != nil:
			t.Errorf("typ %s: %v", tc.typ, err)
		case !tc.accepted && !errors.Is(err, limes.ErrInvalidToken):
			t.Errorf("typ %s: %v, want %v", tc.typ, err, limes.ErrInvalidToken)
		}
	}
}

// Nor does an RFC 9068 access token with every claim it requires, even under
// the capability type: it has none of a capability's own claims, and a
// client_id the capability schema does not define.
func TestLimesRefusesAnAccessTokenPayload(t *testing.T) {
	f := newFixture(t)
	payload, err := json.Marshal(accessTokenClaims())
	if err != nil {
		t.Fatal(err)
	}
	for _, typ := range []string{string(limes.TokenTypeV6), typAccessToken} {
		_, err := f.verifier.Verify(t.Context(), limes.VerifyRequest{Token: f.signAs(t, typ, payload), Audience: audience})
		if !errors.Is(err, limes.ErrInvalidToken) {
			t.Errorf("an access token payload typed %s: %v", typ, err)
		}
	}
}
