package jwks

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oleg-tkachuk/limes/internal/httpx"
)

// RFC 9111: max-age less Age, no-cache and no-store as zero, an invalid
// max-age as stale, and nothing said as nothing.
func TestResponseFreshness(t *testing.T) {
	cases := []struct {
		cacheControl, age string
		want              time.Duration
		ok                bool
	}{
		{"max-age=60", "", time.Minute, true},
		{"public, max-age=60", "15", 45 * time.Second, true},
		{`MAX-AGE="30"`, "", 30 * time.Second, true},
		{"max-age=10", "60", 0, true},
		{"no-cache", "", 0, true},
		{"max-age=60, no-store", "", 0, true},
		{"max-age=abc", "", 0, true},
		{"public", "", 0, false},
		// RFC 9111 §4.2.1: the first max-age stands.
		{"max-age=10, max-age=3600", "", 10 * time.Second, true},
		// §1.2.2: a delta-seconds too large to hold reads as 2^31.
		{"max-age=99999999999999999999", "", time.Duration(maxDeltaSeconds) * time.Second, true},
		{"", "", 0, false},
	}
	for _, tc := range cases {
		h := http.Header{}
		if tc.cacheControl != "" {
			h.Set(httpx.HeaderCacheControl, tc.cacheControl)
		}
		if tc.age != "" {
			h.Set(httpx.HeaderAge, tc.age)
		}
		got, ok := responseFreshness(h)
		if got != tc.want || ok != tc.ok {
			t.Errorf("Cache-Control %q, Age %q = %v, %v; want %v, %v", tc.cacheControl, tc.age, got, ok, tc.want, tc.ok)
		}
	}
}

