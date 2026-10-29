package jwtverify

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/oleg-tkachuk/limes/exchange"
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

// Keys that cannot be had are the server's failure: the verifier returns
// the outage, not a refusal of the token, so the exchange answers it as
// the server's (exchange's own tests hold it to that).
func TestKeysOutageIsTheServers(t *testing.T) {
	p := newIDP(t)
	v := newVerifier(t, Config{Issuer: idpIssuer, Keys: downKeys{}, Now: clock})
	token := p.sign(t, TypeAccessToken, accessClaims())
	if _, err := v.VerifySubject(t.Context(), token); !errors.Is(err, errKeysDown) || errors.Is(err, exchange.ErrInvalidGrant) {
		t.Errorf("err = %v, want the outage and no ErrInvalidGrant", err)
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
	v := newVerifier(t, Config{Issuer: idpIssuer, Keys: CachedKeys(cache, srv.URL), Now: clock})
	if _, err := v.VerifySubject(t.Context(), p.sign(t, TypeAccessToken, accessClaims())); err != nil {
		t.Errorf("a token under the cached keys: %v", err)
	}
	unregistered := newVerifier(t, Config{Issuer: idpIssuer, Keys: CachedKeys(cache, srv.URL+"/other"), Now: clock})
	if _, err := unregistered.VerifySubject(t.Context(), p.sign(t, TypeAccessToken, accessClaims())); err == nil || errors.Is(err, exchange.ErrInvalidGrant) {
		t.Errorf("keys never fetched: %v, want the server's error", err)
	}
}
