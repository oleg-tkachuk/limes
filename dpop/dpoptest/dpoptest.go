// Package dpoptest checks a dpop.ReplayCache and a dpop.Nonces against the
// package's contract, the way storetest checks a limes.Store. Run them from
// the implementation's own tests:
//
//	func TestReplayCacheContract(t *testing.T) {
//		dpoptest.RunReplayCache(t, func(t *testing.T) dpoptest.ReplayEnv { … })
//	}
//
// The checks cover what the verifier relies on: a proof id is new once and a
// replay ever after, while it can still be accepted; ids of different keys
// never collide; concurrent proofs carrying one id let exactly one through;
// a nonce issued is valid, and one never issued or expired is not.
package dpoptest

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes/dpop"
)

// ReplayEnv is one replay cache under test, fresh and empty for each check.
type ReplayEnv struct {
	// Ctx is the context every call runs under.
	Ctx context.Context
	// Cache is the cache under test, with no id recorded.
	Cache dpop.ReplayCache
	// Now reads the cache's clock. Optional: without it, time.Now.
	Now func() time.Time
	// Advance moves the cache's clock forward by d. Optional, with Now:
	// without both, the check that an id is remembered until it expires is
	// skipped.
	Advance func(d time.Duration)
	// EntriesPerKey is how many live ids the cache holds for one key before
	// it refuses the next as a replay. Optional: without it, the check of
	// that share is skipped.
	EntriesPerKey int
}

// NoncesEnv is one nonce source under test, fresh for each check.
type NoncesEnv struct {
	// Ctx is the context every call runs under.
	Ctx context.Context
	// Nonces is the nonce source under test.
	Nonces dpop.Nonces
	// Lifetime is how long a nonce is valid. Optional, with Advance:
	// without both, the check of expiry is skipped.
	Lifetime time.Duration
	// Advance moves the source's clock forward by d.
	Advance func(d time.Duration)
}

const (
	// proofLifetime is how long the ids of these checks can be accepted.
	proofLifetime = time.Minute
	// tick is the least step of a clock these checks move: a store may keep
	// time to the second.
	tick = time.Second
	// concurrentProofs is how many proofs carrying one id arrive at once.
	concurrentProofs = 20
	// jkt and otherJKT are the thumbprints of two keys; their form does not
	// matter to a cache.
	jkt      = "key-a"
	otherJKT = "key-b"
	// notANonce has no nonce's form at all.
	notANonce = "not-a-nonce"
)

// RunReplayCache runs every replay cache check as a subtest, each on a fresh
// ReplayEnv from setup.
func RunReplayCache(t *testing.T, setup func(t *testing.T) ReplayEnv) {
	t.Helper()
	checks := []struct {
		name string
		run  func(*testing.T, ReplayEnv)
	}{
		{"AnIDIsNewOnceThenAReplay", checkReplay},
		{"KeysAndIDsAreApart", checkApart},
		{"AnIDIsRememberedUntilItExpires", checkRemembered},
		{"AKeyPastItsShareIsRefused", checkShare},
		{"AKeysShareFreesOnceItsIDsExpire", checkShareFrees},
		{"ConcurrentProofsLetOneThrough", checkConcurrentReplay},
	}
	for _, c := range checks {
		t.Run(c.name, func(t *testing.T) { c.run(t, setup(t)) })
	}
}

// RunNonces runs every nonce check as a subtest, each on a fresh NoncesEnv
// from setup.
func RunNonces(t *testing.T, setup func(t *testing.T) NoncesEnv) {
	t.Helper()
	checks := []struct {
		name string
		run  func(*testing.T, NoncesEnv)
	}{
		{"AnIssuedNonceIsValid", checkIssued},
		{"ANonceNeverIssuedIsNot", checkForeign},
		{"ANonceExpires", checkExpiry},
	}
	for _, c := range checks {
		t.Run(c.name, func(t *testing.T) { c.run(t, setup(t)) })
	}
}

func (e ReplayEnv) now() time.Time {
	if e.Now == nil {
		return time.Now()
	}
	return e.Now()
}

// entry is a fresh id of key, accepted for proofLifetime from now.
func (e ReplayEnv) entry(key string) dpop.ReplayEntry {
	return dpop.ReplayEntry{JKT: key, JTI: uuid.NewString(), ExpiresAt: e.now().Add(proofLifetime)}
}

func (e ReplayEnv) seen(t *testing.T, entry dpop.ReplayEntry) bool {
	t.Helper()
	replay, err := e.Cache.Seen(e.Ctx, entry)
	if err != nil {
		t.Fatalf("Seen(%+v): %v", entry, err)
	}
	return replay
}

