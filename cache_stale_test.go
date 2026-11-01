package limes

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
	"uuid"
)

// cacheMaxStale is how long past cacheTTL the stale tests' caches may stand
// in for an upstream that fails.
const cacheMaxStale = 10 * time.Minute

var errUpstreamDown = errors.New("upstream down")

// flakyUpstream answers revoked, or fails with err, for every question of
// either kind.
type flakyUpstream struct {
	mu      sync.Mutex
	revoked bool
	err     error
}

func (f *flakyUpstream) set(revoked bool, err error) {
	f.mu.Lock()
	f.revoked, f.err = revoked, err
	f.mu.Unlock()
}

func (f *flakyUpstream) answer() (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.revoked, f.err
}

func (f *flakyUpstream) IsRevoked(context.Context, uuid.UUID) (bool, error) { return f.answer() }

func (f *flakyUpstream) IsBiscuitRevoked(context.Context, [][]byte) (bool, error) {
	return f.answer()
}

// flakyLineage is flakyUpstream naming no ancestors.
type flakyLineage struct{ flakyUpstream }

func (f *flakyLineage) RevocationLineage(context.Context, uuid.UUID) (RevocationAnswer, error) {
	revoked, err := f.answer()
	return RevocationAnswer{Revoked: revoked}, err
}

// stepClock is a time that moves only when told to.
type stepClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *stepClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *stepClock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// staleCache is a cache over up with cacheMaxStale, logging into the
// returned buffer.
func staleCache(t *testing.T, up RevocationLookup, biscuits BiscuitRevocationLookup) (*RevocationCache, *stepClock, *bytes.Buffer) {
	t.Helper()
	clk := &stepClock{now: time.Unix(0, 0)}
	var logs bytes.Buffer
	c := newRevocationCache(t, RevocationCacheConfig{
		Capabilities: up, Biscuits: biscuits, TTL: cacheTTL, MaxStale: cacheMaxStale, Now: clk.Now,
		Logger: slog.New(slog.NewTextHandler(&logs, nil)),
	})
	return c, clk, &logs
}

// An upstream that fails past the TTL is covered by the answer before it,
// up to MaxStale, counted and logged; past MaxStale its failure is
// returned.
func TestRevocationCacheServesAStaleAnswerOverAFailure(t *testing.T) {
	up := &flakyUpstream{}
	c, clk, logs := staleCache(t, up, nil)
	id := uuid.New()
	if revoked, err := c.IsRevoked(t.Context(), id); err != nil || revoked {
		t.Fatalf("IsRevoked = %v, %v; want live", revoked, err)
	}
	up.set(false, errUpstreamDown)
	clk.advance(cacheTTL)
	if revoked, err := c.IsRevoked(t.Context(), id); err != nil || revoked {
		t.Fatalf("IsRevoked over a failure = %v, %v; want the stale live answer", revoked, err)
	}
	if st := c.Stats().Capabilities; st.Stale != 1 {
		t.Errorf("Stats.Capabilities = %+v, want 1 stale answer", st)
	}
	if line := logs.String(); !strings.Contains(line, "level=WARN") || !strings.Contains(line, errUpstreamDown.Error()) || !strings.Contains(line, "kind=capability") {
		t.Errorf("log = %q, want a warning naming the failure", line)
	}
	clk.advance(cacheMaxStale)
	if _, err := c.IsRevoked(t.Context(), id); !errors.Is(err, errUpstreamDown) {
		t.Errorf("IsRevoked past MaxStale = %v, want the failure", err)
	}
}

// Without MaxStale a failure past the TTL is returned, as before.
func TestRevocationCacheServesNoStaleAnswerByDefault(t *testing.T) {
	up := &flakyUpstream{}
	clk := &stepClock{now: time.Unix(0, 0)}
	c := newRevocationCache(t, RevocationCacheConfig{Capabilities: up, TTL: cacheTTL, Now: clk.Now})
	id := uuid.New()
	if _, err := c.IsRevoked(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	up.set(false, errUpstreamDown)
	clk.advance(cacheTTL)
	if _, err := c.IsRevoked(t.Context(), id); !errors.Is(err, errUpstreamDown) {
		t.Errorf("IsRevoked = %v, want the failure", err)
	}
}

// "Not on record" is the upstream's answer, not a failure: no stale answer
// stands in for it.
func TestRevocationCacheTakesNotFoundOverAStaleAnswer(t *testing.T) {
	up := &flakyLineage{flakyUpstream{revoked: true}}
	c, clk, _ := staleCache(t, up, nil)
	id := uuid.New()
	if revoked, err := c.IsRevoked(t.Context(), id); err != nil || !revoked {
		t.Fatalf("IsRevoked = %v, %v; want revoked", revoked, err)
	}
	up.set(false, ErrNotFound)
	clk.advance(cacheTTL)
	if revoked, err := c.IsRevoked(t.Context(), id); err != nil || revoked {
		t.Errorf("IsRevoked = %v, %v; want not on record, read as live", revoked, err)
	}
	if st := c.Stats().Capabilities; st.Stale != 0 {
		t.Errorf("Stats.Capabilities = %+v, want no stale answer", st)
	}
}

// A Biscuit asked about as before is covered by its stale answer.
func TestRevocationCacheServesAStaleBiscuitAnswer(t *testing.T) {
	up := &flakyUpstream{}
	c, clk, logs := staleCache(t, newMemStore(), up)
	ids := [][]byte{[]byte("a"), []byte("b")}
	if revoked, err := c.IsBiscuitRevoked(t.Context(), ids); err != nil || revoked {
		t.Fatalf("IsBiscuitRevoked = %v, %v; want live", revoked, err)
	}
	up.set(false, errUpstreamDown)
	clk.advance(cacheTTL)
	if revoked, err := c.IsBiscuitRevoked(t.Context(), ids); err != nil || revoked {
		t.Fatalf("IsBiscuitRevoked over a failure = %v, %v; want the stale live answer", revoked, err)
	}
	if st := c.Stats().Biscuits; st.Stale != 1 {
		t.Errorf("Stats.Biscuits = %+v, want 1 stale answer", st)
	}
	if !strings.Contains(logs.String(), "kind=biscuit") {
		t.Errorf("log = %q, want a warning for a Biscuit", logs.String())
	}
}

func TestRevocationCacheRefusesABadMaxStale(t *testing.T) {
	for name, stale := range map[string]time.Duration{
		"negative":     -time.Second,
		"past the max": MaxRevocationCacheStale + time.Second,
	} {
		if _, err := NewRevocationCache(RevocationCacheConfig{Capabilities: newMemStore(), MaxStale: stale}); !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("%s: NewRevocationCache = %v, want ErrInvalidConfig", name, err)
		}
	}
}

