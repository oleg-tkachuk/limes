package limes

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
	"uuid"

	"github.com/oleg-tkachuk/limes/jwks"
)

// keysWait is how long a request waits on a key endpoint that never answers.
const keysWait = 50 * time.Millisecond

// A request that times out waiting on the issuer's key endpoint, with no
// key set held, is answered as keys unavailable — the server's fault — not
// as an internal error.
func TestKeysTimeoutIsUnavailable(t *testing.T) {
	release := make(chan struct{})
	hang := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	t.Cleanup(hang.Close)
	t.Cleanup(func() { close(release) })
	keys, err := jwks.NewRemote(jwks.RemoteConfig{URL: hang.URL, Client: hang.Client()})
	if err != nil {
		t.Fatal(err)
	}
	issuer, configured, _, _ := buildIssuerVerifier(t)
	cfg := configured.cfg
	cfg.Keys = keys
	v, err := NewVerifier(cfg)
	if err != nil {
		t.Fatal(err)
	}
	issued, err := issuer.Issue(t.Context(), IssueRequest{ClientID: testClientID,
		IssuedBy: Principal{Subject: "op"}, Subject: Principal{Type: PrincipalTypeAgent, TenantID: uuid.New(), Subject: "a"},
		Audience: []string{audienceData}, Caveats: Caveats{Ops: []Op{OpGet}}, TTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), keysWait)
	defer cancel()
	_, err = v.Verify(ctx, VerifyRequest{Token: issued.Token, Audience: audienceData})
	if resp := v.ErrorResponse(t.Context(), err, AuthSchemeBearer); resp.Status != http.StatusServiceUnavailable {
		t.Errorf("a key endpoint timing out: %v answered %d, want %d", err, resp.Status, http.StatusServiceUnavailable)
	}
}
