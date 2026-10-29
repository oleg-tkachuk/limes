package exchange

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/dpop"
)

// An introspection endpoint limiting each client to the audiences it serves
// answers a token of another audience inactive, and one of its own active.
func TestIntrospectionHoldsTheClientToItsAudiences(t *testing.T) {
	f := newFixture(t, fixtureConfig{})
	res, err := f.ex.Exchange(t.Context(), request(f.idp.sign(t, typeAccessToken, accessClaims())))
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
// one with no DPoP verifier of its own, asked for a nonce no verifier
// issues, answers the server's failure: a use_dpop_nonce with no nonce is
// one the client cannot satisfy (RFC 9449 §8).
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
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != http.StatusInternalServerError || body.Code != codeServerError {
		t.Errorf("status %d, body %s", w.Code, w.Body)
	}
}

// A narrow-only endpoint answers use_dpop_nonce with the nonce its
// capabilities' verifier asked for, so the client can retry.
func TestHandlerNarrowingNonceFromCapabilities(t *testing.T) {
	f := newFixture(t, fixtureConfig{narrow: true, noDPoP: true, capabilityNonces: true})
	h := newHandler(t, f)
	w := httptest.NewRecorder()
	r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, tokenEndpoint, nil)
	h.refuse(w, r, errors.Join(dpop.ErrNonceRequired))
	nonce := w.Header().Get(headerDPoPNonce)
	if ok, err := f.capabilityNonces.Valid(r.Context(), nonce); err != nil || !ok || w.Code != http.StatusBadRequest {
		t.Errorf("status %d, nonce %q valid %v (%v)", w.Code, nonce, ok, err)
	}
}

// failingNonces are server nonces whose store is down.
type failingNonces struct{}

func (failingNonces) Nonce(context.Context) (string, error) {
	return "", errors.New("nonce store down")
}
func (failingNonces) Valid(context.Context, string) (bool, error) {
	return false, errors.New("nonce store down")
}

// A nonce asked for that the server cannot produce is the server's failure,
// logged at ERROR: the client gets no use_dpop_nonce it cannot satisfy.
func TestHandlerAnswersAFailingNonceStoreAsTheServers(t *testing.T) {
	f := newFixture(t, fixtureConfig{})
	replay, err := dpop.NewMemoryReplayCache(dpop.MemoryReplayCacheConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if f.ex.cfg.DPoP, err = dpop.NewVerifier(dpop.VerifierConfig{Replay: replay, Nonces: failingNonces{}, Now: clock}); err != nil {
		t.Fatal(err)
	}
	logs := &records{}
	h := newHandler(t, f)
	h.cfg.Logger = slog.New(logs)
	w := httptest.NewRecorder()
	h.refuse(w, httptest.NewRequestWithContext(t.Context(), http.MethodPost, tokenEndpoint, nil), dpop.ErrNonceRequired)
	var body errorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != http.StatusInternalServerError || body.Code != codeServerError || w.Header().Get(headerDPoPNonce) != "" {
		t.Errorf("status %d, nonce %q, body %s", w.Code, w.Header().Get(headerDPoPNonce), w.Body)
	}
	if level, _ := logs.last(); level != slog.LevelError {
		t.Errorf("logged %s, want ERROR", level)
	}
}

// A policy that leaves the request ceiling unset asks for a capability no
// request can use: Issue refuses it, and the exchange answers it as the
// server's failure, logged at ERROR with the cause, rather than hand the
// client a capability every meter refuses.
func TestExchangeAnswersAPolicyGrantingNoRequestAsTheServers(t *testing.T) {
	var f *fixture
	f = newFixture(t, fixtureConfig{policy: func(ctx context.Context, g Grant) (limes.IssueRequest, error) {
		req, err := f.readPolicy(ctx, g)
		req.Caveats.Requests = limes.RequestCeiling{}
		return req, err
	}})
	logs := &records{}
	f.ex.cfg.Logger = slog.New(logs)
	w := post(t, newHandler(t, f), exchangeForm(f.idp.sign(t, typeAccessToken, accessClaims())), nil)
	var resp errorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || w.Code != http.StatusInternalServerError || resp.Code != codeServerError {
		t.Errorf("status %d: %s", w.Code, w.Body)
	}
	if level, msg := logs.last(); level != slog.LevelError || msg != "exchange refused" || !logs.lastWraps(limes.ErrInvalidCaveats) {
		t.Errorf("logged %s %q, want an ERROR carrying ErrInvalidCaveats", level, msg)
	}
}
