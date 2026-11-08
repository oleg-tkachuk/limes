package jwtverify

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwk"

	"github.com/oleg-tkachuk/limes/exchange"
	"github.com/oleg-tkachuk/limes/jwks"
)

// The kids of a key the identity provider rotates in, and of keys it never
// published.
const (
	rotatedKID = "idp-key-2"
	madeUpKID  = "made-up-1"
	madeUpKID2 = "made-up-2"
)

// fetchMessage is what RemoteKeys logs when a token starts a fetch.
const fetchMessage = "jwtverify: fetching the identity provider's keys"

// keyClock is a clock a test moves.
type keyClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *keyClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *keyClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// jwksServer is an identity provider's JWKS endpoint: the document it
// serves, or a failure, and how many fetches it answered.
type jwksServer struct {
	*httptest.Server
	doc     atomic.Value // []byte
	failing atomic.Bool
	fetches atomic.Int64
}

func newJWKSServer(t *testing.T, sets ...jwk.Set) *jwksServer {
	t.Helper()
	s := &jwksServer{}
	s.publish(t, sets...)
	s.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		s.fetches.Add(1)
		if s.failing.Load() {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", jsonContentType)
		_, _ = w.Write(s.doc.Load().([]byte))
	}))
	t.Cleanup(s.Close)
	return s
}

// publish serves the keys of sets as one JWK Set.
func (s *jwksServer) publish(t *testing.T, sets ...jwk.Set) {
	t.Helper()
	all := jwk.NewSet()
	for _, set := range sets {
		for i := range set.Len() {
			key, _ := set.Key(i)
			if err := all.AddKey(key); err != nil {
				t.Fatal(err)
			}
		}
	}
	doc, err := json.Marshal(all)
	if err != nil {
		t.Fatal(err)
	}
	s.doc.Store(doc)
}

// messages is a slog.Handler keeping the messages it is given.
type messages struct {
	mu   sync.Mutex
	msgs []string
}

func (m *messages) Enabled(context.Context, slog.Level) bool { return true }
func (m *messages) Handle(_ context.Context, r slog.Record) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.msgs = append(m.msgs, r.Message)
	return nil
}
func (m *messages) WithAttrs([]slog.Attr) slog.Handler { return m }
func (m *messages) WithGroup(string) slog.Handler      { return m }

func (m *messages) count(msg string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, got := range m.msgs {
		if got == msg {
			n++
		}
	}
	return n
}

// remoteFixture is RemoteKeys over srv on a clock the test moves, waited
// on until its first set is fetched in the background, the clock then moved
// past the shortest time between two fetches a token starts.
func remoteFixture(t *testing.T, srv *jwksServer) (*KeySource, *keyClock, *messages) {
	t.Helper()
	clk := &keyClock{now: testNow}
	logs := &messages{}
	r, err := newRemote(t.Context(), srv.URL, srv.Client(), clk.Now, slog.New(logs))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(remoteKeysWait)
	for r.lastFetched().IsZero() {
		if time.Now().After(deadline) {
			t.Fatal("the keys were never fetched")
		}
		time.Sleep(remoteKeysPoll)
	}
	clk.advance(remoteMinRefetchInterval)
	return r.source(), clk, logs
}

// A token naming a key the identity provider rotated in since the set was
// fetched has the set fetched again, and verifies under it.
func TestRemoteKeysFetchAnUnknownKID(t *testing.T) {
	p, rotated := newIDP(t), newIDPKey(t, rotatedKID)
	srv := newJWKSServer(t, p.keys)
	keys, _, logs := remoteFixture(t, srv)
	v := newVerifier(t, Config{Issuer: idpIssuer, Keys: keys, Now: clock})
	srv.publish(t, p.keys, rotated.keys)
	before := srv.fetches.Load()
	if _, err := v.VerifySubject(t.Context(), rotated.sign(t, TypeAccessToken, accessClaims())); err != nil {
		t.Fatalf("a token under the rotated-in key: %v", err)
	}
	if n := srv.fetches.Load() - before; n != 1 {
		t.Errorf("%d fetches, want 1", n)
	}
	if logs.count(fetchMessage) == 0 {
		t.Error("the fetch was not logged")
	}
}

// Tokens naming kids nobody published fetch the set at most once per
// remoteMinRefetchInterval, and are refused as the token's fault.
func TestRemoteKeysFetchAtMostOncePerInterval(t *testing.T) {
	p := newIDP(t)
	srv := newJWKSServer(t, p.keys)
	keys, clk, _ := remoteFixture(t, srv)
	v := newVerifier(t, Config{Issuer: idpIssuer, Keys: keys, Now: clock})
	before := srv.fetches.Load()
	for _, kid := range []string{madeUpKID, madeUpKID2} {
		token := signWithKID(t, p.key, jwa.EdDSAEd25519(), TypeAccessToken, kid, accessClaims())
		if _, err := v.VerifySubject(t.Context(), token); !errors.Is(err, exchange.ErrInvalidGrant) {
			t.Errorf("kid %s: err = %v, want ErrInvalidGrant", kid, err)
		}
	}
	if n := srv.fetches.Load() - before; n != 1 {
		t.Errorf("%d fetches for two unknown kids within the interval, want 1", n)
	}
	clk.advance(remoteMinRefetchInterval)
	if _, err := v.VerifySubject(t.Context(), signWithKID(t, p.key, jwa.EdDSAEd25519(), TypeAccessToken, madeUpKID, accessClaims())); !errors.Is(err, exchange.ErrInvalidGrant) {
		t.Errorf("after the interval: err = %v, want ErrInvalidGrant", err)
	}
	if n := srv.fetches.Load() - before; n != 2 {
		t.Errorf("%d fetches after the interval, want 2", n)
	}
}

// While the JWKS URL fails, the last set fetched is served for up to
// remoteMaxStale; past it the keys are jwks.ErrUnavailable, the server's
// failure, not a refusal of the token.
func TestRemoteKeysServeAStaleSetForABoundedTime(t *testing.T) {
	p := newIDP(t)
	srv := newJWKSServer(t, p.keys)
	keys, clk, _ := remoteFixture(t, srv)
	v := newVerifier(t, Config{Issuer: idpIssuer, Keys: keys, Now: clock})
	token := p.sign(t, TypeAccessToken, accessClaims())
	srv.failing.Store(true)
	clk.advance(remoteMaxStale - 2*remoteMinRefetchInterval)
	if _, err := v.VerifySubject(t.Context(), token); err != nil {
		t.Fatalf("within its bound, the last set: %v", err)
	}
	clk.advance(2 * remoteMinRefetchInterval)
	_, err := v.VerifySubject(t.Context(), token)
	if !errors.Is(err, jwks.ErrUnavailable) || errors.Is(err, exchange.ErrInvalidGrant) {
		t.Errorf("past its bound: err = %v, want jwks.ErrUnavailable and no ErrInvalidGrant", err)
	}
}

// RemoteKeys' intervals nest as jwks.Remote's must: a token's fetches no
// more often than the set is refreshed, which is well within the time a
// stale set is served.
func TestRemoteKeysIntervalsNest(t *testing.T) {
	intervals := []time.Duration{remoteMinRefetchInterval, remoteRefreshInterval, remoteMaxStale}
	if !slices.IsSorted(intervals) {
		t.Errorf("intervals %v, want them ascending", intervals)
	}
}
