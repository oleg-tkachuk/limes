package exchange

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/dpop"
)

// clientHeader carries the test client's identity; the test authenticator
// trusts it, as an mTLS or assertion check would its own evidence.
const clientHeader = "X-Test-Client"

func newHandler(t *testing.T, f *fixture) *Handler {
	t.Helper()
	h, err := NewHandler(HandlerConfig{
		Exchanger: f.ex, EndpointURL: tokenEndpoint,
		Authenticate: func(r *http.Request) (Client, error) {
			if r.Header.Get(clientHeader) != mcpServer {
				return Client{}, errors.New("unknown client")
			}
			return client(), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// exchangeForm is a token exchange request for token, to toolResource.
func exchangeForm(token string) url.Values {
	return url.Values{
		paramGrantType: {GrantTypeTokenExchange}, paramSubjectToken: {token},
		paramSubjectTokenType: {TokenTypeAccessToken}, paramResource: {toolResource},
	}
}

// post sends form to h as the MCP server, with header's extra fields.
func post(t *testing.T, h http.Handler, form url.Values, header http.Header) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, tokenEndpoint, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", formContentType)
	r.Header.Set(clientHeader, mcpServer)
	for k, vs := range header {
		r.Header.Del(k)
		for _, v := range vs {
			r.Header.Add(k, v)
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// An RFC 8693 request is answered with a capability the resource's verifier
// accepts, in a response no cache keeps.
func TestHandlerExchanges(t *testing.T) {
	f := newFixture(t, fixtureConfig{})
	w := post(t, newHandler(t, f), exchangeForm(f.idp.sign(t, TypeAccessToken, accessClaims())), nil)
	if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != cacheNoStore || w.Header().Get("Pragma") != pragmaNoCache || w.Header().Get("Content-Type") != jsonContentType {
		t.Fatalf("status %d, headers %v: %s", w.Code, w.Header(), w.Body)
	}
	var resp tokenResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.IssuedTokenType != IssuedTokenTypeCapability || resp.TokenType != TokenTypeBearer || resp.ExpiresIn <= 0 || resp.ExpiresIn > int64(subjectTTL.Seconds()) {
		t.Errorf("response %+v", resp)
	}
	if _, err := f.verifier.Verify(t.Context(), limes.VerifyRequest{Token: resp.AccessToken, Audience: toolResource}); err != nil {
		t.Errorf("the capability does not verify: %v", err)
	}
}

// A proof in the DPoP header binds the capability, presented as DPoP.
func TestHandlerBindsToTheDPoPHeader(t *testing.T) {
	f := newFixture(t, fixtureConfig{})
	key := clientKey(t)
	w := post(t, newHandler(t, f), exchangeForm(f.idp.sign(t, TypeAccessToken, accessClaims())), http.Header{headerDPoP: {proof(t, key, "").JWT}})
	var resp tokenResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || w.Code != http.StatusOK || resp.TokenType != TokenTypeDPoP {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	resource := "https://tools.example.com/call"
	at, err := dpop.Sign(context.Background(), key, dpop.SignRequest{Method: http.MethodPost, URL: resource, Token: resp.AccessToken, IssuedAt: testNow})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.verifier.Verify(t.Context(), limes.VerifyRequest{
		Token: resp.AccessToken, Audience: toolResource, Scheme: limes.AuthSchemeDPoP,
		DPoP: dpop.Proof{JWT: at, Method: http.MethodPost, URL: resource},
	}); err != nil {
		t.Errorf("the bound capability does not verify with its key's proof: %v", err)
	}
}

// Each refusal is answered with its RFC 6749 §5.2 code and a fixed
// description, never the error's own text.
func TestHandlerRefusals(t *testing.T) {
	f := newFixture(t, fixtureConfig{})
	h := newHandler(t, f)
	token := f.idp.sign(t, TypeAccessToken, accessClaims())
	change := func(changes map[string][]string) url.Values {
		form := exchangeForm(token)
		for k, v := range changes {
			if v == nil {
				form.Del(k)
			} else {
				form[k] = v
			}
		}
		return form
	}
	cases := map[string]struct {
		form   url.Values
		header http.Header
		status int
		code   string
	}{
		"another grant":               {change(map[string][]string{paramGrantType: {"client_credentials"}}), nil, http.StatusBadRequest, codeUnsupportedGrantType},
		"no grant":                    {change(map[string][]string{paramGrantType: nil}), nil, http.StatusBadRequest, codeInvalidRequest},
		"no subject token":            {change(map[string][]string{paramSubjectToken: nil}), nil, http.StatusBadRequest, codeInvalidRequest},
		"another subject type":        {change(map[string][]string{paramSubjectTokenType: {"urn:ietf:params:oauth:token-type:id_token"}}), nil, http.StatusBadRequest, codeInvalidRequest},
		"an actor token":              {change(map[string][]string{paramActorToken: {token}}), nil, http.StatusBadRequest, codeInvalidRequest},
		"another requested type":      {change(map[string][]string{paramRequestedTokenType: {TokenTypeJWT}}), nil, http.StatusBadRequest, codeInvalidRequest},
		"a scope twice":               {change(map[string][]string{paramScope: {"a", "b"}}), nil, http.StatusBadRequest, codeInvalidRequest},
		"a relative resource":         {change(map[string][]string{paramResource: {"tools"}}), nil, http.StatusBadRequest, codeInvalidTarget},
		"a resource fragment":         {change(map[string][]string{paramResource: {toolResource + "#x"}}), nil, http.StatusBadRequest, codeInvalidTarget},
		"an empty fragment":           {change(map[string][]string{paramResource: {toolResource + "#"}}), nil, http.StatusBadRequest, codeInvalidTarget},
		"an unparsable resource":      {change(map[string][]string{paramResource: {"https://[::1"}}), nil, http.StatusBadRequest, codeInvalidTarget},
		"details not an array":        {change(map[string][]string{paramAuthorizationDetails: {`{"type":"x"}`}}), nil, http.StatusBadRequest, codeInvalidAuthorizationDetails},
		"a detail not an object":      {change(map[string][]string{paramAuthorizationDetails: {`[1]`}}), nil, http.StatusBadRequest, codeInvalidAuthorizationDetails},
		"a detail with no type":       {change(map[string][]string{paramAuthorizationDetails: {`[{"actions":["read"]}]`}}), nil, http.StatusBadRequest, codeInvalidAuthorizationDetails},
		"a detail with an empty type": {change(map[string][]string{paramAuthorizationDetails: {`[{"type":""}]`}}), nil, http.StatusBadRequest, codeInvalidAuthorizationDetails},
		"a detail with a number type": {change(map[string][]string{paramAuthorizationDetails: {`[{"type":1}]`}}), nil, http.StatusBadRequest, codeInvalidAuthorizationDetails},
		"an unknown client":           {exchangeForm(token), http.Header{clientHeader: {"someone"}}, http.StatusBadRequest, codeInvalidClient},
		"a refused subject token":     {change(map[string][]string{paramSubjectToken: {"a.b.c"}}), nil, http.StatusBadRequest, codeInvalidGrant},
		"two DPoP headers":            {exchangeForm(token), http.Header{headerDPoP: {"a", "b"}}, http.StatusBadRequest, codeInvalidDPoPProof},
	}
	for name, tc := range cases {
		w := post(t, h, tc.form, tc.header)
		var resp errorResponse
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || w.Code != tc.status || resp.Error != tc.code {
			t.Errorf("%s: status %d, body %s; want %d %s", name, w.Code, w.Body, tc.status, tc.code)
			continue
		}
		if strings.Contains(resp.Description, "exchange:") || w.Header().Get("Cache-Control") != cacheNoStore || w.Header().Get("Pragma") != pragmaNoCache {
			t.Errorf("%s: description %q, headers %v", name, resp.Description, w.Header())
		}
	}
}

// A response states what was granted when the request asked: the scope as
// asked, since a policy that does not refuse it grants it all (RFC 8693
// §2.2.1), and the capability's caveats as the authorization_details
// granted (RFC 9396 §7). A request that asked for neither gets neither.
func TestHandlerAnswersWhatWasGranted(t *testing.T) {
	f := newFixture(t, fixtureConfig{})
	h := newHandler(t, f)
	form := exchangeForm(f.idp.sign(t, TypeAccessToken, accessClaims()))
	form.Set(paramScope, "tools:read tools:list")
	form.Set(paramAuthorizationDetails, `[{"type":"https://tools.example.com/call","actions":["read"]}]`)
	w := post(t, h, form, nil)
	var resp struct {
		Scope                string            `json:"scope"`
		AuthorizationDetails []json.RawMessage `json:"authorization_details"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	want, err := caveatsDetail(limes.Caveats{Requests: limes.RequestCeiling{Unlimited: true}, Ops: []limes.Op{limes.OpGet}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Scope != "tools:read tools:list" || len(resp.AuthorizationDetails) != 1 || !jsonEqual(t, resp.AuthorizationDetails[0], want) {
		t.Errorf("scope %q, authorization_details %s; want %s", resp.Scope, resp.AuthorizationDetails, want)
	}
	plain := post(t, h, exchangeForm(f.idp.sign(t, TypeAccessToken, accessClaims())), nil)
	var body map[string]any
	if err := json.Unmarshal(plain.Body.Bytes(), &body); err != nil || plain.Code != http.StatusOK {
		t.Fatalf("status %d: %s", plain.Code, plain.Body)
	}
	for _, member := range []string{paramScope, paramAuthorizationDetails} {
		if _, ok := body[member]; ok {
			t.Errorf("%s answered unasked: %s", member, plain.Body)
		}
	}
}

// jsonEqual reports whether a and b are the same JSON value.
func jsonEqual(t *testing.T, a, b json.RawMessage) bool {
	t.Helper()
	var x, y any
	if err := json.Unmarshal(a, &x); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &y); err != nil {
		t.Fatal(err)
	}
	return reflect.DeepEqual(x, y)
}

// A policy refuses authorization_details it does not understand with
// ErrInvalidAuthorizationDetails, answered invalid_authorization_details
// (RFC 9396 §5) and logged as the client's refusal, at WARN.
func TestHandlerAnswersAPolicyRefusingTheDetails(t *testing.T) {
	refusing := func(context.Context, Grant) (limes.IssueRequest, error) {
		return limes.IssueRequest{}, fmt.Errorf("type %q unknown: %w", "x", ErrInvalidAuthorizationDetails)
	}
	f := newFixture(t, fixtureConfig{policy: refusing})
	logs := &records{}
	f.ex.cfg.Logger = slog.New(logs)
	form := exchangeForm(f.idp.sign(t, TypeAccessToken, accessClaims()))
	form.Set(paramAuthorizationDetails, `[{"type":"x"}]`)
	w := post(t, newHandler(t, f), form, nil)
	var resp errorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || w.Code != http.StatusBadRequest || resp.Error != codeInvalidAuthorizationDetails {
		t.Errorf("status %d: %s", w.Code, w.Body)
	}
	if level, _ := logs.last(); level != slog.LevelWarn {
		t.Errorf("logged %s, want WARN", level)
	}
}

// A parameter sent without a value is treated as omitted (RFC 6749 §3.2):
// the exchange goes through as if it had not been sent.
func TestHandlerTreatsAnEmptyParameterAsOmitted(t *testing.T) {
	f := newFixture(t, fixtureConfig{})
	h := newHandler(t, f)
	for _, name := range []string{
		paramAudience, paramResource, paramActorToken, paramActorTokenType, paramRequestedTokenType, paramScope, paramAuthorizationDetails,
	} {
		form := exchangeForm(f.idp.sign(t, TypeAccessToken, accessClaims()))
		form.Add(name, "")
		if w := post(t, h, form, nil); w.Code != http.StatusOK {
			t.Errorf("an empty %s: status %d: %s", name, w.Code, w.Body)
		}
	}
}

// basicCredentials are HTTP Basic credentials a client sends (RFC 7617).
const basicCredentials = "Basic bWNwOnNlY3JldA=="

// A client refused as not authenticated is answered 401 with a challenge
// in the scheme it authenticated with (RFC 6749 §5.2), or, when it sent no
// Authorization header, 400: a 401 must carry a challenge (RFC 9110
// §15.5.2), and mTLS or a client assertion has none to give. Both
// endpoints answer alike.
func TestClientRefusalChallengesInTheClientsScheme(t *testing.T) {
	f := newFixture(t, fixtureConfig{})
	refusing := func(*http.Request) (Client, error) { return Client{}, errors.New("bad credentials") }
	token, err := NewHandler(HandlerConfig{Exchanger: f.ex, EndpointURL: tokenEndpoint, Authenticate: refusing})
	if err != nil {
		t.Fatal(err)
	}
	introspection, err := NewIntrospectionHandler(IntrospectionConfig{Verifier: f.verifier, Authenticate: refusing})
	if err != nil {
		t.Fatal(err)
	}
	basic := `Basic realm="` + limes.DefaultRealm + `"`
	for name, tc := range map[string]struct {
		authorization []string
		status        int
		challenge     string
	}{
		"no Authorization":         {nil, http.StatusBadRequest, ""},
		"Basic":                    {[]string{basicCredentials}, http.StatusUnauthorized, basic},
		"Basic with no credential": {[]string{"Basic"}, http.StatusUnauthorized, basic},
		"Basic after a tab":        {[]string{"Basic\tbWNw"}, http.StatusUnauthorized, basic},
		"a lower-case scheme":      {[]string{"basic bWNw"}, http.StatusUnauthorized, `basic realm="` + limes.DefaultRealm + `"`},
		"no scheme":                {[]string{" bWNw"}, http.StatusBadRequest, ""},
		"not a token":              {[]string{"B@sic bWNw"}, http.StatusBadRequest, ""},
	} {
		for endpoint, h := range map[string]http.Handler{"token": token, "introspection": introspection} {
			form := exchangeForm(f.idp.sign(t, TypeAccessToken, accessClaims()))
			form.Set(paramToken, "a")
			w := post(t, h, form, http.Header{headerAuthorization: tc.authorization})
			var resp errorResponse
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || w.Code != tc.status || resp.Error != codeInvalidClient {
				t.Errorf("%s, %s: status %d, body %s; want %d %s", endpoint, name, w.Code, w.Body, tc.status, codeInvalidClient)
			}
			var want []string
			if tc.challenge != "" {
				want = []string{tc.challenge}
			}
			if got := w.Header().Values(headerWWWAuthenticate); !slices.Equal(got, want) {
				t.Errorf("%s, %s: WWW-Authenticate %q, want %q", endpoint, name, got, tc.challenge)
			}
		}
	}
}

// An authenticated client not allowed to exchange is answered
// unauthorized_client (400), wrapped or not; any other error of
// Authenticate stays invalid_client (401). Each refusal is logged at WARN
// with the authenticator's error.
func TestHandlerTellsAnUnauthorizedClientFromAnUnauthenticatedOne(t *testing.T) {
	f := newFixture(t, fixtureConfig{})
	token := f.idp.sign(t, TypeAccessToken, accessClaims())
	for name, tc := range map[string]struct {
		err    error
		status int
		code   string
	}{
		"the sentinel":         {ErrUnauthorizedClient, http.StatusBadRequest, codeUnauthorizedClient},
		"the sentinel wrapped": {fmt.Errorf("client %s may not exchange: %w", mcpServer, ErrUnauthorizedClient), http.StatusBadRequest, codeUnauthorizedClient},
		"a plain error":        {errors.New("bad credentials"), http.StatusUnauthorized, codeInvalidClient},
	} {
		logs := &records{}
		h, err := NewHandler(HandlerConfig{
			Exchanger: f.ex, EndpointURL: tokenEndpoint, Logger: slog.New(logs),
			Authenticate: func(*http.Request) (Client, error) { return Client{}, tc.err },
		})
		if err != nil {
			t.Fatal(err)
		}
		w := post(t, h, exchangeForm(token), http.Header{headerAuthorization: {basicCredentials}})
		var resp errorResponse
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || w.Code != tc.status || resp.Error != tc.code {
			t.Errorf("%s: status %d, body %s; want %d %s", name, w.Code, w.Body, tc.status, tc.code)
		}
		if level, msg := logs.last(); level != slog.LevelWarn || msg != "exchange request refused" || !logs.lastWraps(tc.err) {
			t.Errorf("%s logged %s %q, want a WARN refusal carrying %v", name, level, msg, tc.err)
		}
		if logs.lastWraps(ErrInvalidClient) != (tc.code == codeInvalidClient) {
			t.Errorf("%s: logged as an unauthenticated client: %v", name, logs.lastWraps(ErrInvalidClient))
		}
	}
}

// A request that is not a form POST, or too large, is refused before
// anything is read from it.
func TestHandlerReadsOnlyAFormPost(t *testing.T) {
	f := newFixture(t, fixtureConfig{})
	h := newHandler(t, f)
	get := httptest.NewRequestWithContext(t.Context(), http.MethodGet, tokenEndpoint, nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, get)
	if w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != http.MethodPost {
		t.Errorf("GET: status %d, Allow %q", w.Code, w.Header().Get("Allow"))
	}
	asJSON := httptest.NewRequestWithContext(t.Context(), http.MethodPost, tokenEndpoint, strings.NewReader("{}"))
	asJSON.Header.Set("Content-Type", jsonContentType)
	asJSON.Header.Set(clientHeader, mcpServer)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, asJSON)
	if w.Code != http.StatusBadRequest {
		t.Errorf("a JSON body: status %d", w.Code)
	}
	big := exchangeForm(strings.Repeat("a", DefaultMaxBodyBytes))
	if w := post(t, h, big, nil); w.Code != http.StatusBadRequest {
		t.Errorf("a body over the bound: status %d", w.Code)
	}
}

// An exchange whose DPoP verifier wants nonces answers a proof without one
// with use_dpop_nonce and a nonce to use.
func TestHandlerAsksForTheNonce(t *testing.T) {
	f := newFixture(t, fixtureConfig{nonces: true})
	h := newHandler(t, f)
	form := exchangeForm(f.idp.sign(t, TypeAccessToken, accessClaims()))
	key := clientKey(t)
	w := post(t, h, form, http.Header{headerDPoP: {proof(t, key, "").JWT}})
	var resp errorResponse
	nonce := w.Header().Get(headerDPoPNonce)
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || w.Code != http.StatusBadRequest || resp.Error != codeUseDPoPNonce || nonce == "" {
		t.Fatalf("status %d, nonce %q: %s", w.Code, nonce, w.Body)
	}
	if w := post(t, h, form, http.Header{headerDPoP: {proof(t, key, nonce).JWT}}); w.Code != http.StatusOK {
		t.Errorf("with the nonce: status %d: %s", w.Code, w.Body)
	}
}

// A failure that is not the client's is a 500 telling nothing.
func TestHandlerHidesTheServersFailure(t *testing.T) {
	f := newFixture(t, fixtureConfig{})
	f.ex.cfg.Policy = func(context.Context, Grant) (limes.IssueRequest, error) {
		return limes.IssueRequest{}, errors.New("policy store down")
	}
	w := post(t, newHandler(t, f), exchangeForm(f.idp.sign(t, TypeAccessToken, accessClaims())), nil)
	if w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), "down") {
		t.Errorf("status %d: %s", w.Code, w.Body)
	}
}

func TestNewHandlerRefusesAnIncompleteConfig(t *testing.T) {
	f := newFixture(t, fixtureConfig{})
	auth := func(*http.Request) (Client, error) { return client(), nil }
	for name, cfg := range map[string]HandlerConfig{
		"no exchanger":         {Authenticate: auth, EndpointURL: tokenEndpoint},
		"no authenticator":     {Exchanger: f.ex, EndpointURL: tokenEndpoint},
		"DPoP with no URL":     {Exchanger: f.ex, Authenticate: auth},
		"a negative body size": {Exchanger: f.ex, Authenticate: auth, EndpointURL: tokenEndpoint, MaxBodyBytes: -1},
	} {
		if _, err := NewHandler(cfg); err == nil {
			t.Errorf("a handler with %s was built", name)
		}
	}
}
