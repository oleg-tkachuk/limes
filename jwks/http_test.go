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
	"testing/synctest"
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
		// §5.1: of a list-valued Age, the first member.
		{"max-age=60", "30, 40", 30 * time.Second, true},
		{"max-age=60", " 15 ,", 45 * time.Second, true},
		{"max-age=60", "x, 10", time.Minute, true},
		{"no-cache", "", 0, true},
		{"max-age=60, no-store", "", 0, true},
		{"max-age=abc", "", 0, true},
		// §5.2: a quoted value only between two quotes; one left open, or
		// closed without opening, is invalid.
		{`max-age="5`, "", 0, true},
		{`max-age=5"`, "", 0, true},
		{`max-age="60`, "", 0, true},
		{`max-age=60"`, "", 0, true},
		{`max-age="`, "", 0, true},
		{`max-age=""`, "", 0, true},
		// A comma inside a quoted string separates no directives.
		{`ext="a,max-age=1", max-age=60`, "", time.Minute, true},
		{`ext="x,no-store,y", max-age=60`, "", time.Minute, true},
		{`ext="a\",max-age=1", max-age=60`, "", time.Minute, true},
		// A quoted string never closed runs to the end of the field.
		{`ext="a, max-age=60`, "", 0, false},
		// RFC 9110 §5.6.1: empty members are allowed, leading one included.
		{",max-age=60", "", time.Minute, true},
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
	synctest.Test(t, func(t *testing.T) {
		kid, pub, _, err := generateKeypair()
		if err != nil {
			t.Fatal(err)
		}
		var fetches atomic.Int32
		var accept atomic.Value
		const maxAge = "60"
		srv := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fetches.Add(1)
			accept.Store(r.Header.Get("Accept"))
			doc, _ := Marshal(map[string]ed25519.PublicKey{kid: pub})
			w.Header().Set(httpx.HeaderCacheControl, "max-age="+maxAge)
			_, _ = w.Write(doc)
		})
		clock := &stepClock{t: time.Unix(1_800_000_000, 0)}
		r, err := NewRemote(RemoteConfig{
			URL: fixtureURL, Client: handlerClient(srv), Now: clock.Now,
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
	})
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
	srv := httptest.NewTLSServer(h)
	defer srv.Close()
	r, err := NewRemote(RemoteConfig{URL: srv.URL, Client: srv.Client()})
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
	// served, when set, is the body every response carries.
	var served atomic.Pointer[string]
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if body := served.Load(); body != nil {
			_, _ = w.Write([]byte(*body))
			return
		}
		if foreign.Load() {
			_, _ = w.Write([]byte(`{"keys":[{"kty":"OKP","crv":"Ed448","kid":"k2","x":"AA"}]}`))
			return
		}
		doc, _ := Marshal(map[string]ed25519.PublicKey{kid: pub})
		_, _ = w.Write(doc)
	}))
	defer srv.Close()
	clock := &stepClock{t: time.Unix(1_800_000_000, 0)}
	r, err := NewRemote(RemoteConfig{URL: srv.URL, Client: srv.Client(), Now: clock.Now})
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
	// A document with no "keys" array is broken too (RFC 7517 §5), not a
	// withdrawal: the last good key is still served.
	for _, body := range []string{`null`, `{}`, `{"keys":null}`} {
		served.Store(&body)
		clock.Advance(10 * time.Minute)
		if got, err := r.PublicKey(ctx, kid); err != nil || !got.Equal(pub) {
			t.Fatalf("after a response of %s: %v, %v; want the last good key", body, got, err)
		}
	}
	// An empty set is an issuer withdrawing every key, and is taken as such.
	if keys, skipped, err := parse([]byte(`{"keys":[]}`)); err != nil || len(keys) != 0 || skipped != 0 {
		t.Errorf("an empty set = %v, %d, %v", keys, skipped, err)
	}
}

// The Cache-Control a handler sends: DefaultMaxAge when it names none, and
// no-cache only when told to revalidate — max-age=0 would still let a cache
// serve the set stale (RFC 9111 §4.2.4). Remote reads each as served.
func TestHandlerMaxAge(t *testing.T) {
	keys := func() map[string]ed25519.PublicKey { return nil }
	for name, tc := range map[string]struct {
		cfg   HandlerConfig
		want  string
		fresh time.Duration
	}{
		"none named": {HandlerConfig{Keys: keys}, fmt.Sprintf("public, max-age=%d", int64(DefaultMaxAge/time.Second)), DefaultMaxAge},
		"revalidate": {HandlerConfig{Keys: keys, Revalidate: true}, "public, no-cache", 0},
		"a minute":   {HandlerConfig{Keys: keys, MaxAge: time.Minute}, "public, max-age=60", time.Minute},
		"a second":   {HandlerConfig{Keys: keys, MaxAge: time.Second}, "public, max-age=1", time.Second},
		// Whole seconds, the fraction dropped.
		"a second and a half": {HandlerConfig{Keys: keys, MaxAge: 1500 * time.Millisecond}, "public, max-age=1", time.Second},
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
		if fresh, ok := responseFreshness(rec.Header()); fresh != tc.fresh || !ok {
			t.Errorf("%s: read back as fresh for %v, %v; want %v", name, fresh, ok, tc.fresh)
		}
	}
}

// A handler with no keys, a negative max-age — an invalid Cache-Control on
// every response — or one under a second is refused; a remote resolver with a negative
// interval or bound likewise.
func TestConfigsRefuseNegatives(t *testing.T) {
	keys := func() map[string]ed25519.PublicKey { return nil }
	for name, cfg := range map[string]HandlerConfig{
		"no keys":                   {},
		"negative max-age":          {Keys: keys, MaxAge: -time.Second},
		"max-age beside revalidate": {Keys: keys, MaxAge: time.Second, Revalidate: true},
		// Served in whole seconds, it would read max-age=0, stale at once.
		"max-age under a second":  {Keys: keys, MaxAge: 500 * time.Millisecond},
		"max-age of a nanosecond": {Keys: keys, MaxAge: time.Nanosecond},
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
