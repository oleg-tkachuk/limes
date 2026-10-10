package limes

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// endpointClient authenticates with a header naming the client, which may
// act on the capabilities of its own tenant only.
const clientHeader = "X-Test-Client"

func testAuthenticate(r *http.Request) (Principal, error) {
	tenant, err := uuid.Parse(r.Header.Get(clientHeader))
	if err != nil {
		return Principal{}, ErrClientUnauthorized
	}
	return Principal{Subject: "client", TenantID: tenant}, nil
}

func sameTenant(client Principal, c Capability) bool { return client.TenantID == c.Subject.TenantID }

func postForm(t *testing.T, h http.Handler, client string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", mediaTypeForm)
	if client != "" {
		req.Header.Set(clientHeader, client)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestRevocationHandler(t *testing.T) {
	issuer, v, store, _ := buildIssuerVerifier(t)
	h, err := RevocationHandler(RevocationConfig{Verifier: v, Store: store, Authenticate: testAuthenticate, Authorize: sameTenant})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	c, token := issueFor(t, issuer)
	owner, stranger := c.Subject.TenantID.String(), uuid.NewString()

	// A client that may not revoke it gets 200 and revokes nothing.
	if rec := postForm(t, h, stranger, url.Values{paramToken: {token}}); rec.Code != http.StatusOK {
		t.Fatalf("a stranger's revocation = %d, want 200", rec.Code)
	}
	if _, err := v.Verify(ctx, VerifyRequest{Token: token, Audience: audienceData}); err != nil {
		t.Fatalf("a stranger revoked the token: %v", err)
	}
	// Its owner revokes it.
	if rec := postForm(t, h, owner, url.Values{paramToken: {token}, "token_type_hint": {"refresh_token"}}); rec.Code != http.StatusOK {
		t.Fatalf("the owner's revocation = %d, want 200", rec.Code)
	}
	if _, err := v.Verify(ctx, VerifyRequest{Token: token, Audience: audienceData}); !errors.Is(err, ErrRevoked) {
		t.Fatalf("after revocation: err = %v, want ErrRevoked", err)
	}
	// §2.2: an invalid token is answered 200 too.
	if rec := postForm(t, h, owner, url.Values{paramToken: {"not-a-token"}}); rec.Code != http.StatusOK {
		t.Errorf("an invalid token = %d, want 200", rec.Code)
	}
	checkEndpointRefusals(t, h)
}

// A Biscuit presented for revocation revokes that copy, not its capability.
func TestRevocationHandlerRevokesOneBiscuitCopy(t *testing.T) {
	_, v, store, c, token := biscuitFixture(t)
	h, err := RevocationHandler(RevocationConfig{Verifier: v, Store: store, Biscuits: store, Authenticate: testAuthenticate, Authorize: sameTenant})
	if err != nil {
		t.Fatal(err)
	}
	narrowed, err := Attenuate(token, Attenuation{Ops: []Op{OpGet}})
	if err != nil {
		t.Fatal(err)
	}
	if rec := postForm(t, h, c.Subject.TenantID.String(), url.Values{paramToken: {narrowed}}); rec.Code != http.StatusOK {
		t.Fatalf("revocation = %d", rec.Code)
	}
	ctx := context.Background()
	if _, err := v.Verify(ctx, VerifyRequest{Token: narrowed, Audience: audienceData}); !errors.Is(err, ErrRevoked) {
		t.Errorf("the revoked copy: err = %v, want ErrRevoked", err)
	}
	if _, err := v.Verify(ctx, VerifyRequest{Token: token, Audience: audienceData}); err != nil {
		t.Errorf("the copy it came from was revoked with it: %v", err)
	}
}

func TestIntrospectionHandler(t *testing.T) {
	issuer, v, store, _ := buildIssuerVerifier(t)
	h, err := IntrospectionHandler(IntrospectionConfig{Verifier: v, Authenticate: testAuthenticate, Authorize: sameTenant})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	key := dpopKeys(t)["Ed25519"]
	bound := issueBound(t, issuer, key)
	got, err := v.Introspect(ctx, bound)
	if err != nil {
		t.Fatal(err)
	}
	owner := got.Subject.TenantID.String()
	introspect := func(client, token string) map[string]any {
		t.Helper()
		rec := postForm(t, h, client, url.Values{paramToken: {token}})
		if rec.Code != http.StatusOK || rec.Header().Get(headerCacheControl) != directiveNoStore {
			t.Fatalf("introspection = %d %v", rec.Code, rec.Header())
		}
		var out map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	// A bound token introspects without its proof: that is the resource
	// server's to check.
	in := introspect(owner, bound)
	if in["active"] != true || in["token_type"] != tokenTypeDPoP || in["cnf"].(map[string]any)["jkt"] != got.ConfirmationJKT ||
		in["sub"] != got.Authority() || in["iss"] != got.Issuer || in["jti"] != got.ID.String() {
		t.Errorf("introspection = %v", in)
	}
	details := in["authorization_details"].([]any)[0].(map[string]any)
	if details["type"] != AuthorizationDetailsType || details["actions"].([]any)[0] != string(OpGet) {
		t.Errorf("authorization_details = %v", details)
	}

	inactive := map[string]any{"active": false}
	if out := introspect(uuid.NewString(), bound); len(out) != 1 || out["active"] != inactive["active"] {
		t.Errorf("a client that may not see it = %v, want only active false", out)
	}
	if err := store.Revoke(ctx, RevokeRequest{ID: got.ID}); err != nil {
		t.Fatal(err)
	}
	if out := introspect(owner, bound); out["active"] != false {
		t.Errorf("a revoked token = %v, want inactive", out)
	}
	if out := introspect(owner, "garbage"); out["active"] != false {
		t.Errorf("garbage = %v, want inactive", out)
	}
	checkEndpointRefusals(t, h)
}

// Both endpoints take an authenticated form POST with a token, and answer
// anything else with an RFC 6749 §5.2 error.
func checkEndpointRefusals(t *testing.T, h http.Handler) {
	t.Helper()
	client := uuid.NewString()
	if rec := postForm(t, h, "", url.Values{paramToken: {"x"}}); rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), codeInvalidClient) {
		t.Errorf("no client = %d %s, want 401 invalid_client", rec.Code, rec.Body)
	}
	if rec := postForm(t, h, client, url.Values{}); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), codeInvalidRequest) {
		t.Errorf("no token = %d %s, want 400 invalid_request", rec.Code, rec.Body)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET = %d, want 405", rec.Code)
	}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/", strings.NewReader(`{"token":"x"}`))
	req.Header.Set("Content-Type", mediaTypeJSON)
	req.Header.Set(clientHeader, client)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("a JSON body = %d, want 415", rec.Code)
	}
}