// idUpstream answers a set of Biscuit revocation ids revoked when any of
// them is among revoked, or fails with err.
type idUpstream struct {
	mu      sync.Mutex
	revoked map[string]bool
	err     error
}

func (u *idUpstream) IsBiscuitRevoked(_ context.Context, ids [][]byte) (bool, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.err != nil {
		return false, u.err
	}
	for _, id := range ids {
		if u.revoked[string(id)] {
			return true, nil
		}
	}
	return false, nil
}

func (u *idUpstream) set(revoked string, err error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if revoked != "" {
		u.revoked[revoked] = true
	}
	u.err = err
}

// A token the cache saw revoked stays revoked over a failure: a stale live
// answer about the set of its ids does not stand for the revoked answer the
// cache holds about one of them, read since.
func TestRevocationCacheKeepsARevokedIDOverAStaleSet(t *testing.T) {
	up := &idUpstream{revoked: map[string]bool{}}
	c, clk, _ := staleCache(t, &flakyUpstream{}, up)
	a, b, sibling := []byte("a"), []byte("b"), []byte("c")
	ask := func(ids ...[]byte) (bool, error) { return c.IsBiscuitRevoked(t.Context(), ids) }
	if revoked, err := ask(a, b); err != nil || revoked {
		t.Fatalf("{a,b} = %v, %v; want live", revoked, err)
	}
	// Past the TTL, b is revoked; a sibling copy keeps a fresh, so the
	// next question about {a,b} asks about b alone.
	clk.advance(cacheTTL + time.Second)
	up.set(string(b), nil)
	if revoked, err := ask(a, sibling); err != nil || revoked {
		t.Fatalf("{a,c} = %v, %v; want live", revoked, err)
	}
	if revoked, err := ask(a, b); err != nil || !revoked {
		t.Fatalf("{a,b} once b is revoked = %v, %v; want revoked", revoked, err)
	}
	// Past the TTL again the upstream fails: the set's stale live answer
	// must not undo b's revocation.
	clk.advance(cacheTTL + time.Second)
	up.set("", errUpstreamDown)
	if revoked, err := ask(a, b); err != nil || !revoked {
		t.Errorf("{a,b} over a failure = %v, %v; want revoked, as the cache saw b", revoked, err)
	}
}

// evictingBound holds the revoked id, the set and one of its ids, so seeding
// the set's other id pushes out the oldest: the revoked answer.
const evictingBound = 3

// A revoked answer the cache let go to make room fails a stale live answer
// about a set of ids, as it fails a delegated capability's: the cache can no
// longer tell whether the set held the revocation it lost.
func TestRevocationCacheFailsAStaleSetOnceARevocationWasEvicted(t *testing.T) {
	up := &idUpstream{revoked: map[string]bool{}}
	clk := &stepClock{now: time.Unix(0, 0)}
	c := newRevocationCache(t, RevocationCacheConfig{
		Capabilities: &flakyUpstream{}, Biscuits: up, TTL: cacheTTL, MaxStale: cacheMaxStale, MaxEntries: evictingBound, Now: clk.Now,
	})
	a, b, revoked := []byte("a"), []byte("b"), []byte("c")
	up.set(string(revoked), nil)
	if got, err := c.IsBiscuitRevoked(t.Context(), [][]byte{revoked}); err != nil || !got {
		t.Fatalf("{c} = %v, %v; want revoked", got, err)
	}
	// The set's answer and its seeded ids push c's revoked answer out.
	if got, err := c.IsBiscuitRevoked(t.Context(), [][]byte{a, b}); err != nil || got {
		t.Fatalf("{a,b} = %v, %v; want live", got, err)
	}
	clk.advance(cacheTTL + time.Second)
	up.set("", errUpstreamDown)
	if got, err := c.IsBiscuitRevoked(t.Context(), [][]byte{a, b}); !errors.Is(err, errUpstreamDown) {
		t.Errorf("{a,b} over a failure once a revoked answer was evicted = %v, %v; want the failure", got, err)
	}
}
