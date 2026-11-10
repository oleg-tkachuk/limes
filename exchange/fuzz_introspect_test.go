package exchange

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// FuzzIntrospection posts the introspection endpoint a body the fuzzer
// writes, as an authenticated resource server, and holds it to RFC 7662's
// answers: a token it reads or not is a 200 and one it cannot read the
// request of a 400 — never a server error for what a client sent — always
// as JSON; and the only token it calls active is one the exchange issued.
func FuzzIntrospection(f *testing.F) {
	fx := newFixture(f, fixtureConfig{})
	h, err := NewIntrospectionHandler(IntrospectionConfig{
		Verifier: fx.verifier, AnyAudience: true,
		Authenticate: func(*http.Request) (Client, error) { return client(), nil },
	})
	if err != nil {
		f.Fatal(err)
	}
	issued, err := fx.ex.Exchange(context.Background(), request(fx.idp.sign(f, typeAccessToken, accessClaims())))
	if err != nil {
		f.Fatal(err)
	}
	f.Add(url.Values{paramToken: {issued.Token}}.Encode())
	f.Add(url.Values{paramToken: {issued.Token}, paramTokenTypeHint: {"access_token"}}.Encode())
	f.Add(url.Values{paramToken: {issued.Token, issued.Token}}.Encode())
	f.Add("token=%zz")
	f.Fuzz(func(t *testing.T, body string) {
		r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, tokenEndpoint, strings.NewReader(body))
		r.Header.Set("Content-Type", formContentType)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusOK && w.Code != http.StatusBadRequest {
			t.Fatalf("body %q: status %d: %s", body, w.Code, w.Body)
		}
		var answer map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &answer); err != nil {
			t.Fatalf("body %q: status %d, not JSON: %s", body, w.Code, w.Body)
		}
		if answer["active"] != true {
			return
		}
		// An answer at all means the endpoint read the body as a form.
		form, _ := url.ParseQuery(body)
		if form.Get(paramToken) != issued.Token {
			t.Fatalf("body %q: an active answer for a token the exchange never issued: %v", body, answer)
		}
	})
}
