package exchange

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
	"uuid"

	"github.com/oleg-tkachuk/limes"
)

// racingExchanges is how many exchanges of one subject token race in the
// concurrency check.
const racingExchanges = 8

// longSubjectTTL is a subject token's lifetime past DefaultTTL, so a
// capability that takes the token's own lifetime is told apart.
const longSubjectTTL = time.Hour

// Exchanged again, a subject token yields the capability its first
// exchange issued: the same token, recorded once.
func TestOneCapabilityPerSubjectToken(t *testing.T) {
	f := newFixture(t, fixtureConfig{onePerToken: true, noDPoP: true})
	token := f.idp.sign(t, typeAccessToken, accessClaims())
	first, err := f.ex.Exchange(t.Context(), request(token))
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.ex.Exchange(t.Context(), request(token))
	if err != nil {
		t.Fatal(err)
	}
	if first.Reissued || !second.Reissued {
		t.Errorf("Reissued = %v, %v; want false, then true", first.Reissued, second.Reissued)
	}
	if second.Token != first.Token || second.Capability.ID != first.Capability.ID {
		t.Errorf("the second exchange is capability %s; want the first's, %s, and its token", second.Capability.ID, first.Capability.ID)
	}
	if n := f.store.inserts.Load(); n != 1 {
		t.Errorf("%d capabilities recorded, want one", n)
	}
	other, err := f.ex.Exchange(t.Context(), request(f.idp.sign(t, typeAccessToken, accessClaims())))
	if err != nil || other.Capability.ID == first.Capability.ID || other.Reissued {
		t.Errorf("another subject token = %s, reissued %v, %v; want a capability of its own", other.Capability.ID, other.Reissued, err)
	}
}

// Exchanges racing on one subject token, as replicas do, converge on one
// capability and one token.
func TestRacingExchangesConverge(t *testing.T) {
	f := newFixture(t, fixtureConfig{onePerToken: true, noDPoP: true})
	token := f.idp.sign(t, typeAccessToken, accessClaims())
	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		tokens = map[string]int{}
	)
	for range racingExchanges {
		wg.Go(func() {
			res, err := f.ex.Exchange(t.Context(), request(token))
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				t.Errorf("a racing exchange: %v", err)
				return
			}
			tokens[res.Token]++
		})
	}
	wg.Wait()
	if len(tokens) != 1 {
		t.Errorf("racing exchanges handed out %d tokens, want one", len(tokens))
	}
}

// lostRace is an ExchangeStore that never finds the key and loses every
// claim to winner: the exchange that lost a race.
type lostRace struct{ winner uuid.UUID }

func (lostRace) ExchangedFor(context.Context, limes.ExchangeKey) (uuid.UUID, error) {
	return uuid.Nil(), limes.ErrNotFound
}

func (l lostRace) ClaimExchange(context.Context, limes.ExchangeKey, uuid.UUID) (uuid.UUID, bool, error) {
	return l.winner, false, nil
}

// An exchange that loses its claim hands out the winner's capability, not
// the one it issued.
func TestALostClaimHandsOutTheWinners(t *testing.T) {
	f := newFixture(t, fixtureConfig{noDPoP: true})
	winner, err := f.ex.Exchange(t.Context(), request(f.idp.sign(t, typeAccessToken, accessClaims())))
	if err != nil {
		t.Fatal(err)
	}
	f.ex.cfg.Exchanges = lostRace{winner: winner.Capability.ID}
	got, err := f.ex.Exchange(t.Context(), request(f.idp.sign(t, typeAccessToken, accessClaims())))
	if err != nil {
		t.Fatal(err)
	}
	if got.Capability.ID != winner.Capability.ID || got.Token != winner.Token || !got.Reissued {
		t.Errorf("the losing exchange = %s, reissued %v; want the winner's %s", got.Capability.ID, got.Reissued, winner.Capability.ID)
	}
}

