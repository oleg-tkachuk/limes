package limes

import (
	"context"
	"crypto/ed25519"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
	"uuid"

	"github.com/oleg-tkachuk/limes/jwks"
)

// cacheTTL is the lifetime of an entry in the tests below; they step a clock
// past it rather than sleeping.
const cacheTTL = time.Minute

// checkDeadline bounds a check that must not wait on another caller's
// lookup, so a regression fails the test instead of hanging it.
const checkDeadline = time.Second

// slowBiscuitLookup blocks until released, counting its calls.
type slowBiscuitLookup struct {
	calls   atomic.Int32
	release chan struct{}
}

func (s *slowBiscuitLookup) IsBiscuitRevoked(context.Context, [][]byte) (bool, error) {
	s.calls.Add(1)
	<-s.release
	return true, nil
}

// The Biscuit cache shares concurrent misses for one token as the
// capability cache does.
func TestBiscuitRevocationCacheCoalescesConcurrentMisses(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		up := &slowBiscuitLookup{release: make(chan struct{})}
		c := newBiscuitCache(t, RevocationCacheConfig{Biscuits: up, TTL: cacheTTL})
		ids := [][]byte{[]byte("a"), []byte("b")}

		var wg sync.WaitGroup
		for range 20 {
			wg.Go(func() {
				if r, err := c.IsBiscuitRevoked(context.Background(), ids); err != nil || !r {
					t.Errorf("IsBiscuitRevoked = %v, %v", r, err)
				}
			})
		}
		// Every caller has joined the one lookup once all of them are blocked.
		synctest.Wait()
		close(up.release)
		wg.Wait()
		if n := up.calls.Load(); n != 1 {
			t.Errorf("upstream calls = %d, want 1", n)
		}
	})
}

// Sweep drops what has expired and keeps what has not, of both kinds.
func TestRevocationCacheSweep(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	clock := func() time.Time { return now }
	c := newRevocationCache(t, RevocationCacheConfig{Capabilities: newMemStore(), Biscuits: &countingBiscuitLookup{}, TTL: cacheTTL, Now: clock})

	_, _ = c.IsRevoked(ctx, uuid.New())
	_, _ = c.IsBiscuitRevoked(ctx, [][]byte{[]byte("old")})
	now = now.Add(cacheTTL / 2)
	_, _ = c.IsRevoked(ctx, uuid.New())
	_, _ = c.IsBiscuitRevoked(ctx, [][]byte{[]byte("new")})
	now = now.Add(cacheTTL / 2) // the old ones expire, the new ones live on

	if n := c.Sweep(); n != 2 || c.caps.Len() != 1 || c.copies.Len() != 1 {
		t.Errorf("Sweep = %d, %d and %d left; want 2 dropped, one of each kind left", n, c.caps.Len(), c.copies.Len())
	}
	if n := c.Sweep(); n != 0 {
		t.Errorf("a second Sweep dropped %d", n)
	}
}

// staleThenRevoked answers its first call with a "live" read that blocks
// until released — a lookup that began before a revocation — and every
// later call with "revoked".
type staleThenRevoked struct {
	calls   atomic.Int32
	entered chan struct{}
	release chan struct{}
}

func newStaleThenRevoked() *staleThenRevoked {
	return &staleThenRevoked{entered: make(chan struct{}), release: make(chan struct{})}
}

func (s *staleThenRevoked) IsRevoked(context.Context, uuid.UUID) (bool, error) {
	if s.calls.Add(1) == 1 {
		close(s.entered)
		<-s.release
		return false, nil
	}
	return true, nil
}

// A Clear that lands while a lookup is in flight is not undone by that
// lookup's answer: the next check goes upstream and sees the revocation, and
// a check arriving meanwhile does not join the stale lookup.
func TestRevocationCacheClearDuringLookupIsNotUndone(t *testing.T) {
	ctx := context.Background()
	up := newStaleThenRevoked()
	c := newRevocationCache(t, RevocationCacheConfig{Capabilities: up, TTL: cacheTTL})
	id := uuid.New()

	stale := make(chan bool)
	go func() {
		r, _ := c.IsRevoked(ctx, id)
		stale <- r
	}()
	<-up.entered
	c.Clear()

	bounded, cancel := context.WithTimeout(ctx, checkDeadline)
	defer cancel()
	if r, err := c.IsRevoked(bounded, id); err != nil || !r {
		t.Errorf("check after Clear, lookup still in flight = %v, %v; want revoked", r, err)
	}
	close(up.release)
	<-stale
	if r, err := c.IsRevoked(ctx, id); err != nil || !r {
		t.Errorf("check after the stale lookup finished = %v, %v; want revoked", r, err)
	}
}

