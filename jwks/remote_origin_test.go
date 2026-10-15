package jwks

import (
	"context"
	"crypto/ed25519"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// The key set is fetched over https only: any other URL is refused when the
// resolver is built, before anything is fetched.
func TestNewRemoteRequiresHTTPS(t *testing.T) {
	for _, u := range []string{
		"http://issuer.example/jwks",
		"HTTP://issuer.example/jwks",
		"ftp://issuer.example/jwks",
		"//issuer.example/jwks",
		"issuer.example/jwks",
		"https:///jwks",
		"https://issuer.example/%zz",
	} {
		if _, err := NewRemote(RemoteConfig{URL: u}); err == nil {
			t.Errorf("NewRemote(%q) accepted", u)
		}
	}
	if _, err := NewRemote(RemoteConfig{URL: "https://issuer.example/.well-known/jwks.json"}); err != nil {
		t.Errorf("NewRemote refused an https URL: %v", err)
	}
}

// redirectTo answers every request with a redirect to target.
func redirectTo(target string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target, http.StatusFound)
	})
}

// remoteBehindRedirect starts a TLS server that redirects every request to
// target, and returns a resolver of it whose client has own as its policy.
func remoteBehindRedirect(t *testing.T, target string, own func(*http.Request, []*http.Request) error) *Remote {
	t.Helper()
	front := httptest.NewTLSServer(redirectTo(target))
	t.Cleanup(front.Close)
	client := front.Client()
	client.CheckRedirect = own
	r, err := NewRemote(RemoteConfig{URL: front.URL + "/moved", Client: client})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// A redirect to plain http or to another host is not followed: the server,
// or whoever can steer it, would choose the keys the verifier trusts.
func TestRemoteRefusesARedirectThatLeavesTheOrigin(t *testing.T) {
	kid, pub, _, err := generateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	served := &jwksServer{keys: map[string]ed25519.PublicKey{kid: pub}}
	plain := httptest.NewServer(served)
	t.Cleanup(plain.Close)
	other := httptest.NewTLSServer(served)
	t.Cleanup(other.Close)

	for name, target := range map[string]string{"to http": plain.URL, "to another host": other.URL} {
		r := remoteBehindRedirect(t, target, nil)
		if _, err := r.PublicKey(context.Background(), kid); !errors.Is(err, ErrUnavailable) {
			t.Errorf("%s: PublicKey = %v, want ErrUnavailable", name, err)
		}
	}
	if n := served.fetches.Load(); n != 0 {
		t.Errorf("the redirect target was fetched %d times, want 0", n)
	}
}

// A redirect within the origin is followed, and the client the caller passed
// in keeps its own redirect policy, which is still consulted.
func TestRemoteFollowsARedirectWithinTheOrigin(t *testing.T) {
	kid, pub, _, err := generateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	served := &jwksServer{keys: map[string]ed25519.PublicKey{kid: pub}}
	mux := http.NewServeMux()
	mux.Handle("/moved", redirectTo("/jwks"))
	mux.Handle("/jwks", served)
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)

	var consulted atomic.Int32
	client := srv.Client()
	own := func(*http.Request, []*http.Request) error { consulted.Add(1); return nil }
	client.CheckRedirect = own
	r, err := NewRemote(RemoteConfig{URL: srv.URL + "/moved", Client: client})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := r.PublicKey(context.Background(), kid); err != nil || !got.Equal(pub) {
		t.Fatalf("PublicKey = %v, %v", got, err)
	}
	if n := consulted.Load(); n != 1 {
		t.Errorf("the client's own CheckRedirect ran %d times, want 1", n)
	}
	if r.cfg.Client == client {
		t.Error("NewRemote changed the caller's client rather than a copy")
	}

	// The caller's policy can still stop a redirect the origin check allows.
	stop := errors.New("stop")
	r = remoteBehindRedirect(t, "/jwks", func(*http.Request, []*http.Request) error { return stop })
	if _, err := r.PublicKey(context.Background(), kid); !errors.Is(err, ErrUnavailable) {
		t.Errorf("own policy refusing: PublicKey = %v, want ErrUnavailable", err)
	}
}

// With no policy of its own, a client follows at most maxRedirects, as
// net/http's default does.
func TestRemoteStopsAnEndlessRedirect(t *testing.T) {
	var hops atomic.Int32
	loop := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hops.Add(1)
		http.Redirect(w, r, "/again", http.StatusFound)
	}))
	t.Cleanup(loop.Close)
	r, err := NewRemote(RemoteConfig{URL: loop.URL, Client: loop.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.PublicKey(context.Background(), "any"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("PublicKey = %v, want ErrUnavailable", err)
	}
	if n := hops.Load(); n != maxRedirects {
		t.Errorf("%d requests, want %d", n, maxRedirects)
	}
}
