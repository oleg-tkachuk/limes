package exchange

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/oleg-tkachuk/limes/dpop"
)

// An introspection endpoint limiting each client to the audiences it serves
// answers a token of another audience inactive, and one of its own active.
func TestIntrospectionHoldsTheClientToItsAudiences(t *testing.T) {
	f := newFixture(t, fixtureConfig{})
	res, err := f.ex.Exchange(t.Context(), request(f.idp.sign(t, TypeAccessToken, accessClaims())))
	if err != nil {
		t.Fatal(err)
	}
	for audience, active := range map[string]bool{toolResource: true, "https://elsewhere.example.com": false} {
		h, err := NewIntrospectionHandler(IntrospectionConfig{
			Verifier:     f.verifier,
			Authenticate: func(*http.Request) (Client, error) { return client(), nil },
			Audiences:    func(c Client) []string { return []string{audience} },
		})
		if err != nil {
			t.Fatal(err)
		}
		_, body := introspect(t, h, url.Values{paramToken: {res.Token}})
		if body["active"] != active || (!active && len(body) != 1) {
			t.Errorf("a client serving %s: %v, want active %v", audience, body, active)
		}
	}
}

// A narrowing endpoint needs its URL to check a bound parent's proof, and
// one with no DPoP verifier of its own answers a nonce asked for by the
// capabilities' verifier without failing.
func TestHandlerNarrowingWithoutItsOwnDPoP(t *testing.T) {
	f := newFixture(t, fixtureConfig{narrow: true, noDPoP: true})
	if _, err := NewHandler(HandlerConfig{Exchanger: f.ex, Authenticate: func(*http.Request) (Client, error) { return client(), nil }}); err == nil {
		t.Error("a narrowing endpoint with no URL was built")
	}
	h := newHandler(t, f)
	w := httptest.NewRecorder()
	r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, tokenEndpoint, nil)
	h.refuse(w, r, errors.Join(dpop.ErrNonceRequired))
	var body errorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != http.StatusBadRequest || body.Error != codeUseDPoPNonce {
		t.Errorf("status %d, body %s", w.Code, w.Body)
	}
}