// panickingLookup panics once released; a caller that recovers sees it.
type panickingLookup struct {
	calls   atomic.Int32
	entered chan struct{}
	release chan struct{}
}

func (p *panickingLookup) IsRevoked(context.Context, uuid.UUID) (bool, error) {
	if p.calls.Add(1) == 1 {
		close(p.entered)
		<-p.release
		panic("upstream fault")
	}
	return false, nil
}

// A lookup that panics releases the callers waiting on it with an error —
// never with a "live" answer nobody gave — and leaves the key free for the
// next check.
func TestRevocationCacheLookupPanicReleasesWaiters(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		up := &panickingLookup{entered: make(chan struct{}), release: make(chan struct{})}
		c := newRevocationCache(t, RevocationCacheConfig{Capabilities: up, TTL: cacheTTL})
		id := uuid.New()

		go func() {
			defer func() { _ = recover() }()
			_, _ = c.IsRevoked(context.Background(), id)
		}()
		<-up.entered

		type answer struct {
			revoked bool
			err     error
		}
		waiter := make(chan answer)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), checkDeadline)
			defer cancel()
			r, err := c.IsRevoked(ctx, id)
			waiter <- answer{r, err}
		}()
		// The waiter has joined once it blocks.
		synctest.Wait()
		close(up.release)

		got := <-waiter
		if got.err == nil || errors.Is(got.err, context.DeadlineExceeded) {
			t.Errorf("waiter on a panicked lookup = %v, %v; want released with an error", got.revoked, got.err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), checkDeadline)
		defer cancel()
		if _, err := c.IsRevoked(ctx, id); err != nil {
			t.Errorf("check after the panic = %v; want a fresh lookup", err)
		}
	})
}

// countingLookup counts its calls and revokes nothing.
type countingLookup struct{ calls atomic.Int32 }

func (c *countingLookup) IsRevoked(context.Context, uuid.UUID) (bool, error) {
	c.calls.Add(1)
	return false, nil
}

// A cache full of live answers makes room by dropping its oldest one, not
// every one: a flood of new ids must not send every caller of every cached
// id back upstream at once.
func TestRevocationCacheEvictsTheOldestWhenFull(t *testing.T) {
	const bound = 4
	ctx := context.Background()
	up := &countingLookup{}
	c := newRevocationCache(t, RevocationCacheConfig{Capabilities: up, TTL: time.Hour, MaxEntries: bound})
	ids := make([]uuid.UUID, bound)
	for i := range ids {
		ids[i] = uuid.New()
		_, _ = c.IsRevoked(ctx, ids[i])
	}
	_, _ = c.IsRevoked(ctx, uuid.New()) // one past the bound
	before := up.calls.Load()
	for _, id := range ids[1:] {
		_, _ = c.IsRevoked(ctx, id)
	}
	if n := up.calls.Load() - before; n != 0 {
		t.Errorf("%d of the newer answers were dropped to make room for one", n)
	}
	_, _ = c.IsRevoked(ctx, ids[0])
	if n := up.calls.Load() - before; n != 1 {
		t.Errorf("the oldest answer was kept past the bound (%d upstream calls, want 1)", n)
	}
	if n := c.caps.Len(); n > bound {
		t.Errorf("cache holds %d entries, bound %d", n, bound)
	}
}

// The eviction order holds one place per entry, so it stays within the
// bound however many answers come and go — by eviction or by expiry.
func TestRevocationCacheOrderStaysBounded(t *testing.T) {
	const bound, churn = 10, 10_000
	ctx := context.Background()
	clock := &mutableClock{now: time.Unix(0, 0)}
	c := newRevocationCache(t, RevocationCacheConfig{Capabilities: &countingLookup{}, TTL: cacheTTL, MaxEntries: bound, Now: func() time.Time { return clock.now }})
	ids := make([]uuid.UUID, bound/2)
	for i := range ids {
		ids[i] = uuid.New()
	}
	for i := range churn {
		_, _ = c.IsRevoked(ctx, uuid.New())
		// The same ids again once they have expired.
		_, _ = c.IsRevoked(ctx, ids[i%len(ids)])
		clock.now = clock.now.Add(cacheTTL / 3)
	}
	if n, e := c.caps.Queued(), c.caps.Len(); n > bound || n != e {
		t.Errorf("the eviction order holds %d places for %d entries and a bound of %d", n, e, bound)
	}
}

