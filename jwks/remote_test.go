package jwks

import (
	"context"
	"crypto/ed25519"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// jwksServer serves a mutable key set with an ETag and counts fetches.
type jwksServer struct {
	mu      sync.Mutex
	keys    map[string]ed25519.PublicKey
	fail    bool
	fetches atomic.Int32
}

// checkDeadline bounds a check that must not hang; checkPoll is how often a
// check waiting on a fetch behind a caller looks again.
const (
	checkDeadline = time.Second
	checkPoll     = time.Millisecond
)

// awaitFetches waits, up to checkDeadline, for fetches to reach want: a
// refresh started behind a caller ends after the caller has its key.
func awaitFetches(t *testing.T, fetches func() int32, want int32) {
	t.Helper()
	deadline := time.Now().Add(checkDeadline)
	for fetches() < want && time.Now().Before(deadline) {
		time.Sleep(checkPoll)
	}
	if got := fetches(); got != want {
		t.Fatalf("fetches = %d, want %d", got, want)
	}
}

func (s *jwksServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.fetches.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		http.Error(w, "down", http.StatusServiceUnavailable)
		return
	}
	doc, err := Marshal(s.keys)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	etag := `"` + string(rune('a'+len(s.keys))) + `"`
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("ETag", etag)
	_, _ = w.Write(doc)
}

func (s *jwksServer) set(f func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f()
}

type stepClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *stepClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *stepClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newRemoteFixture(t *testing.T) (*jwksServer, *Remote, *stepClock, string, ed25519.PublicKey) {
	t.Helper()
	kid, pub, _, err := generateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	srv := &jwksServer{keys: map[string]ed25519.PublicKey{kid: pub}}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	clock := &stepClock{t: time.Unix(1_800_000_000, 0)}
	r, err := NewRemote(RemoteConfig{
		URL: ts.URL, Client: ts.Client(), Now: clock.Now,
		RefreshInterval: time.Minute, MinRefreshInterval: 10 * time.Second, MaxStale: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	return srv, r, clock, kid, pub
}

func TestRemoteJWKSCachesUntilRefreshInterval(t *testing.T) {
	srv, r, clock, kid, pub := newRemoteFixture(t)
	ctx := context.Background()

	for range 3 {
		got, err := r.PublicKey(ctx, kid)
		if err != nil || !got.Equal(pub) {
			t.Fatalf("PublicKey = %v, %v", got, err)
		}
	}
	if n := srv.fetches.Load(); n != 1 {
		t.Fatalf("fetches = %d, want 1 (cached)", n)
	}

	clock.Advance(2 * time.Minute)
	if _, err := r.PublicKey(ctx, kid); err != nil {
		t.Fatal(err)
	}
	awaitFetches(t, srv.fetches.Load, 2)
}

// A known key from a set past its freshness but within MaxStale is served at
// once while the endpoint hangs: the refresh runs behind the caller, whose
// deadline the endpoint cannot spend.
func TestRemoteJWKSServesAHeldKeyWhileTheEndpointHangs(t *testing.T) {
	srv, r, clock, kid, pub := newRemoteFixture(t)
	if _, err := r.PublicKey(context.Background(), kid); err != nil {
		t.Fatal(err)
	}
	srv.mu.Lock() // the endpoint hangs until the check ends
	t.Cleanup(srv.mu.Unlock)

	clock.Advance(2 * time.Minute) // past RefreshInterval, within MaxStale
	ctx, cancel := context.WithTimeout(context.Background(), checkDeadline/10)
	defer cancel()
	got, err := r.PublicKey(ctx, kid)
	if err != nil || !got.Equal(pub) {
		t.Fatalf("PublicKey while the endpoint hangs = %v, %v; want the held key", got, err)
	}
	awaitFetches(t, srv.fetches.Load, 2)
}

// A new kid is picked up on first sight — that is how rotation reaches a
// remote verifier — but unknown kids cannot force a fetch per token.
func TestRemoteJWKSPicksUpRotatedKeyAndRateLimitsUnknownKids(t *testing.T) {
	srv, r, clock, kid, _ := newRemoteFixture(t)
	ctx := context.Background()
	if _, err := r.PublicKey(ctx, kid); err != nil {
		t.Fatal(err)
	}

	for range 5 {
		if _, err := r.PublicKey(ctx, "made-up"); !errors.Is(err, ErrUnknownKID) {
			t.Fatalf("unknown kid err = %v, want ErrUnknownKID", err)
		}
	}
	if n := srv.fetches.Load(); n != 1 {
		t.Fatalf("fetches = %d, want 1: unknown kids inside MinRefreshInterval must not refetch", n)
	}

	newKid, newPub, _, _ := generateKeypair()
	srv.set(func() { srv.keys[newKid] = newPub })
	clock.Advance(11 * time.Second)
	got, err := r.PublicKey(ctx, newKid)
	if err != nil || !got.Equal(newPub) {
		t.Fatalf("rotated-in key = %v, %v", got, err)
	}
}

// Through an outage the last good set keeps verifying, up to MaxStale; past
// it the resolver fails closed.
func TestRemoteJWKSServesStaleThenFailsClosed(t *testing.T) {
	srv, r, clock, kid, _ := newRemoteFixture(t)
	ctx := context.Background()
	if _, err := r.PublicKey(ctx, kid); err != nil {
		t.Fatal(err)
	}
	srv.set(func() { srv.fail = true })

	clock.Advance(30 * time.Minute)
	if _, err := r.PublicKey(ctx, kid); err != nil {
		t.Fatalf("within MaxStale: %v, want the cached key", err)
	}

	clock.Advance(31 * time.Minute)
	if _, err := r.PublicKey(ctx, kid); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("past MaxStale err = %v, want ErrUnavailable", err)
	}

	srv.set(func() { srv.fail = false })
	clock.Advance(time.Minute)
	if _, err := r.PublicKey(ctx, kid); err != nil {
		t.Fatalf("after recovery: %v", err)
	}
}

func TestRemoteJWKSNeverReachedFailsClosed(t *testing.T) {
	r, err := NewRemote(RemoteConfig{URL: "http://127.0.0.1:1/jwks.json"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.PublicKey(context.Background(), "any"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
}

// A caller waiting behind another's fetch gives up when its own context
// does, rather than for as long as that fetch takes.
func TestRemoteJWKSWaiterHonoursItsContext(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		once.Do(func() { close(entered) })
		<-release
		http.Error(w, "slow", http.StatusServiceUnavailable)
	}))
	t.Cleanup(ts.Close)
	t.Cleanup(func() { close(release) })
	r, err := NewRemote(RemoteConfig{URL: ts.URL, Client: ts.Client()})
	if err != nil {
		t.Fatal(err)
	}

	go func() { _, _ = r.PublicKey(context.Background(), "first") }()
	<-entered

	ctx, cancel := context.WithTimeout(context.Background(), checkDeadline/10)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := r.PublicKey(ctx, "second")
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, ErrUnavailable) {
			t.Errorf("waiter's error = %v, want its context's, as ErrUnavailable", err)
		}
	case <-time.After(checkDeadline):
		t.Fatal("a waiter outlived its context behind another caller's fetch")
	}
}

