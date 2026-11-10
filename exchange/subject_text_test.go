package exchange

import (
	"errors"
	"testing"
)

// A subject token whose id or subject holds text no store can keep — a NUL,
// which a JSON string carries and a store's text cannot — is an invalid
// grant, refused before it is claimed or a capability minted for it; the
// same token with plain text exchanges.
func TestExchangeRefusesSubjectTextNoStoreCanKeep(t *testing.T) {
	f := newFixture(t, fixtureConfig{onePerToken: true})
	if _, err := f.ex.Exchange(t.Context(), request(f.idp.sign(t, typeAccessToken, accessClaims()))); err != nil {
		t.Fatalf("plain text: %v", err)
	}
	for _, claim := range []string{claimJWTID, claimSubject, claimClientID} {
		token := f.idp.sign(t, typeAccessToken, with(accessClaims(), map[string]any{claim: "a\x00b"}))
		if _, err := f.ex.Exchange(t.Context(), request(token)); !errors.Is(err, ErrInvalidGrant) {
			t.Errorf("a NUL in %s: %v, want ErrInvalidGrant", claim, err)
		}
	}
}
