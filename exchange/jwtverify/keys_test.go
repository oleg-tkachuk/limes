package jwtverify

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v4/jwk"
	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/exchange"
)

// remoteKeysWait bounds how long a test waits for RemoteKeys' first fetch,
// and remoteKeysPoll how often it looks.
const (
	remoteKeysWait = 5 * time.Second
	remoteKeysPoll = 10 * time.Millisecond
)

// errKeysDown is a KeySource's failure to reach the identity provider.
var errKeysDown = errors.New("JWKS endpoint unreachable")

// Keys that cannot be had are the server's failure: the verifier returns
// the outage, not a refusal of the token, so the exchange answers it as
// the server's (exchange's own tests hold it to that).
func TestKeysOutageIsTheServers(t *testing.T) {
	p := newIDP(t)
	down := &KeySource{keys: func(context.Context) (jwk.Set, error) { return nil, errKeysDown }}
	v := newVerifier(t, Config{Issuer: idpIssuer, Keys: down, Now: clock})
	token := p.sign(t, TypeAccessToken, accessClaims())
	if _, err := v.VerifySubject(t.Context(), token); !errors.Is(err, errKeysDown) || errors.Is(err, exchange.ErrInvalidGrant) {
		t.Errorf("err = %v, want the outage and no ErrInvalidGrant", err)
	}
}

// StaticKeys refuses a document that is no JWK Set, as a configuration
// error.
func TestStaticKeysRefusesANonJWKSet(t *testing.T) {
	for name, doc := range map[string]string{"not JSON": "{", "no keys member": `{"kty": 1}`} {
		if _, err := StaticKeys([]byte(doc)); !errors.Is(err, limes.ErrInvalidConfig) {
			t.Errorf("%s: err = %v, want ErrInvalidConfig", name, err)
		}
	}
}

// RemoteKeys refuses an empty URL as a configuration error.
func TestRemoteKeysNeedsAURL(t *testing.T) {
	if _, err := RemoteKeys(t.Context(), "", nil); !errors.Is(err, limes.ErrInvalidConfig) {
		t.Errorf("err = %v, want ErrInvalidConfig", err)
	}
}

// RemoteKeys serves the set fetched from the identity provider's JWKS URL
// through the client it is given, and returns without waiting for it.
func TestRemoteKeysServesTheFetchedSet(t *testing.T) {
	p := newIDP(t)
	doc, err := json.Marshal(p.keys)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", jsonContentType)
		_, _ = w.Write(doc)
	}))
	t.Cleanup(srv.Close)
	keys, err := RemoteKeys(t.Context(), srv.URL, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	v := newVerifier(t, Config{Issuer: idpIssuer, Keys: keys, Now: clock})
	token := p.sign(t, TypeAccessToken, accessClaims())
	deadline := time.Now().Add(remoteKeysWait)
	for {
		_, err := v.VerifySubject(t.Context(), token)
		if err == nil {
			return
		}
		if errors.Is(err, exchange.ErrInvalidGrant) {
			t.Fatalf("a token under the fetched keys refused: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("the keys were never fetched: %v", err)
		}
		time.Sleep(remoteKeysPoll)
	}
}

// Keys never fetched — the JWKS URL out of reach from the start — are the
// server's failure, not a refusal of the token.
func TestRemoteKeysNeverFetchedAreTheServers(t *testing.T) {
	p := newIDP(t)
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	keys, err := RemoteKeys(t.Context(), url, nil)
	if err != nil {
		t.Fatal(err)
	}
	v := newVerifier(t, Config{Issuer: idpIssuer, Keys: keys, Now: clock})
	if _, err := v.VerifySubject(t.Context(), p.sign(t, TypeAccessToken, accessClaims())); err == nil || errors.Is(err, exchange.ErrInvalidGrant) {
		t.Errorf("keys never fetched: %v, want the server's error", err)
	}
}
