package jwtverify_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jws"

	"github.com/oleg-tkachuk/limes/exchange/jwtverify"
	"github.com/oleg-tkachuk/limes/jwks"
)

// jwtverify and jwks.Remote fetch keys by one rule (internal/httpsurl): an
// https URL with a host, and redirects that keep to its scheme and host. One
// table of cases runs through both, by their exported API alone, and each
// must reach the same verdict: a fetch path that stops applying the rule
// fails here.

const (
	// parityKID is the kid the served key set names and the token carries.
	parityKID = "parity-key"
	// parityIssuer, paritySubject, parityAudience and parityClient are the
	// claims of the token the exchange's copy verifies.
	parityIssuer   = "https://idp.example.com"
	paritySubject  = "user-1"
	parityAudience = "https://mcp.example.com"
	parityClient   = "mcp-client"
	// parityTokenTTL is the token's lifetime.
	parityTokenTTL = 5 * time.Minute
	// Paths the key set and the redirects to it are served at.
	keysPath  = "/jwks.json"
	movedPath = "/moved"
	loopPath  = "/loop"
	// certHost is a host name httptest's certificate is valid for, and
	// certHostUpper the same host in another case.
	certHost      = "example.com"
	certHostUpper = "EXAMPLE.com"
	// jsonContentType is a JWKS document's media type.
	jsonContentType = "application/json"
	// Claim names of an RFC 9068 access token.
	claimIssuer   = "iss"
	claimSubject  = "sub"
	claimAudience = "aud"
	claimIssuedAt = "iat"
	claimExpiry   = "exp"
	claimJWTID    = "jti"
	parityJTI     = "parity-token"
)

// verdict is whether a copy fetched the keys from a case's URL.
type verdict string

const (
	accepted verdict = "accepted"
	refused  verdict = "refused"
)

// parityKeys is the identity provider both copies fetch from: its key, the
// key set it serves, and a token it signed.
type parityKeys struct {
	doc   []byte
	token string
}

func newParityKeys(t *testing.T) parityKeys {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	k, err := jwk.Import(pub)
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]any{jwk.KeyIDKey: parityKID, jwk.AlgorithmKey: jwa.EdDSAEd25519()} {
		if err := k.Set(name, value); err != nil {
			t.Fatal(err)
		}
	}
	set := jwk.NewSet()
	if err := set.AddKey(k); err != nil {
		t.Fatal(err)
	}
	doc, err := json.Marshal(set)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	payload, err := json.Marshal(map[string]any{
		claimIssuer: parityIssuer, claimSubject: paritySubject, claimAudience: parityAudience,
		jwtverify.ClaimClientID: parityClient, claimIssuedAt: now.Unix(), claimExpiry: now.Add(parityTokenTTL).Unix(), claimJWTID: parityJTI,
	})
	if err != nil {
		t.Fatal(err)
	}
	headers := jws.NewHeaders()
	for name, value := range map[string]any{jws.TypeKey: jwtverify.TypeAccessToken, jws.KeyIDKey: parityKID} {
		if err := headers.Set(name, value); err != nil {
			t.Fatal(err)
		}
	}
	token, err := jws.Sign(payload, jws.WithKey(jwa.EdDSAEd25519(), key, jws.WithProtectedHeaders(headers)))
	if err != nil {
		t.Fatal(err)
	}
	return parityKeys{doc: doc, token: string(token)}
}

// serve answers keysPath with the key set.
func (p parityKeys) serve(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", jsonContentType)
	_, _ = w.Write(p.doc)
}

// redirect answers every request with a redirect to target.
func redirect(target string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target, http.StatusFound)
	}
}

// parityCase is a URL both copies are pointed at, and the client they fetch
// it with; want is the verdict the rule gives.
type parityCase struct {
	url    string
	client *http.Client
	want   verdict
}

// throughJWKS is jwks.Remote's verdict on c.
func throughJWKS(t *testing.T, c parityCase) (verdict, error) {
	t.Helper()
	r, err := jwks.NewRemote(jwks.RemoteConfig{URL: c.url, Client: c.client})
	if err != nil {
		return refused, err
	}
	if _, err := r.PublicKey(t.Context(), parityKID); err != nil {
		return refused, err
	}
	return accepted, nil
}

