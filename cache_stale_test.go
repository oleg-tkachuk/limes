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
