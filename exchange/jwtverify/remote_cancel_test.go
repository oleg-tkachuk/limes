package jwtverify

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oleg-tkachuk/limes/jwks"
)

// cancelledFetchWait is how long the fetch a test cancels runs before its
// caller gives up on it.
const cancelledFetchWait = 50 * time.Millisecond

// A token's fetch its caller gave up on does not count as one: the next
// token whose kid is unknown fetches at once, rather than wait out
// remoteMinRefetchInterval with the set it lacks the kid of.
func TestRemoteKeysACancelledFetchHoldsOffNoFetch(t *testing.T) {
	p := newIDP(t)
	srv := newJWKSServer(t, p.keys)
	keys, _, _ := remoteFixture(t, srv)
	// The endpoint holds the fetches that arrive while held until let go,
	// and counts those that arrive after.
	var held atomic.Bool
	var after atomic.Int64
	release := make(chan struct{})
	serve := srv.Config.Handler
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if held.Load() {
			<-release
		} else {
			after.Add(1)
		}
		serve.ServeHTTP(w, req)
	})
	held.Store(true)
	ctx, cancel := context.WithTimeout(t.Context(), cancelledFetchWait)
	defer cancel()
	if _, err := keys.refetch(ctx); err == nil {
		t.Fatal("a fetch its caller gave up on returned keys")
	}
	held.Store(false)
	close(release)
	if _, err := keys.refetch(t.Context()); err != nil {
		t.Fatalf("the next token's fetch: %v", err)
	}
	if after.Load() == 0 {
		t.Error("the next token's fetch was held off by one its caller gave up on")
	}
}

// A key set document past jwks.DefaultMaxDocumentBytes is refused, whatever
// limit jwx is configured with in the process: the fetch fails, and no
// key is held.
func TestRemoteKeysRefuseAnOversizedDocument(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", jsonContentType)
		_, _ = w.Write([]byte(`{"keys":[],"pad":"` + strings.Repeat("x", jwks.DefaultMaxDocumentBytes) + `"}`))
	}))
	t.Cleanup(srv.Close)
	r, err := newRemote(t.Context(), srv.URL, srv.Client(), time.Now, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.keys(t.Context()); err == nil {
		t.Error("keys from an oversized document: no error")
	}
	if !r.lastFetched().IsZero() {
		t.Error("an oversized document was read as a set")
	}
}