// A caller that gives up during its own fetch takes only itself down: the
// fetch goes on without it, and the next caller is served by that fetch
// rather than a second one or someone else's cancellation.
func TestRemoteJWKSCallerCancellingItsFetchBlocksNoOne(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := Marshal(map[string]ed25519.PublicKey{"k": pub})
	if err != nil {
		t.Fatal(err)
	}
	var fetches atomic.Int32
	release := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fetches.Add(1)
		<-release // slow until the first caller has given up
		_, _ = w.Write(doc)
	}))
	t.Cleanup(ts.Close)
	r, err := NewRemote(RemoteConfig{URL: ts.URL, Client: ts.Client()})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), checkDeadline/10)
	defer cancel()
	if _, err := r.PublicKey(ctx, "k"); !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("the caller that gave up: err = %v, want its context's, as ErrUnavailable", err)
	}
	close(release)
	got, err := r.PublicKey(context.Background(), "k")
	if err != nil || !got.Equal(pub) {
		t.Fatalf("the next caller = %v, %v; want the key", got, err)
	}
	if n := fetches.Load(); n != 1 {
		t.Errorf("%d fetches, want the one the first caller started", n)
	}
}

// Callers that give up cannot turn the resolver into a request amplifier:
// however many cancel, the issuer sees at most one fetch per
// MinRefreshInterval.
func TestRemoteJWKSCancellingCallersKeepTheRateLimit(t *testing.T) {
	const callers = 5
	var fetches atomic.Int32
	release := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fetches.Add(1)
		<-release
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	t.Cleanup(ts.Close)
	t.Cleanup(func() { close(release) })
	r, err := NewRemote(RemoteConfig{
		URL: ts.URL, Client: ts.Client(),
		MinRefreshInterval: time.Hour, RefreshInterval: time.Hour, MaxStale: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	for range callers {
		ctx, cancel := context.WithTimeout(context.Background(), checkDeadline/20)
		_, _ = r.PublicKey(ctx, "unknown")
		cancel()
	}
	if n := fetches.Load(); n != 1 {
		t.Errorf("%d fetches reached the issuer from %d cancelled callers within one MinRefreshInterval, want 1", n, callers)
	}
}

// A caller whose context is already done fetches nothing.
func TestRemoteJWKSDoneCallerFetchesNothing(t *testing.T) {
	var fetches atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fetches.Add(1)
		http.Error(w, "unused", http.StatusServiceUnavailable)
	}))
	t.Cleanup(ts.Close)
	r, err := NewRemote(RemoteConfig{URL: ts.URL, Client: ts.Client()})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.PublicKey(ctx, "k"); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if n := fetches.Load(); n != 0 {
		t.Errorf("a cancelled caller fetched %d times", n)
	}
}

