// Package exchangetest holds a SubjectVerifier to the contract the
// Exchanger relies on, for an implementation of your own: one for opaque
// tokens through introspection, or for a provider whose tokens are not RFC
// 9068's.
//
//	func TestMyVerifier(t *testing.T) {
//		exchangetest.TestSubjectVerifier(t, myVerifier, exchangetest.Tokens{
//			Valid:   func(t *testing.T) (string, exchange.Subject) { … },
//			Expired: func(t *testing.T) string { … },
//			…
//		})
//	}
package exchangetest

import (
	"errors"
	"slices"
	"testing"

	"github.com/oleg-tkachuk/limes/exchange"
)

// Tokens mints the tokens the suite presents. Valid is required; a refusal
// left nil is skipped, and says so, since a verifier that cannot be shown
// one of them may still refuse it.
type Tokens struct {
	// Valid is a token the verifier must accept, and the subject it must
	// read from it: issuer, subject, client, audience, expiry and binding.
	Valid func(t *testing.T) (token string, want exchange.Subject)
	// Expired is a token whose lifetime has ended.
	Expired func(t *testing.T) string
	// OtherIssuer is a token from an issuer the verifier does not trust.
	OtherIssuer func(t *testing.T) string
	// Forged is a token signed, or vouched for, by none of the issuer's keys.
	Forged func(t *testing.T) string
	// NoClient is a token that names no client.
	NoClient func(t *testing.T) string
	// Bound is a token bound to a DPoP key, and that key's thumbprint.
	Bound func(t *testing.T) (token, jkt string)
}

// TestSubjectVerifier holds v to what the Exchanger relies on: it reads a
// valid token as the issuer wrote it, and refuses everything else with
// exchange.ErrInvalidGrant, which the exchange answers as invalid_grant;
// any other error would be answered as the server's failure.
func TestSubjectVerifier(t *testing.T, v exchange.SubjectVerifier, tokens Tokens) {
	t.Helper()
	if tokens.Valid == nil {
		t.Fatal("exchangetest: Tokens.Valid required")
	}
	t.Run("reads a valid token", func(t *testing.T) {
		token, want := tokens.Valid(t)
		got, err := v.VerifySubject(t.Context(), token)
		if err != nil {
			t.Fatalf("VerifySubject: %v", err)
		}
		if got.Issuer != want.Issuer || got.Subject != want.Subject || got.ClientID != want.ClientID ||
			!slices.Equal(got.Audience, want.Audience) || !got.ExpiresAt.Equal(want.ExpiresAt) || got.JKT != want.JKT {
			t.Errorf("subject %+v, want %+v", got, want)
		}
	})
	if tokens.Bound != nil {
		t.Run("reads the binding", func(t *testing.T) {
			token, jkt := tokens.Bound(t)
			got, err := v.VerifySubject(t.Context(), token)
			if err != nil || got.JKT != jkt {
				t.Errorf("subject %+v, %v; want bound to %s", got, err, jkt)
			}
		})
	}
	refusals := map[string]func(t *testing.T) string{
		"refuses an expired token":         tokens.Expired,
		"refuses another issuer's token":   tokens.OtherIssuer,
		"refuses a forged token":           tokens.Forged,
		"refuses a token naming no client": tokens.NoClient,
		"refuses what is no token":         func(*testing.T) string { return "a.b.c" },
		"refuses an empty token":           func(*testing.T) string { return "" },
	}
	for name, mint := range refusals {
		t.Run(name, func(t *testing.T) {
			if mint == nil {
				t.Skip("exchangetest: no such token minted")
			}
			if _, err := v.VerifySubject(t.Context(), mint(t)); !errors.Is(err, exchange.ErrInvalidGrant) {
				t.Errorf("err = %v, want exchange.ErrInvalidGrant", err)
			}
		})
	}
}