// newRevocationCache is NewRevocationCache, failing the test on a
// refused configuration.
func newRevocationCache(t testing.TB, cfg RevocationCacheConfig) *RevocationCache {
	t.Helper()
	c, err := NewRevocationCache(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// newBiscuitCache is newRevocationCache for a test of Biscuit answers: one
// that sets no capability upstream gets an empty store.
func newBiscuitCache(t testing.TB, cfg RevocationCacheConfig) *RevocationCache {
	t.Helper()
	if cfg.Capabilities == nil {
		cfg.Capabilities = newMemStore()
	}
	return newRevocationCache(t, cfg)
}

// A configuration with no capability upstream, or a negative TTL or bound,
// is refused rather than read as "no cache" or "the default". A Biscuit
// upstream alone is no capability upstream.
func TestRevocationCacheRefusesABadConfiguration(t *testing.T) {
	for name, cfg := range map[string]RevocationCacheConfig{
		"no upstream":         {},
		"a Biscuit one alone": {Biscuits: &countingBiscuitLookup{}},
		"negative TTL":        {Capabilities: newMemStore(), TTL: -time.Second},
		"negative bound":      {Capabilities: newMemStore(), MaxEntries: -1},
	} {
		if _, err := NewRevocationCache(cfg); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// A cache with no Biscuit upstream refuses a Biscuit question rather than
// answer "live", and NewVerifier refuses it where Biscuits are read.
func TestRevocationCacheWithoutBiscuits(t *testing.T) {
	c := newRevocationCache(t, RevocationCacheConfig{Capabilities: newMemStore(), TTL: cacheTTL})
	if revoked, err := c.IsBiscuitRevoked(context.Background(), [][]byte{[]byte("a")}); !errors.Is(err, errNoBiscuitLookup) || revoked {
		t.Errorf("IsBiscuitRevoked = %v, %v; want errNoBiscuitLookup", revoked, err)
	}
	_, err := NewVerifier(VerifierConfig{
		Keys: staticKeys(t, map[string]ed25519.PublicKey{}), Revocations: c, TrustedIssuers: []string{goldenIssuer},
		Biscuit: &BiscuitConfig{Revocations: c},
	})
	if !errors.Is(err, errNoBiscuitLookup) {
		t.Errorf("NewVerifier = %v, want errNoBiscuitLookup", err)
	}
}

// One Clear drops the answers of both kinds: a revocation notification wired
// to it reaches every answer the verifier holds.
func TestRevocationCacheClearDropsBothKinds(t *testing.T) {
	ctx := context.Background()
	caps, copies := &countingLookup{}, &countingBiscuitLookup{}
	c := newRevocationCache(t, RevocationCacheConfig{Capabilities: caps, Biscuits: copies, TTL: cacheTTL})
	id, copyID := uuid.New(), [][]byte{[]byte("a")}
	_, _ = c.IsRevoked(ctx, id)
	_, _ = c.IsBiscuitRevoked(ctx, copyID)
	c.Clear()
	_, _ = c.IsRevoked(ctx, id)
	_, _ = c.IsBiscuitRevoked(ctx, copyID)
	if caps.calls.Load() != 2 || copies.calls != 2 {
		t.Errorf("after Clear: %d capability and %d Biscuit lookups, want 2 of each", caps.calls.Load(), copies.calls)
	}
}

// Each kind of answer has a bound of its own: a flood of Biscuit ids does
// not evict the capability answers.
func TestRevocationCacheBoundsEachKind(t *testing.T) {
	ctx := context.Background()
	const bound = 2
	caps := &countingLookup{}
	c := newRevocationCache(t, RevocationCacheConfig{Capabilities: caps, Biscuits: &countingBiscuitLookup{}, TTL: cacheTTL, MaxEntries: bound})
	id := uuid.New()
	_, _ = c.IsRevoked(ctx, id)
	for i := range 4 * bound {
		_, _ = c.IsBiscuitRevoked(ctx, [][]byte{{byte(i)}})
	}
	_, _ = c.IsRevoked(ctx, id)
	if caps.calls.Load() != 1 {
		t.Errorf("the capability answer was evicted by Biscuit answers: %d lookups", caps.calls.Load())
	}
	if c.copies.Len() > bound {
		t.Errorf("%d Biscuit entries, bound %d", c.copies.Len(), bound)
	}
}

// An upstream that is an interface holding a nil pointer is no upstream:
// refused at construction rather than panicking at the first lookup.
func TestRevocationCacheRefusesATypedNilUpstream(t *testing.T) {
	var noStore *memStore
	var noBiscuits *countingBiscuitLookup
	for name, cfg := range map[string]RevocationCacheConfig{
		"nil capability store": {Capabilities: noStore},
		"nil Biscuit lookup":   {Capabilities: newMemStore(), Biscuits: noBiscuits},
	} {
		if _, err := NewRevocationCache(cfg); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// A nil *RevocationCache as BiscuitConfig.Revocations is refused by
// NewVerifier, not dereferenced.
func TestNewVerifierRefusesANilRevocationCache(t *testing.T) {
	var none *RevocationCache
	c := newRevocationCache(t, RevocationCacheConfig{Capabilities: newMemStore(), TTL: cacheTTL})
	_, err := NewVerifier(VerifierConfig{
		Keys: staticKeys(t, map[string]ed25519.PublicKey{}), Revocations: c, TrustedIssuers: []string{goldenIssuer},
		Biscuit: &BiscuitConfig{Revocations: none},
	})
	if !errors.Is(err, errNoBiscuitLookup) {
		t.Errorf("NewVerifier = %v, want errNoBiscuitLookup", err)
	}
}

// A nil pointer as VerifierConfig.Revocations or Keys is refused by
// NewVerifier, as no value is, rather than dereferenced at the first token.
func TestNewVerifierRefusesTypedNilWiring(t *testing.T) {
	var noCache *RevocationCache
	var noStore *memStore
	var noKeys *jwks.Static
	keys := staticKeys(t, map[string]ed25519.PublicKey{})
	c := newRevocationCache(t, RevocationCacheConfig{Capabilities: newMemStore(), TTL: cacheTTL})
	for name, cfg := range map[string]VerifierConfig{
		"a nil *RevocationCache": {Keys: keys, Revocations: noCache, TrustedIssuers: []string{goldenIssuer}},
		"a nil store":            {Keys: keys, Revocations: noStore, TrustedIssuers: []string{goldenIssuer}},
		"a nil *jwks.Static":     {Keys: noKeys, Revocations: c, TrustedIssuers: []string{goldenIssuer}},
	} {
		if _, err := NewVerifier(cfg); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestIsNilValue(t *testing.T) {
	var p *memStore
	var m map[string]int
	var f func()
	for name, c := range map[string]struct {
		v    any
		want bool
	}{
		"nil":         {nil, true},
		"nil pointer": {p, true},
		"nil map":     {m, true},
		"nil func":    {f, true},
		"a pointer":   {newMemStore(), false},
		"a value":     {0, false},
	} {
		if got := isNilValue(c.v); got != c.want {
			t.Errorf("%s: isNilValue = %v, want %v", name, got, c.want)
		}
	}
}

// Stats counts each kind's questions apart, a Biscuit question as a hit only
// when every id of the token has a fresh answer, and the clears.
func TestRevocationCacheStats(t *testing.T) {
	ctx := context.Background()
	c := newRevocationCache(t, RevocationCacheConfig{Capabilities: &countingLookup{}, Biscuits: &countingBiscuitLookup{}, TTL: cacheTTL})
	id := uuid.New()
	_, _ = c.IsRevoked(ctx, id)
	_, _ = c.IsRevoked(ctx, id)
	a, b, other := []byte("a"), []byte("b"), []byte("c")
	_, _ = c.IsBiscuitRevoked(ctx, [][]byte{a, b})     // a miss, one lookup for both
	_, _ = c.IsBiscuitRevoked(ctx, [][]byte{a, b})     // a hit: both seeded live
	_, _ = c.IsBiscuitRevoked(ctx, [][]byte{a, other}) // a miss, for the new id
	got := c.Stats()
	if want := (RevocationCacheCounts{Hits: 1, Misses: 1, Lookups: 1, Entries: 1}); !reflect.DeepEqual(got.Capabilities, want) {
		t.Errorf("Capabilities = %+v, want %+v", got.Capabilities, want)
	}
	// The set {a, b} and its ids a and b, then c.
	const biscuitEntries = 4
	if want := (RevocationCacheCounts{Hits: 1, Misses: 2, Lookups: 2, Entries: biscuitEntries}); !reflect.DeepEqual(got.Biscuits, want) {
		t.Errorf("Biscuits = %+v, want %+v", got.Biscuits, want)
	}
	c.Clear()
	got = c.Stats()
	if got.Clears != 1 || got.Capabilities.Entries != 0 || got.Biscuits.Entries != 0 || got.Capabilities.Hits != 1 {
		t.Errorf("after Clear: %+v, want 1 clear, no entries, the counts kept", got)
	}
}
