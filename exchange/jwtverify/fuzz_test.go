package jwtverify

import (
	"errors"
	"testing"

	"github.com/oleg-tkachuk/limes/exchange"
)

// FuzzVerifySubject hands the verifier whole tokens the fuzzer writes,
// mutated from ones the identity provider signed, and holds it to two
// answers: a token it cannot read or trust is exchange.ErrInvalidGrant,
// never a panic or the server's error; and what it accepts is a token the
// identity provider signed, byte for byte, for the subject it names.
func FuzzVerifySubject(f *testing.F) {
	p := newIDP(f)
	v, err := New(Config{Issuer: idpIssuer, Keys: staticKeys(f, p.keys), Now: clock})
	if err != nil {
		f.Fatal(err)
	}
	signed := map[string]bool{}
	for _, typ := range []string{TypeAccessToken, TypeAccessTokenMedia} {
		token := p.sign(f, typ, accessClaims())
		signed[token] = true
		f.Add(token)
	}
	f.Fuzz(func(t *testing.T, token string) {
		s, err := v.VerifySubject(t.Context(), token)
		if err != nil {
			if !errors.Is(err, exchange.ErrInvalidGrant) {
				t.Fatalf("token %q: %v, not ErrInvalidGrant", token, err)
			}
			return
		}
		if !signed[token] {
			t.Fatalf("verified a token the identity provider never signed: %q", token)
		}
		if s.Issuer != idpIssuer || s.Subject != userSubject {
			t.Fatalf("token %q verified as %+v", token, s)
		}
	})
}
