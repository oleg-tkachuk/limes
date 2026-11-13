package jwtverify

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oleg-tkachuk/limes/jwks"
)

// cancelledFetchWait is how long a test's caller waits on a fetch before
// giving up on it.
const cancelledFetchWait = 50 * time.Millisecond

// givingUpCallers is how many tokens with an unknown kid give up on their
// fetch in a row in TestRemoteKeysACancelledFetchStillCounts.
const givingUpCallers = 5

// A token's fetch its caller gave up on still runs, as httprc sends it
// anyway, and counts: tokens that give up one after another within
// remoteMinRefetchInterval reach the endpoint once, and the next token
// waits for that fetch and takes its set rather than start another.
func TestRemoteKeysACancelledFetchStillCounts(t *testing.T) {
	p := newIDP(t)
	srv := newJWKSServer(t, p.keys)
	keys, _, _ := remoteFixture(t, srv)
	// The endpoint holds every fetch until let go, counting them.
	var reached atomic.Int64
	release := make(chan struct{})
	serve := srv.Config.Handler
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		reached.Add(1)
		<-release
		serve.ServeHTTP(w, req)
	})
	// A regression leaves fetches held: let them go when the test ends.
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	for range givingUpCallers {
		ctx, cancel := context.WithTimeout(t.Context(), cancelledFetchWait)
		_, err := keys.refetch(ctx)
		cancel()
		if !errors.Is(err, jwks.ErrUnavailable) || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("a token that gave up on its fetch: err = %v, want ErrUnavailable wrapping its deadline", err)
		}
	}
	got := make(chan error, 1)
	go func() {
		_, err := keys.refetch(t.Context())
		got <- err
	}()
	close(release)
	select {
	case err := <-got:
		if err != nil {
			t.Fatalf("the token after them: %v", err)
		}
	case <-time.After(remoteKeysWait):
		t.Fatal("the token after them never got the keys")
	}
	if n := reached.Load(); n != 1 {
		t.Errorf("%d fetches reached the endpoint, want 1", n)
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
