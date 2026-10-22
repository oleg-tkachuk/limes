package ttlcache

import (
	"context"
	"errors"
	"testing"
)

// The script FuzzCounters runs is its input, a byte a step: the low bits
// pick the step, the rest the key it is about. Few keys over a smaller
// bound, so steps keep finding a key cached, expired, cleared and evicted.
const (
	fuzzKeys       = 8
	fuzzMaxEntries = 3
	fuzzStepBits   = 3
	fuzzStepMask   = 1<<fuzzStepBits - 1
	// fuzzMaxSteps bounds one script, so a long input does not make one
	// run of many seconds.
	fuzzMaxSteps = 512
)

// The steps of a script.
const (
	stepGet = iota
	stepGetAgain
	stepGetFailing
	stepCached
	stepSeedLive
	stepClear
	stepSweep
	stepAdvance
)

// errFuzzLookup is the upstream failing.
var errFuzzLookup = errors.New("upstream down")

// fuzzModel is what the cache may answer, kept apart from how it keeps it:
// until when an answer about each key may be served, and which answers.
type fuzzModel struct {
	until   map[int]int64 // nanoseconds of the clock; absent: none may be
	answers map[int]map[bool]bool
}

func (m fuzzModel) mayServe(key int, now int64, revoked bool) bool {
	return now < m.until[key] && m.answers[key][revoked]
}

func (m fuzzModel) forget() {
	clear(m.until)
	clear(m.answers)
}

// FuzzCounters runs a script of calls on one goroutine and holds the cache,
// after every step, to what RevocationCache.Stats promises its readers: an
// answer is served only while fresh and only as last looked up or seeded,
// never past a Clear; every question is one hit or one miss, and each miss
// one lookup, as no two callers share one here; an eviction is a live entry
// dropped to make room for another, never a sweep; and the cache holds no
// more than its bound. The waiters' path needs concurrent callers and is
// TestAWaiterThatAsksAgainIsOneMiss's.
func FuzzCounters(f *testing.F) {
	key := func(step, k int) byte { return byte(k<<fuzzStepBits | step) }
	// A hit, then a miss once the answer expires.
	f.Add([]byte{key(stepGet, 1), key(stepGetAgain, 1), key(stepAdvance, 0), key(stepAdvance, 0), key(stepAdvance, 0), key(stepGet, 1)})
	// More keys than the bound: evictions, then a sweep that evicts nothing.
	f.Add([]byte{key(stepGet, 0), key(stepGet, 1), key(stepGet, 2), key(stepGet, 3), key(stepGet, 4), key(stepAdvance, 0), key(stepAdvance, 0), key(stepAdvance, 0), key(stepSweep, 0)})
	// A clear between two questions about one key.
	f.Add([]byte{key(stepGet, 5), key(stepClear, 0), key(stepGetAgain, 5), key(stepCached, 5)})
	// A failing lookup caches nothing; a seeded key answers Cached.
	f.Add([]byte{key(stepGetFailing, 6), key(stepGet, 6), key(stepSeedLive, 7), key(stepCached, 7)})
	f.Fuzz(func(t *testing.T, script []byte) {
		if len(script) > fuzzMaxSteps {
			script = script[:fuzzMaxSteps]
		}
		c, clk := newCache(fuzzMaxEntries)
		m := fuzzModel{until: map[int]int64{}, answers: map[int]map[bool]bool{}}
		ctx := context.Background()
		var hits, lookups uint64
		var answer bool // the next lookup's answer, which alternates
		var last Stats
		for i, b := range script {
			k := int(b>>fuzzStepBits) % fuzzKeys
			key := keyName(k)
			now := clk.Now().UnixNano()
			inserts := 0
			switch step := int(b & fuzzStepMask); step {
			case stepGet, stepGetAgain, stepGetFailing:
				looked := false
				got, _, err := c.Get(ctx, key, func(context.Context) (bool, error) {
					looked = true
					if step == stepGetFailing {
						return false, errFuzzLookup
					}
					answer = !answer
					return answer, nil
				})
				if !looked {
					hits++
					if !m.mayServe(k, now, got) {
						t.Fatalf("step %d: key %d served %v from the cache, which may not serve it", i, k, got)
					}
					break
				}
				lookups++
				if err == nil {
					inserts = 1
					m.until[k] = now + int64(testTTL)
					m.answers[k] = map[bool]bool{got: true}
				}
			case stepCached:
				other := (k + 1) % fuzzKeys
				revoked, missing, _ := c.Cached([]string{key, keyName(other)})
				if len(missing) > 0 {
					break
				}
				hits++
				if !revoked && (!m.mayServe(k, now, false) || !m.mayServe(other, now, false)) {
					t.Fatalf("step %d: keys %d and %d answered live from the cache, which may not say so", i, k, other)
				}
				if revoked && !m.mayServe(k, now, true) && !m.mayServe(other, now, true) {
					t.Fatalf("step %d: keys %d and %d answered revoked from the cache, which may not say so", i, k, other)
				}
			case stepSeedLive:
				// The epoch a seed is read in comes with a question, of no
				// keys: answered in full, so a hit.
				_, _, epoch := c.Cached(nil)
				hits++
				expires := now + int64(testTTL)/2
				// Read half a TTL ago: fresh for half a TTL more.
				c.SeedLive([]string{key}, epoch, clk.Now().Add(-testTTL/2))
				inserts = 1
				// Seeded over an answer the cache dropped, or kept beside
				// one it holds: either may be served after.
				if now >= m.until[k] {
					m.answers[k] = map[bool]bool{}
				}
				m.answers[k][false] = true
				m.until[k] = max(m.until[k], expires)
			case stepClear:
				c.Clear()
				m.forget()
			case stepSweep:
				c.Sweep()
			case stepAdvance:
				clk.advance(testTTL / 3)
			}

			st := c.Stats()
			if st.Hits != hits || st.Misses != lookups || st.Lookups != lookups {
				t.Fatalf("step %d: counted hits %d, misses %d, lookups %d; asked %d hits and %d lookups", i, st.Hits, st.Misses, st.Lookups, hits, lookups)
			}
			if st.Entries != c.Len() || st.Entries > fuzzMaxEntries {
				t.Fatalf("step %d: %d entries, Len %d, bound %d", i, st.Entries, c.Len(), fuzzMaxEntries)
			}
			if st.Evictions < last.Evictions || st.Evictions-last.Evictions > uint64(inserts) {
				t.Fatalf("step %d: evictions went from %d to %d on a step that cached %d answers", i, last.Evictions, st.Evictions, inserts)
			}
			last = st
		}
	})
}

// keyName is key k as the cache holds it.
func keyName(k int) string { return string(rune('a' + k)) }