// The resolver asks for the registered JWK Set media type and keeps a set
// only as long as the response's Cache-Control allows, within its bounds.
func TestRemoteJWKSHonoursTheResponse(t *testing.T) {
	kid, pub, _, err := generateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	var fetches atomic.Int32
	var accept atomic.Value
	const maxAge = "60"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetches.Add(1)
		accept.Store(r.Header.Get("Accept"))
		doc, _ := Marshal(map[string]ed25519.PublicKey{kid: pub})
		w.Header().Set(httpx.HeaderCacheControl, "max-age="+maxAge)
		_, _ = w.Write(doc)
	}))
	defer srv.Close()
	clock := &stepClock{t: time.Unix(1_800_000_000, 0)}
	r, err := NewRemote(RemoteConfig{
		URL: srv.URL, Now: clock.Now,
		MinRefreshInterval: 10 * time.Second, RefreshInterval: 5 * time.Minute, MaxStale: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := r.PublicKey(ctx, kid); err != nil {
		t.Fatal(err)
	}
	if got := accept.Load(); !strings.HasPrefix(got.(string), mediaTypeJWKSet) {
		t.Errorf("Accept = %q, want %s first", got, mediaTypeJWKSet)
	}
	clock.Advance(30 * time.Second)
	_, _ = r.PublicKey(ctx, kid)
	if n := fetches.Load(); n != 1 {
		t.Errorf("%d fetches within max-age, want 1", n)
	}
	clock.Advance(31 * time.Second) // past max-age, well before RefreshInterval
	_, _ = r.PublicKey(ctx, kid)
	awaitFetches(t, fetches.Load, 2)
}

// A max-age outside the resolver's bounds is clamped to them.
func TestRemoteJWKSClampsFreshness(t *testing.T) {
	r, err := NewRemote(RemoteConfig{
		URL: "https://issuer.example/jwks", MinRefreshInterval: 10 * time.Second, RefreshInterval: time.Minute, MaxStale: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	for cc, want := range map[string]time.Duration{"max-age=1": 10 * time.Second, "max-age=86400": time.Minute, "": time.Minute, "no-store": 10 * time.Second} {
		h := http.Header{}
		if cc != "" {
			h.Set(httpx.HeaderCacheControl, cc)
		}
		if got := r.freshness(h); got != want {
			t.Errorf("freshness(%q) = %v, want %v", cc, got, want)
		}
	}
}

func TestJWKSHandler(t *testing.T) {
	kid, pub, _, err := generateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewHandler(HandlerConfig{
		Keys:   func() map[string]ed25519.PublicKey { return map[string]ed25519.PublicKey{kid: pub} },
		MaxAge: 5 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/jwks", nil))
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != mediaTypeJWKSet ||
		rec.Header().Get(httpx.HeaderCacheControl) != "public, max-age=300" {
		t.Fatalf("GET = %d %v", rec.Code, rec.Header())
	}
	keys, err := Parse(rec.Body.Bytes())
	if err != nil || !keys[kid].Equal(pub) {
		t.Fatalf("served set = %v, %v", keys, err)
	}
	etag := rec.Header().Get("ETag")

	for _, inm := range []string{etag, "W/" + etag, `"other", ` + etag, "*"} {
		rec = httptest.NewRecorder()
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/jwks", nil)
		req.Header.Set("If-None-Match", inm)
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
			t.Errorf("If-None-Match %s = %d, want 304 with no body", inm, rec.Code)
		}
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/jwks", nil))
	if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") == "" {
		t.Errorf("POST = %d, Allow %q; want 405 with Allow", rec.Code, rec.Header().Get("Allow"))
	}

	// The resolver reads what the handler serves, and revalidates by ETag.
	srv := httptest.NewServer(h)
	defer srv.Close()
	r, err := NewRemote(RemoteConfig{URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := r.PublicKey(context.Background(), kid); err != nil || !got.Equal(pub) {
		t.Errorf("resolver over the handler = %v, %v", got, err)
	}
}

// A response whose every entry is unusable — a key type this module does not
// read, say — is a failed fetch, not an empty key set: the last good set
// keeps serving within MaxStale, as for any other failure.
func TestRemoteJWKSKeepsTheSetWhenNoKeyIsUsable(t *testing.T) {
	kid, pub, _, err := generateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	var foreign atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if foreign.Load() {
			_, _ = w.Write([]byte(`{"keys":[{"kty":"OKP","crv":"Ed448","kid":"k2","x":"AA"}]}`))
			return
		}
		doc, _ := Marshal(map[string]ed25519.PublicKey{kid: pub})
		_, _ = w.Write(doc)
	}))
	defer srv.Close()
	clock := &stepClock{t: time.Unix(1_800_000_000, 0)}
	r, err := NewRemote(RemoteConfig{URL: srv.URL, Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := r.PublicKey(ctx, kid); err != nil {
		t.Fatal(err)
	}
	foreign.Store(true)
	clock.Advance(10 * time.Minute) // past RefreshInterval, inside MaxStale
	if got, err := r.PublicKey(ctx, kid); err != nil || !got.Equal(pub) {
		t.Fatalf("after a response with no usable key: %v, %v; want the last good key", got, err)
	}
	// An empty set is an issuer withdrawing every key, and is taken as such.
	if keys, skipped, err := parse([]byte(`{"keys":[]}`)); err != nil || len(keys) != 0 || skipped != 0 {
		t.Errorf("an empty set = %v, %d, %v", keys, skipped, err)
	}
}

// The Cache-Control a handler sends: DefaultMaxAge when it names none, and
// max-age=0 only when told to revalidate.
func TestHandlerMaxAge(t *testing.T) {
	keys := func() map[string]ed25519.PublicKey { return nil }
	for name, tc := range map[string]struct {
		cfg  HandlerConfig
		want string
	}{
		"none named": {HandlerConfig{Keys: keys}, fmt.Sprintf("public, max-age=%d", int64(DefaultMaxAge/time.Second))},
		"revalidate": {HandlerConfig{Keys: keys, Revalidate: true}, "public, max-age=0"},
		"a minute":   {HandlerConfig{Keys: keys, MaxAge: time.Minute}, "public, max-age=60"},
	} {
		h, err := NewHandler(tc.cfg)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/jwks", nil))
		if got := rec.Header().Get(httpx.HeaderCacheControl); got != tc.want {
			t.Errorf("%s: Cache-Control = %q, want %q", name, got, tc.want)
		}
	}
}

// A handler with no keys, or a negative max-age — an invalid Cache-Control
// on every response — is refused; a remote resolver with a negative
// interval or bound likewise.
func TestConfigsRefuseNegatives(t *testing.T) {
	keys := func() map[string]ed25519.PublicKey { return nil }
	for name, cfg := range map[string]HandlerConfig{
		"no keys":                   {},
		"negative max-age":          {Keys: keys, MaxAge: -time.Second},
		"max-age beside revalidate": {Keys: keys, MaxAge: time.Second, Revalidate: true},
	} {
		if _, err := NewHandler(cfg); err == nil {
			t.Errorf("NewHandler, %s: accepted", name)
		}
	}
	const url = "https://issuer.example.com/jwks"
	for name, cfg := range map[string]RemoteConfig{
		"negative refresh":     {URL: url, RefreshInterval: -time.Second},
		"negative min refresh": {URL: url, MinRefreshInterval: -time.Second},
		"negative max stale":   {URL: url, MaxStale: -time.Second},
		"negative max bytes":   {URL: url, MaxDocumentBytes: -1},
	} {
		if _, err := NewRemote(cfg); err == nil {
			t.Errorf("NewRemote, %s: accepted", name)
		}
	}
}
