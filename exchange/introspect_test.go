package exchange

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/dpop"
	"github.com/oleg-tkachuk/limes/jwks"
)

func newIntrospection(t *testing.T, v *limes.Verifier) *IntrospectionHandler {
	t.Helper()
	h, err := NewIntrospectionHandler(IntrospectionConfig{
		Verifier: v, AnyAudience: true,
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

// introspect posts form to h as the MCP server and decodes the answer.
func introspect(t *testing.T, h http.Handler, form url.Values) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	w := post(t, h, form, nil)
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	return w, body
}

// An active token's answer carries its standard members, its client, and,
// for a bound one, its key; the hint is read and ignored.
func TestIntrospectionActive(t *testing.T) {
	f := newFixture(t, fixtureConfig{})
	h := newIntrospection(t, f.verifier)
	key := clientKey(t)
	jkt, err := dpop.Thumbprint(key.Public())
	if err != nil {
		t.Fatal(err)
	}
	bearer, err := f.ex.Exchange(t.Context(), request(f.idp.sign(t, typeAccessToken, accessClaims())))
	if err != nil {
		t.Fatal(err)
	}
	boundReq := request(f.idp.sign(t, typeAccessToken, accessClaims()))
	boundReq.DPoP = proof(t, key, "")
	bound, err := f.ex.Exchange(t.Context(), boundReq)
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		res       Result
		tokenType string
		jkt       string
	}{
		"a bearer token": {bearer, TokenTypeBearer, ""},
		"a bound token":  {bound, TokenTypeDPoP, jkt},
	} {
		w, body := introspect(t, h, url.Values{paramToken: {tc.res.Token}, paramTokenTypeHint: {"refresh_token"}})
		c := tc.res.Capability
		if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != cacheNoStore || body["active"] != true ||
			body["jti"] != c.ID.String() || body["sub"] != c.Subject.Subject || body["iss"] != c.Issuer ||
			body["client_id"] != idpClient || body["token_type"] != tc.tokenType || body["exp"] != float64(c.ExpiresAt.Unix()) {
			t.Errorf("%s: status %d, body %v", name, w.Code, body)
		}
		cnfGot, _ := body["cnf"].(map[string]any)
		if (tc.jkt == "") != (cnfGot == nil) || (cnfGot != nil && cnfGot["jkt"] != tc.jkt) {
			t.Errorf("%s: cnf %v, want jkt %q", name, body["cnf"], tc.jkt)
		}
		checkCaveatsDetail(t, name, body, c.Caveats)
	}
}

// checkCaveatsDetail holds an answer's authorization_details to one entry of
// the caveats type, whose other members are the token's limes_caveats claim.
func checkCaveatsDetail(t *testing.T, name string, body map[string]any, caveats limes.Caveats) {
	t.Helper()
	details, _ := body["authorization_details"].([]any)
	if len(details) != 1 {
		t.Fatalf("%s: authorization_details %v, want one entry", name, body["authorization_details"])
	}
	entry, _ := details[0].(map[string]any)
	if entry[detailType] != AuthorizationDetailsTypeCaveats {
		t.Errorf("%s: entry type %v, want %q", name, entry[detailType], AuthorizationDetailsTypeCaveats)
	}
	raw, err := limes.MarshalCaveats(caveats)
	if err != nil {
		t.Fatal(err)
	}
	var want map[string]any
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	delete(entry, detailType)
	if !reflect.DeepEqual(entry, want) {
		t.Errorf("%s: entry %v, want the caveats %v", name, entry, want)
	}
}

// An inactive token's answer is active: false and nothing more, revoked or
// not a token at all.
func TestIntrospectionInactive(t *testing.T) {
	f := newFixture(t, fixtureConfig{})
	h := newIntrospection(t, f.verifier)
	res, err := f.ex.Exchange(t.Context(), request(f.idp.sign(t, typeAccessToken, accessClaims())))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Revoke(t.Context(), limes.RevokeRequest{CapabilityID: res.Capability.ID, Actor: "test"}); err != nil {
		t.Fatal(err)
	}
	for name, token := range map[string]string{"a revoked token": res.Token, "what is no token": "a.b.c"} {
		w, body := introspect(t, h, url.Values{paramToken: {token}})
		if w.Code != http.StatusOK || len(body) != 1 || body["active"] != false {
			t.Errorf("%s: status %d, body %v; want {active: false} alone", name, w.Code, body)
		}
	}
}

// failingRevocations cannot answer.
type failingRevocations struct{}

func (failingRevocations) IsRevoked(context.Context, uuid.UUID) (bool, error) {
	return false, errors.New("store down")
}

// A store that cannot answer is a 500, never active: false, which a gateway
// would read as every token revoked at once.
func TestIntrospectionOutage(t *testing.T) {
	f := newFixture(t, fixtureConfig{})
	res, err := f.ex.Exchange(t.Context(), request(f.idp.sign(t, typeAccessToken, accessClaims())))
	if err != nil {
		t.Fatal(err)
	}
	keys, err := jwks.NewStatic(jwks.StaticConfig{Keys: map[string]ed25519.PublicKey{f.signingKID: f.signingKey}})
	if err != nil {
		t.Fatal(err)
	}
	// The lookup answers nothing, record or not: the outage is the point.
	v, err := limes.NewVerifier(limes.VerifierConfig{Keys: keys, Revocations: failingRevocations{}, AllowUnrecorded: true, TrustedIssuers: []string{limesIssuer}, Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	w := post(t, newIntrospection(t, v), url.Values{paramToken: {res.Token}}, nil)
	if w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), "active") {
		t.Errorf("status %d: %s", w.Code, w.Body)
	}
}

// During a revocation store outage, a client asking about a token of an
// audience it does not serve is answered active: false alike for a genuine
// token and a forged one, so the answer tells it nothing of which foreign
// tokens are genuine; a token of an audience it serves still answers the
// outage as the server's failure.
func TestIntrospectionOutageTellsNothingOfForeignTokens(t *testing.T) {
	f := newFixture(t, fixtureConfig{})
	res, err := f.ex.Exchange(t.Context(), request(f.idp.sign(t, typeAccessToken, accessClaims())))
	if err != nil {
		t.Fatal(err)
	}
	keys, err := jwks.NewStatic(jwks.StaticConfig{Keys: map[string]ed25519.PublicKey{f.signingKID: f.signingKey}})
	if err != nil {
		t.Fatal(err)
	}
	v, err := limes.NewVerifier(limes.VerifierConfig{Keys: keys, Revocations: failingRevocations{}, AllowUnrecorded: true, TrustedIssuers: []string{limesIssuer}, Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	handler := func(audience string) http.Handler {
		h, err := NewIntrospectionHandler(IntrospectionConfig{
			Verifier:     v,
			Authenticate: func(*http.Request) (Client, error) { return client(), nil },
			Audiences:    func(Client) []string { return []string{audience} },
		})
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	dot := strings.LastIndexByte(res.Token, '.') + 1
	swap := "A"
	if res.Token[dot:dot+1] == swap {
		swap = "B"
	}
	forged := res.Token[:dot] + swap + res.Token[dot+1:]
	foreign := handler("https://elsewhere.example.com")
	answers := map[string]string{}
	for name, token := range map[string]string{"a genuine token": res.Token, "a forged token": forged} {
		w := post(t, foreign, url.Values{paramToken: {token}}, nil)
		if w.Code != http.StatusOK {
			t.Errorf("%s of another audience: status %d: %s", name, w.Code, w.Body)
		}
		answers[name] = w.Body.String()
	}
	if answers["a genuine token"] != answers["a forged token"] {
		t.Errorf("a genuine token answered %s, a forged one %s; want the same", answers["a genuine token"], answers["a forged token"])
	}
	if w := post(t, handler(toolResource), url.Values{paramToken: {res.Token}}, nil); w.Code != http.StatusInternalServerError {
		t.Errorf("a token of the client's audience: status %d, want 500: %s", w.Code, w.Body)
	}
}

// A client that serves no audience learns about no token.
func TestIntrospectionOfAClientServingNoAudience(t *testing.T) {
	f := newFixture(t, fixtureConfig{})
	res, err := f.ex.Exchange(t.Context(), request(f.idp.sign(t, typeAccessToken, accessClaims())))
	if err != nil {
		t.Fatal(err)
	}
	for name, served := range map[string][]string{"none": nil, "an empty one": {""}} {
		h, err := NewIntrospectionHandler(IntrospectionConfig{
			Verifier:     f.verifier,
			Authenticate: func(*http.Request) (Client, error) { return client(), nil },
			Audiences:    func(Client) []string { return served },
		})
		if err != nil {
			t.Fatal(err)
		}
		w, body := introspect(t, h, url.Values{paramToken: {res.Token}})
		if w.Code != http.StatusOK || len(body) != 1 || body["active"] != false {
			t.Errorf("%s: status %d, body %v; want {active: false} alone", name, w.Code, body)
		}
	}
}

// A resource server the endpoint authenticated but does not allow to
// introspect is answered 400 unauthorized_client, wrapped or not, and logged
// at WARN as a refusal (RFC 7662 §2.3, RFC 6749 §5.2); a server error of
// Authenticate is answered 500 server_error with no detail, logged at ERROR.
func TestIntrospectionTellsAnUnauthorizedClientAndAServerError(t *testing.T) {
	f := newFixture(t, fixtureConfig{})
	for name, tc := range map[string]struct {
		err    error
		status int
		code   string
		level  slog.Level
	}{
		"the sentinel":           {ErrUnauthorizedClient, http.StatusBadRequest, codeUnauthorizedClient, slog.LevelWarn},
		"the sentinel wrapped":   {fmt.Errorf("%s may not introspect: %w", mcpServer, ErrUnauthorizedClient), http.StatusBadRequest, codeUnauthorizedClient, slog.LevelWarn},
		"a server error":         {ErrServerError, http.StatusInternalServerError, codeServerError, slog.LevelError},
		"a server error wrapped": {fmt.Errorf("look up %s: %w", mcpServer, ErrServerError), http.StatusInternalServerError, codeServerError, slog.LevelError},
	} {
		refusal := tc.err
		logs := &records{}
		h, err := NewIntrospectionHandler(IntrospectionConfig{
			Verifier: f.verifier, Logger: slog.New(logs), AnyAudience: true,
			Authenticate: func(*http.Request) (Client, error) { return Client{}, refusal },
		})
		if err != nil {
			t.Fatal(err)
		}
		w := post(t, h, url.Values{paramToken: {"a"}}, http.Header{headerAuthorization: {basicCredentials}})
		var resp errorResponse
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || w.Code != tc.status || resp.Code != tc.code {
			t.Errorf("%s: status %d, body %s", name, w.Code, w.Body)
		}
		if tc.code == codeServerError && resp.Description != "" {
			t.Errorf("%s: the body tells the client why: %s", name, w.Body)
		}
		if level, _ := logs.last(); level != tc.level || !logs.lastWraps(refusal) || logs.lastWraps(ErrInvalidClient) {
			t.Errorf("%s logged %s", name, level)
		}
	}
}

// A request that is not a form POST naming one token, or comes from no
// client the endpoint knows, is refused.
func TestIntrospectionRefusals(t *testing.T) {
	f := newFixture(t, fixtureConfig{})
	h := newIntrospection(t, f.verifier)
	for name, tc := range map[string]struct {
		form   url.Values
		header http.Header
		status int
	}{
		"no token":          {url.Values{}, nil, http.StatusBadRequest},
		"a token twice":     {url.Values{paramToken: {"a", "b"}}, nil, http.StatusBadRequest},
		"an unknown client": {url.Values{paramToken: {"a"}}, http.Header{clientHeader: {"someone"}}, http.StatusBadRequest},
	} {
		if w := post(t, h, tc.form, tc.header); w.Code != tc.status {
			t.Errorf("%s: status %d, want %d", name, w.Code, tc.status)
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, tokenEndpoint, nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET: status %d", w.Code)
	}
	for name, cfg := range map[string]IntrospectionConfig{
		"no verifier":      {Authenticate: func(*http.Request) (Client, error) { return client(), nil }},
		"no authenticator": {Verifier: f.verifier, AnyAudience: true},
	} {
		if _, err := NewIntrospectionHandler(cfg); err == nil {
			t.Errorf("a handler with %s was built", name)
		}
	}
}
