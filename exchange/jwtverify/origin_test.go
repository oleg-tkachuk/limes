package jwtverify

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/internal/httpsurl"
	"github.com/oleg-tkachuk/limes/jwks"
)

// fetchChains is how many fetches a test's key source may run at once: the
// one in the background, and the one a token starts.
const fetchChains = 2

// urlPassword is the secret a JWKS URL carries in its userinfo.
const urlPassword = "s3cret-in-the-url"

// The paths of a server that redirects to its own key set.
const (
	movedPath = "/moved"
	keysPath  = "/jwks"
)

// The identity provider's keys are fetched over https only: any other URL
// is refused as a configuration error when the key source is built, before
// anything is fetched, and the error names no password the URL carries.
func TestRemoteKeysRequireHTTPS(t *testing.T) {
	var fetches atomic.Int64
	plain := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { fetches.Add(1) }))
	t.Cleanup(plain.Close)
	for name, u := range map[string]string{
		"http":                 plain.URL + keysPath,
		"http in capitals":     strings.Replace(plain.URL, "http://", "HTTP://", 1) + keysPath,
		"another scheme":       "ftp://idp.example.com/jwks",
		"no scheme":            "//idp.example.com/jwks",
		"no host":              "https:///jwks",
		"a relative path":      "idp.example.com/jwks",
		"not a URL":            "https://idp.example.com/%zz",
		"http with a password": "http://user:" + urlPassword + "@idp.example.com/jwks",
		"unparsed, a password": "https://user:" + urlPassword + "@idp.example.com/%zz",
	} {
		_, err := RemoteKeys(t.Context(), u, nil)
		if !errors.Is(err, limes.ErrInvalidConfig) {
			t.Errorf("%s: err = %v, want ErrInvalidConfig", name, err)
			continue
		}
		if strings.Contains(err.Error(), urlPassword) {
			t.Errorf("%s: err = %q, names the password", name, err)
		}
	}
	if n := fetches.Load(); n != 0 {
		t.Errorf("the http URL was fetched %d times, want 0", n)
	}
}

// redirectTo answers every request with a redirect to target.
func redirectTo(target string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target, http.StatusFound)
	})
}

// A redirect to plain http or to another host is not followed: the server,
// or whoever can steer it, would choose the keys the exchange trusts. The
// keys are the server's failure, not a refusal of the token.
func TestRemoteKeysRefuseARedirectThatLeavesTheOrigin(t *testing.T) {
	p := newIDP(t)
	served := newJWKSServer(t, p.keys)
	plain := httptest.NewServer(served.Config.Handler)
	t.Cleanup(plain.Close)
	for name, target := range map[string]string{"to http": plain.URL, "to another host": served.URL} {
		front := httptest.NewTLSServer(redirectTo(target))
		t.Cleanup(front.Close)
		r, err := newRemote(t.Context(), front.URL+movedPath, front.Client(), clock, slog.New(&messages{}))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.keys(t.Context()); !errors.Is(err, jwks.ErrUnavailable) {
			t.Errorf("%s: keys = %v, want jwks.ErrUnavailable", name, err)
		}
	}
	if n := served.fetches.Load(); n != 0 {
		t.Errorf("the redirect target was fetched %d times, want 0", n)
	}
}

// A redirect within the origin is followed. The caller's client keeps its
// own redirect policy, which is still consulted, and is not changed: the
// origin check is on a copy.
func TestRemoteKeysFollowARedirectWithinTheOrigin(t *testing.T) {
	p := newIDP(t)
	doc, err := json.Marshal(p.keys)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle(movedPath, redirectTo(keysPath))
	mux.Handle(keysPath, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", jsonContentType)
		_, _ = w.Write(doc)
	}))
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)

	var consulted atomic.Int64
	client := srv.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { consulted.Add(1); return nil }
	r, err := newRemote(t.Context(), srv.URL+movedPath, client, clock, slog.New(&messages{}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.keys(t.Context()); err != nil {
		t.Fatalf("keys behind a redirect within the origin: %v", err)
	}
	if consulted.Load() == 0 {
		t.Error("the client's own CheckRedirect was never consulted")
	}

	// srv.Client is one client, whose policy the test set above: a fresh one
	// over its transport.
	bare := &http.Client{Transport: srv.Client().Transport}
	if _, err := newRemote(t.Context(), srv.URL+movedPath, bare, clock, slog.New(&messages{})); err != nil {
		t.Fatal(err)
	}
	if bare.CheckRedirect != nil {
		t.Error("newRemote set a redirect policy on the caller's client rather than a copy")
	}
}

// The caller's own policy can still stop a redirect the origin check allows.
func TestRemoteKeysKeepTheClientsRedirectPolicy(t *testing.T) {
	front := httptest.NewTLSServer(redirectTo(keysPath))
	t.Cleanup(front.Close)
	stop := errors.New("stop")
	client := front.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return stop }
	r, err := newRemote(t.Context(), front.URL+movedPath, client, clock, slog.New(&messages{}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.keys(t.Context()); !errors.Is(err, jwks.ErrUnavailable) {
		t.Errorf("own policy refusing: keys = %v, want jwks.ErrUnavailable", err)
	}
}

// With no policy of its own, a client follows at most
// httpsurl.MaxRedirects, as net/http's default does.
func TestRemoteKeysStopAnEndlessRedirect(t *testing.T) {
	var hops atomic.Int64
	loop := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hops.Add(1)
		http.Redirect(w, r, movedPath, http.StatusFound)
	}))
	t.Cleanup(loop.Close)
	r, err := newRemote(t.Context(), loop.URL+movedPath, loop.Client(), clock, slog.New(&messages{}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.keys(t.Context()); !errors.Is(err, jwks.ErrUnavailable) {
		t.Fatalf("keys = %v, want jwks.ErrUnavailable", err)
	}
	// The background fetch may run a chain of its own beside the token's.
	if n := hops.Load(); n == 0 || n > fetchChains*httpsurl.MaxRedirects {
		t.Errorf("%d requests, want at most %d chains of %d", n, fetchChains, httpsurl.MaxRedirects)
	}
}