// A revoked exchange stays revoked: exchanging its token again is refused,
// and issues nothing.
func TestARevokedExchangeIsNotIssuedAgain(t *testing.T) {
	f := newFixture(t, fixtureConfig{onePerToken: true, noDPoP: true})
	token := f.idp.sign(t, typeAccessToken, accessClaims())
	first, err := f.ex.Exchange(t.Context(), request(token))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Revoke(t.Context(), limes.RevokeRequest{CapabilityID: first.Capability.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ex.Exchange(t.Context(), request(token)); !errors.Is(err, ErrInvalidGrant) {
		t.Errorf("an exchange of a revoked capability's token: err = %v, want ErrInvalidGrant", err)
	}
	if n := f.store.inserts.Load(); n != 1 {
		t.Errorf("%d capabilities recorded, want only the revoked one", n)
	}
}

// A subject token without a jti cannot be told from another, so it is
// refused rather than issued a capability of its own.
func TestASubjectTokenWithoutAJTIIsRefused(t *testing.T) {
	f := newFixture(t, fixtureConfig{onePerToken: true, noDPoP: true})
	token := f.idp.sign(t, typeAccessToken, with(accessClaims(), map[string]any{claimJWTID: nil}))
	if _, err := f.ex.Exchange(t.Context(), request(token)); !errors.Is(err, ErrInvalidGrant) {
		t.Errorf("err = %v, want ErrInvalidGrant", err)
	}
}

// The capability is bound to the first exchange's key: an exchange that
// proves another key, or none, is refused, as is a bound one of a bearer
// capability.
func TestTheCapabilityKeepsItsBinding(t *testing.T) {
	f := newFixture(t, fixtureConfig{onePerToken: true})
	token := f.idp.sign(t, typeAccessToken, accessClaims())
	bound := request(token)
	bound.DPoP = proof(t, clientKey(t), "")
	if _, err := f.ex.Exchange(t.Context(), bound); err != nil {
		t.Fatal(err)
	}
	other := request(token)
	other.DPoP = proof(t, clientKey(t), "")
	if _, err := f.ex.Exchange(t.Context(), other); !errors.Is(err, ErrInvalidGrant) {
		t.Errorf("another key: err = %v, want ErrInvalidGrant", err)
	}
	if _, err := f.ex.Exchange(t.Context(), request(token)); !errors.Is(err, ErrInvalidGrant) {
		t.Errorf("no proof: err = %v, want ErrInvalidGrant", err)
	}
	bearerToken := f.idp.sign(t, typeAccessToken, accessClaims())
	if _, err := f.ex.Exchange(t.Context(), request(bearerToken)); err != nil {
		t.Fatal(err)
	}
	late := request(bearerToken)
	late.DPoP = proof(t, clientKey(t), "")
	if _, err := f.ex.Exchange(t.Context(), late); !errors.Is(err, ErrInvalidGrant) {
		t.Errorf("a proof for a bearer capability: err = %v, want ErrInvalidGrant", err)
	}
}

// An exchange asking for another audience than the capability's is
// refused: one subject token, one capability.
func TestAnotherAudienceIsRefused(t *testing.T) {
	f := newFixture(t, fixtureConfig{onePerToken: true, noDPoP: true})
	token := f.idp.sign(t, typeAccessToken, accessClaims())
	if _, err := f.ex.Exchange(t.Context(), request(token)); err != nil {
		t.Fatal(err)
	}
	elsewhere := request(token)
	elsewhere.Resources = []string{toolResource + "/other"}
	if _, err := f.ex.Exchange(t.Context(), elsewhere); !errors.Is(err, ErrInvalidTarget) {
		t.Errorf("err = %v, want ErrInvalidTarget", err)
	}
}

// A policy that names no lifetime gives the capability the subject token's
// own, not DefaultTTL: one capability per token must last as long as it.
func TestTheCapabilityLivesAsLongAsTheToken(t *testing.T) {
	claims := with(accessClaims(), map[string]any{claimExpiry: testNow.Add(longSubjectTTL).Unix()})
	for name, tc := range map[string]struct {
		onePerToken bool
		want        time.Duration
	}{
		"one per token": {onePerToken: true, want: longSubjectTTL},
		"one each time": {onePerToken: false, want: DefaultTTL},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, fixtureConfig{onePerToken: tc.onePerToken, noDPoP: true})
			res, err := f.ex.Exchange(t.Context(), request(f.idp.sign(t, typeAccessToken, claims)))
			if err != nil {
				t.Fatal(err)
			}
			if got := res.Capability.ExpiresAt.Sub(testNow); got != tc.want {
				t.Errorf("the capability lives %s, want %s", got, tc.want)
			}
		})
	}
}