// throughJWTVerify is jwtverify.RemoteKeys' verdict on c.
func throughJWTVerify(t *testing.T, c parityCase, token string) (verdict, error) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	keys, err := jwtverify.RemoteKeys(ctx, c.url, c.client)
	if err != nil {
		return refused, err
	}
	v, err := jwtverify.New(jwtverify.Config{Issuer: parityIssuer, Keys: keys})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.VerifySubject(ctx, token); err != nil {
		return refused, err
	}
	return accepted, nil
}

// dialing is a client of srv's that dials srv whatever host a URL names, so
// a URL under certHost reaches it.
func dialing(srv *httptest.Server) *http.Client {
	tr := srv.Client().Transport.(*http.Transport).Clone()
	addr := srv.Listener.Addr().String()
	tr.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}
	return &http.Client{Transport: tr}
}

// underHost is srv's URL with its host named host.
func underHost(t *testing.T, srv *httptest.Server, host string) string {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	u.Host = host + ":" + u.Port()
	return u.String()
}

func TestOriginRuleMatchesJWKS(t *testing.T) {
	p := newParityKeys(t)

	mux := http.NewServeMux()
	mux.HandleFunc(keysPath, p.serve)
	mux.Handle(movedPath, redirect(keysPath))
	mux.Handle(loopPath, redirect(loopPath))
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)

	plain := httptest.NewServer(http.HandlerFunc(p.serve))
	t.Cleanup(plain.Close)
	other := httptest.NewTLSServer(http.HandlerFunc(p.serve))
	t.Cleanup(other.Close)
	toPlain := httptest.NewTLSServer(redirect(plain.URL + keysPath))
	t.Cleanup(toPlain.Close)
	toOther := httptest.NewTLSServer(redirect(other.URL + keysPath))
	t.Cleanup(toOther.Close)
	byName := dialing(srv)
	// The redirect names the host in another case: the same origin.
	caseMux := http.NewServeMux()
	caseMux.HandleFunc(keysPath, p.serve)
	caseSrv := httptest.NewTLSServer(caseMux)
	t.Cleanup(caseSrv.Close)
	caseMux.Handle(movedPath, redirect(underHost(t, caseSrv, certHostUpper)+keysPath))

	refusing := &http.Client{Transport: srv.Client().Transport, CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("the client's own policy refuses every redirect")
	}}

	cases := map[string]parityCase{
		"https with a host":                       {url: srv.URL + keysPath, client: srv.Client(), want: accepted},
		"plain http":                              {url: plain.URL + keysPath, client: plain.Client(), want: refused},
		"https with no host":                      {url: "https://" + keysPath, client: srv.Client(), want: refused},
		"no scheme":                               {url: strings.TrimPrefix(srv.URL, "https://") + keysPath, client: srv.Client(), want: refused},
		"a redirect within the origin":            {url: srv.URL + movedPath, client: srv.Client(), want: accepted},
		"a redirect to the host in another case":  {url: underHost(t, caseSrv, certHost) + movedPath, client: dialing(caseSrv), want: accepted},
		"a redirect to plain http":                {url: toPlain.URL + keysPath, client: toPlain.Client(), want: refused},
		"a redirect to another host":              {url: toOther.URL + keysPath, client: toOther.Client(), want: refused},
		"redirects past net/http's limit":         {url: srv.URL + loopPath, client: srv.Client(), want: refused},
		"a redirect the client's policy refuses":  {url: srv.URL + movedPath, client: refusing, want: refused},
		"https under a host name the cert covers": {url: underHost(t, srv, certHost) + keysPath, client: byName, want: accepted},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			viaJWKS, jwksErr := throughJWKS(t, c)
			viaExchange, exchangeErr := throughJWTVerify(t, c, p.token)
			if viaJWKS != viaExchange {
				t.Fatalf("jwks.Remote %s (%v), jwtverify.RemoteKeys %s (%v): the copies disagree", viaJWKS, jwksErr, viaExchange, exchangeErr)
			}
			if viaJWKS != c.want {
				t.Fatalf("both copies %s (jwks: %v; jwtverify: %v), want %s", viaJWKS, jwksErr, exchangeErr, c.want)
			}
		})
	}
}
