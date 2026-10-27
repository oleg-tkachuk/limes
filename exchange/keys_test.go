package exchange

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lestrrat-go/httprc/v3"
	"github.com/lestrrat-go/jwx/v3/jwk"
)

// errKeysDown is a KeySource that cannot reach the identity provider.
var errKeysDown = errors.New("JWKS endpoint unreachable")

type downKeys struct{}

func (downKeys) Keys(context.Context) (jwk.Set, error) { return nil, errKeysDown }

// Keys that cannot be had are the server's failure: the exchange answers it
// as one, and does not tell the client its token was refused.
func TestKeysOutageIsTheServers(t *testing.T) {
	f := newFixture(t, fixtureConfig{})
	v := newJWTVerifier(t, JWTVerifierConfig{Issuer: idpIssuer, Keys: downKeys{}, Now: clock})
	token := f.idp.sign(t, TypeAccessToken, accessClaims())
	if _, err := v.VerifySubject(t.Context(), token); !errors.Is(err, errKeysDown) || errors.Is(err, ErrInvalidGrant) {
		t.Errorf("err = %v, want the outage and no ErrInvalidGrant", err)
	}
	f.ex.cfg.Subjects = v
	if _, err := f.ex.Exchange(t.Context(), request(token)); isRefusal(err) {
		t.Errorf("err = %v, a refusal of the client", err)
	}
	if w := post(t, newHandler(t, f), exchangeForm(token), nil); w.Code != http.StatusInternalServerError {
		t.Errorf("status %d, want 500: %s", w.Code, w.Body)
	}
}

// CachedKeys serves the set a jwk.Cache fetched from the identity
// provider's JWKS URL, and an error for a URL the cache never fetched.
func TestCachedKeys(t *testing.T) {
	p := newIDP(t)
	doc, err := json.Marshal(p.keys)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", jsonContentType)
		_, _ = w.Write(doc)
	}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	cache, err := jwk.NewCache(ctx, httprc.NewClient())
	if err != nil {
		t.Fatal(err)
	}
	if err := cache.Register(ctx, srv.URL, jwk.WithWaitReady(true)); err != nil {
		t.Fatal(err)
	}
	v := newJWTVerifier(t, JWTVerifierConfig{Issuer: idpIssuer, Keys: CachedKeys(cache, srv.URL), Now: clock})
	if _, err := v.VerifySubject(t.Context(), p.sign(t, TypeAccessToken, accessClaims())); err != nil {
		t.Errorf("a token under the cached keys: %v", err)
	}
	unregistered := newJWTVerifier(t, JWTVerifierConfig{Issuer: idpIssuer, Keys: CachedKeys(cache, srv.URL+"/other"), Now: clock})
	if _, err := unregistered.VerifySubject(t.Context(), p.sign(t, TypeAccessToken, accessClaims())); err == nil || errors.Is(err, ErrInvalidGrant) {
		t.Errorf("keys never fetched: %v, want the server's error", err)
	}
}
