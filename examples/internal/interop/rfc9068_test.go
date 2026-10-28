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
	"github.com/oleg-tkachuk/limes/jwks"
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

// retiredCapabilityType is the typ of the capability format retired before
// 1.0, which limes no longer reads.
const retiredCapabilityType = "limes-cap-v6+jwt"

// signAs is payload signed with the issuer's key by jwx, under typ.
func (f *fixture) signAs(t *testing.T, typ string, payload []byte) string {
	t.Helper()
	return f.signUnder(t, typ, f.kid, payload)
}

// signUnder is payload signed with the issuer's key by jwx, under typ and
// kid.
func (f *fixture) signUnder(t *testing.T, typ, kid string, payload []byte) string {
	t.Helper()
	headers := jws.NewHeaders()
	for name, value := range map[string]any{jws.TypeKey: typ, jws.KeyIDKey: kid} {
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

// A capability is an RFC 9068 access token: a validator of the profile finds
// nothing wrong with it, and a limes verifier reads it.
func TestACapabilityIsAnRFC9068AccessToken(t *testing.T) {
	f := newFixture(t)
	res := f.issue(t, "")
	if faults := rfc9068Faults(t, res.Token, f.fetchKeys(t)); len(faults) != 0 {
		t.Errorf("a capability's faults as an RFC 9068 access token: %q", faults)
	}
	got, err := f.verifier.Verify(t.Context(), limes.VerifyRequest{Token: res.Token, Audience: audience})
	if err != nil || got.Capability().ClientID != interopClient {
		t.Errorf("Verify = client %q, %v; want %q", got.Capability().ClientID, err, interopClient)
	}
}

// The subjects of a capability acting on a user's behalf (RFC 8693 §4.1):
// the user, the agent the capability is issued to, and the sub-agent it is
// delegated to.
const (
	claimActor    = "act"
	actingUser    = "user-1"
	actingAgent   = "agent-planner"
	actingSubject = "agent-searcher"
)

// A capability acting for a user, delegated on to a sub-agent, is read by
// jwx as RFC 8693 and RFC 9068 describe it: sub the user, act the sub-agent,
// the agent nested within as the prior actor; and it is still an RFC 9068
// access token.
func TestJWXReadsWhomACapabilityActsFor(t *testing.T) {
	f := newFixture(t)
	user := limes.Principal{Type: limes.PrincipalTypeUser, TenantID: f.tenant, Subject: actingUser}
	parent, err := f.issuer.Issue(t.Context(), limes.IssueRequest{
		ClientID: interopClient, OnBehalfOf: &user,
		IssuedBy: limes.Principal{Type: limes.PrincipalTypeUser, TenantID: f.tenant, Subject: operatorSubject},
		Subject:  limes.Principal{Type: limes.PrincipalTypeAgent, TenantID: f.tenant, Subject: actingAgent},
		Audience: []string{audience}, TTL: tokenTTL, Caveats: limes.Caveats{Requests: limes.RequestCeiling{Unlimited: true}, Ops: []limes.Op{limes.OpGet, limes.OpShare}},
	})
	if err != nil {
		t.Fatal(err)
	}
	verified, err := f.verifier.Verify(t.Context(), limes.VerifyRequest{Token: parent.Token, Audience: audience})
	if err != nil {
		t.Fatal(err)
	}
	child, err := f.issuer.Delegate(t.Context(), limes.DelegateRequest{
		Parent: verified, Subject: limes.Principal{Type: limes.PrincipalTypeAgent, Subject: actingSubject}, InheritCaveats: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	set := f.fetchKeys(t)
	if faults := rfc9068Faults(t, child.Token, set); len(faults) != 0 {
		t.Errorf("the delegated capability's faults as an RFC 9068 access token: %q", faults)
	}
	tok, err := jwt.ParseString(child.Token, jwt.WithKeySet(set), jwt.WithIssuer(issuerName), jwt.WithAudience(audience))
	if err != nil {
		t.Fatal(err)
	}
	var act map[string]any
	if err := tok.Get(claimActor, &act); err != nil {
		t.Fatalf("jwx reads no act: %v", err)
	}
	var chain []string
	for at := act; at != nil; at, _ = at[claimActor].(map[string]any) {
		sub, _ := at[claimSubject].(string)
		chain = append(chain, sub)
	}
	if sub, _ := tok.Subject(); sub != actingUser || !slices.Equal(chain, []string{actingSubject, actingAgent}) {
		t.Errorf("jwx reads sub %q, act %v; want %s acted for by %s after %s", sub, chain, actingUser, actingSubject, actingAgent)
	}
}

// A capability's own payload, signed by jwx with the issuer's key, passes
// only typed at+jwt and under a limes kid: under another type, a retired one
// among them, or a kid outside limes's namespace, it is refused.
func TestLimesRefusesAnAccessToken(t *testing.T) {
	f := newFixture(t)
	token := f.issue(t, "").Token
	payload, err := base64.RawURLEncoding.DecodeString(strings.Split(token, ".")[1])
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		typ, kid string
		accepted bool
	}{
		{typAccessToken, f.kid, true},
		{typAccessTokenMedia, f.kid, false},
		{retiredCapabilityType, f.kid, false},
		{typAccessToken, strings.TrimPrefix(f.kid, jwks.KeyIDPrefix), false},
	} {
		_, err := f.verifier.Verify(t.Context(), limes.VerifyRequest{Token: f.signUnder(t, tc.typ, tc.kid, payload), Audience: audience})
		switch {
		case tc.accepted && err != nil:
			t.Errorf("typ %s under %s: %v", tc.typ, tc.kid, err)
		case !tc.accepted && !errors.Is(err, limes.ErrInvalidToken):
			t.Errorf("typ %s under %s: %v, want %v", tc.typ, tc.kid, err, limes.ErrInvalidToken)
		}
	}
}

// Nor does an RFC 9068 access token with every claim it requires, signed by
// the issuer's key under its kid: it has none of a capability's own claims.
func TestLimesRefusesAnAccessTokenPayload(t *testing.T) {
	f := newFixture(t)
	payload, err := json.Marshal(accessTokenClaims())
	if err != nil {
		t.Fatal(err)
	}
	for _, typ := range []string{typAccessToken, typAccessTokenMedia} {
		_, err := f.verifier.Verify(t.Context(), limes.VerifyRequest{Token: f.signAs(t, typ, payload), Audience: audience})
		if !errors.Is(err, limes.ErrInvalidToken) {
			t.Errorf("an access token payload typed %s: %v", typ, err)
		}
	}
}