// The intervals must nest: a key set older than MaxStale before it is due a
// refresh would fail closed with no fetch having failed.
func TestRemoteJWKSRefusesIntervalsThatDoNotNest(t *testing.T) {
	const url = "https://issuer.example.com/jwks"
	cases := map[string]struct {
		cfg RemoteConfig
		ok  bool
	}{
		"defaults":                      {RemoteConfig{URL: url}, true},
		"equal":                         {RemoteConfig{URL: url, MinRefreshInterval: time.Minute, RefreshInterval: time.Minute, MaxStale: time.Minute}, true},
		"stale before due a refresh":    {RemoteConfig{URL: url, RefreshInterval: time.Hour, MaxStale: time.Minute}, false},
		"refresh inside the rate limit": {RemoteConfig{URL: url, MinRefreshInterval: time.Minute, RefreshInterval: time.Second}, false},
	}
	for name, tc := range cases {
		if _, err := NewRemote(tc.cfg); (err == nil) != tc.ok {
			t.Errorf("%s: err = %v, want ok = %v", name, err, tc.ok)
		}
	}
}

// A kid the held set lacks, while the endpoint fails, is the endpoint's
// fault — the issuer may have rotated it in — so it is ErrUnavailable, not
// an unknown kid the token is refused for; once the endpoint answers
// without it, it is unknown again.
func TestRemoteJWKSUnknownKidDuringAnOutageIsUnavailable(t *testing.T) {
	srv, r, clock, kid, _ := newRemoteFixture(t)
	ctx := context.Background()
	if _, err := r.PublicKey(ctx, kid); err != nil {
		t.Fatal(err)
	}
	const rateLimitPassed = 11 * time.Second // past MinRefreshInterval
	srv.set(func() { srv.fail = true })
	clock.Advance(rateLimitPassed)
	_, err := r.PublicKey(ctx, "rotated-in")
	if !errors.Is(err, ErrUnavailable) || errors.Is(err, ErrUnknownKID) {
		t.Errorf("an unknown kid while the endpoint fails: err = %v, want ErrUnavailable", err)
	}
	if _, err := r.PublicKey(ctx, kid); err != nil {
		t.Errorf("a held kid while the endpoint fails: %v", err)
	}

	srv.set(func() { srv.fail = false })
	clock.Advance(rateLimitPassed)
	if _, err := r.PublicKey(ctx, "rotated-in"); !errors.Is(err, ErrUnknownKID) {
		t.Errorf("an unknown kid once the endpoint answers: err = %v, want ErrUnknownKID", err)
	}
}

// A caller that waited for a fetch finds the fetch slot free once it has the
// answer, so the refresh it may start next is not mistaken for one in flight.
func TestRemoteJWKSFreesTheFetchBeforeAnswering(t *testing.T) {
	const attempts = 200 // the old order lost the race in a fraction of them
	for range attempts {
		_, r, _, kid, _ := newRemoteFixture(t)
		if _, err := r.PublicKey(context.Background(), kid); err != nil {
			t.Fatal(err)
		}
		if held := len(r.fetching); held != 0 {
			t.Fatalf("the fetch slot is still held after its caller was answered")
		}
	}
}