func TestEndpointsNeedTheirWiring(t *testing.T) {
	if _, err := RevocationHandler(RevocationConfig{}); err == nil {
		t.Error("RevocationHandler with nothing wired")
	}
	if _, err := IntrospectionHandler(IntrospectionConfig{}); err == nil {
		t.Error("IntrospectionHandler with nothing wired")
	}
}

// An expired token can still be revoked: the endpoint proves only that the
// token is genuine.
func TestRevocationHandlerRevokesAnExpiredToken(t *testing.T) {
	issuer, v, store, _ := buildIssuerVerifier(t)
	c, token := issueFor(t, issuer)
	cfg := v.cfg
	cfg.Now = func() time.Time { return c.ExpiresAt.Add(time.Hour) }
	late, err := NewStandardVerifier(cfg)
	if err != nil {
		t.Fatal(err)
	}
	h, err := RevocationHandler(RevocationConfig{Verifier: late, Store: store, Authenticate: testAuthenticate, Authorize: sameTenant})
	if err != nil {
		t.Fatal(err)
	}
	postForm(t, h, c.Subject.TenantID.String(), url.Values{paramToken: {token}})
	if revoked, err := store.IsRevoked(context.Background(), c.ID); err != nil || !revoked {
		t.Errorf("an expired token was not revoked: %v, %v", revoked, err)
	}
}