func checkReplay(t *testing.T, e ReplayEnv) {
	entry := e.entry(jkt)
	if e.seen(t, entry) {
		t.Fatal("a new id was reported as a replay")
	}
	if !e.seen(t, entry) {
		t.Fatal("a recorded id was reported as new")
	}
}

func checkApart(t *testing.T, e ReplayEnv) {
	entry := e.entry(jkt)
	e.seen(t, entry)
	other := entry
	other.JKT = otherJKT
	if e.seen(t, other) {
		t.Error("an id of one key was a replay for another")
	}
	if e.seen(t, e.entry(jkt)) {
		t.Error("another id of the same key was a replay")
	}
}

func checkRemembered(t *testing.T, e ReplayEnv) {
	if e.Advance == nil || e.Now == nil {
		t.Skip("ReplayEnv.Now and Advance not set")
	}
	entry := e.entry(jkt)
	e.seen(t, entry)
	e.Advance(proofLifetime - tick)
	if !e.seen(t, entry) {
		t.Error("an id was forgotten before it expired")
	}
}

func checkShare(t *testing.T, e ReplayEnv) {
	if e.EntriesPerKey <= 0 {
		t.Skip("ReplayEnv.EntriesPerKey not set")
	}
	for i := range e.EntriesPerKey {
		if e.seen(t, e.entry(jkt)) {
			t.Fatalf("id %d of %d within the key's share was refused", i+1, e.EntriesPerKey)
		}
	}
	if !e.seen(t, e.entry(jkt)) {
		t.Error("a key past its share recorded another id")
	}
	if e.seen(t, e.entry(otherJKT)) {
		t.Error("one key's full share refused another key")
	}
}

func checkConcurrentReplay(t *testing.T, e ReplayEnv) {
	entry := e.entry(jkt)
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		accepted int
	)
	for range concurrentProofs {
		wg.Go(func() {
			replay, err := e.Cache.Seen(e.Ctx, entry)
			if err != nil {
				t.Errorf("Seen: %v", err)
				return
			}
			if !replay {
				mu.Lock()
				accepted++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if accepted != 1 {
		t.Errorf("%d of %d concurrent proofs carrying one id were accepted, want 1", accepted, concurrentProofs)
	}
}

func (e NoncesEnv) nonce(t *testing.T) string {
	t.Helper()
	n, err := e.Nonces.Nonce(e.Ctx)
	if err != nil {
		t.Fatalf("Nonce: %v", err)
	}
	if n == "" {
		t.Fatal("Nonce issued an empty nonce")
	}
	return n
}

func (e NoncesEnv) valid(t *testing.T, nonce string) bool {
	t.Helper()
	ok, err := e.Nonces.Valid(e.Ctx, nonce)
	if err != nil {
		t.Fatalf("Valid(%q): %v", nonce, err)
	}
	return ok
}

func checkIssued(t *testing.T, e NoncesEnv) {
	if n := e.nonce(t); !e.valid(t, n) {
		t.Errorf("a nonce just issued, %q, is not valid", n)
	}
}

func checkForeign(t *testing.T, e NoncesEnv) {
	issued := e.nonce(t)
	// The issued nonce with its first character changed: what a client
	// that forged or mangled one sends.
	altered := []byte(issued)
	if altered[0] == 'A' {
		altered[0] = 'B'
	} else {
		altered[0] = 'A'
	}
	for _, n := range []string{"", notANonce, string(altered)} {
		if e.valid(t, n) {
			t.Errorf("Valid(%q) = true for a nonce never issued", n)
		}
	}
}

func checkExpiry(t *testing.T, e NoncesEnv) {
	if e.Advance == nil || e.Lifetime <= 0 {
		t.Skip("NoncesEnv.Lifetime and Advance not set")
	}
	n := e.nonce(t)
	e.Advance(e.Lifetime / 2)
	if !e.valid(t, n) {
		t.Fatal("a nonce expired within its lifetime")
	}
	e.Advance(e.Lifetime)
	if e.valid(t, n) {
		t.Error("a nonce is valid past its lifetime")
	}
}

// checkShareFrees holds a cache to forgetting what expired: a key that filled
// its share is not locked out for good once its ids lapse.
func checkShareFrees(t *testing.T, e ReplayEnv) {
	if e.EntriesPerKey <= 0 || e.Advance == nil || e.Now == nil {
		t.Skip("ReplayEnv.EntriesPerKey, Now and Advance not set")
	}
	for range e.EntriesPerKey {
		e.seen(t, e.entry(jkt))
	}
	e.Advance(proofLifetime + tick)
	if e.seen(t, e.entry(jkt)) {
		t.Error("a key's share stayed full after every id in it expired")
	}
}
