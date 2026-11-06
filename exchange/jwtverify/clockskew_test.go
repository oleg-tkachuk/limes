package jwtverify

import (
	"errors"
	"testing"
	"time"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/exchange"
)

// withinSkew is how long past its expiry a token is checked: inside
// limes.DefaultLeeway, so only a verifier holding it to the clock exactly
// refuses it.
const withinSkew = limes.DefaultLeeway / 2

// A token's times are checked with limes.DefaultLeeway's skew unless the
// config names another, or NoClockSkew holds them to the clock exactly.
func TestVerifierClockSkew(t *testing.T) {
	p := newIDP(t)
	token := p.sign(t, TypeAccessToken, with(accessClaims(), map[string]any{claimExpiry: testNow.Add(-withinSkew).Unix()}))
	for name, tc := range map[string]struct {
		skew   time.Duration
		exact  bool
		accept bool
	}{
		"the default":  {accept: true},
		"a narrower":   {skew: withinSkew / 2},
		"none at all":  {exact: true},
		"a wider skew": {skew: 2 * withinSkew, accept: true},
	} {
		v := newVerifier(t, Config{Issuer: idpIssuer, Keys: staticKeys(t, p.keys), Now: clock, ClockSkew: tc.skew, NoClockSkew: tc.exact})
		_, err := v.VerifySubject(t.Context(), token)
		if tc.accept != (err == nil) || (err != nil && !errors.Is(err, exchange.ErrInvalidGrant)) {
			t.Errorf("%s: err = %v, want accepted %v", name, err, tc.accept)
		}
	}
}

// A negative skew, or one beside NoClockSkew, is refused as a configuration
// error.
func TestVerifierRefusesASkewItCannotTake(t *testing.T) {
	p := newIDP(t)
	for name, cfg := range map[string]Config{
		"negative":           {ClockSkew: -time.Second},
		"beside NoClockSkew": {ClockSkew: time.Second, NoClockSkew: true},
	} {
		cfg.Issuer, cfg.Keys = idpIssuer, staticKeys(t, p.keys)
		if _, err := New(cfg); !errors.Is(err, limes.ErrInvalidConfig) {
			t.Errorf("%s: err = %v, want ErrInvalidConfig", name, err)
		}
	}
}
