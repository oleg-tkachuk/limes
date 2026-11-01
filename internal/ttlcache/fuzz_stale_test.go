package ttlcache

import (
	"context"
	"errors"
	"testing"
	"time"
)

// The steps of a FuzzStaleAnswers script, a byte a step as FuzzCounters
// reads its own: the low bits pick the step, the rest the key.
const (
	staleStepGet = iota
	staleStepGetFailing
	staleStepGetAnswered
	staleStepClear
	staleStepSweep
	staleStepAdvance
	staleStepAdvanceLong
	staleStepGetAgain
)

// staleFuzzLongStep is how far staleStepAdvanceLong moves the clock: a few
// of them cross MaxStale.
const staleFuzzLongStep = testMaxStale / 3

// staleFuzzModel is the newest answer the upstream gave about each key and
// when, which is all a stale answer may stand for; Clear forgets them.
type staleFuzzModel struct {
	at      map[int]time.Time
	answers map[int]bool
}

// mayServe reports whether the cache may answer revoked about key at now
// from what it holds: fresh for TTL, and for MaxStale more when stale.
func (m staleFuzzModel) mayServe(key int, now time.Time, revoked bool, stale bool) bool {
	at, ok := m.at[key]
	if !ok || m.answers[key] != revoked {
		return false
	}
	keep := testTTL
	if stale {
		keep += testMaxStale
	}
	return now.Before(at.Add(keep))
}

// FuzzStaleAnswers runs a script of calls on a cache with MaxStale and holds
// every answer to what the cache promises: a stale answer stands only for a
// lookup that failed, as the upstream last answered, no longer than TTL and
// MaxStale after it answered and never past a Clear; an error the upstream
// gives as an answer is returned, never covered; and Stats counts each
// stale answer served. The cache may fail closed where the model allows a
// stale answer, having evicted it to stay within its bound.
func FuzzStaleAnswers(f *testing.F) {
	key := func(step, k int) byte { return byte(k<<fuzzStepBits | step) }
	// A stale answer over a failure, then none once MaxStale has passed.
	f.Add([]byte{key(staleStepGet, 1), key(staleStepAdvance, 0), key(staleStepAdvance, 0), key(staleStepAdvance, 0), key(staleStepGetFailing, 1),
		key(staleStepAdvanceLong, 0), key(staleStepAdvanceLong, 0), key(staleStepAdvanceLong, 0), key(staleStepGetFailing, 1)})
	// An upstream answer is not covered; a Clear drops the stale answer.
	f.Add([]byte{key(staleStepGet, 2), key(staleStepAdvance, 0), key(staleStepAdvance, 0), key(staleStepAdvance, 0), key(staleStepGetAnswered, 2),
		key(staleStepClear, 0), key(staleStepGetFailing, 2)})
	// More keys than the bound, a sweep, then failures.
	f.Add([]byte{key(staleStepGet, 0), key(staleStepGet, 3), key(staleStepGet, 4), key(staleStepGet, 5), key(staleStepAdvanceLong, 0),
		key(staleStepSweep, 0), key(staleStepGetFailing, 0), key(staleStepGetFailing, 5), key(staleStepGetAgain, 5)})
	f.Fuzz(func(t *testing.T, script []byte) {
		if len(script) > fuzzMaxSteps {
			script = script[:fuzzMaxSteps]
		}
		c, clk, served := staleCache(fuzzMaxEntries)
		m := staleFuzzModel{at: map[int]time.Time{}, answers: map[int]bool{}}
		ctx := context.Background()
		var answer bool // the next answer the upstream gives, which alternates
		var stale uint64
		for i, b := range script {
			k := int(b>>fuzzStepBits) % fuzzKeys
			now := clk.Now()
			switch step := int(b & fuzzStepMask); step {
			case staleStepGet, staleStepGetAgain, staleStepGetFailing, staleStepGetAnswered:
				looked := false
				got, _, err := c.Get(ctx, keyName(k), func(context.Context) (bool, error) {
					looked = true
					switch step {
					case staleStepGetFailing:
						return false, errDown
					case staleStepGetAnswered:
						return false, errAnswered
					}
					answer = !answer
					return answer, nil
				})
				switch {
				case !looked:
					if err != nil || !m.mayServe(k, now, got, false) {
						t.Fatalf("step %d: key %d answered %v, %v from the cache, which holds no fresh answer so", i, k, got, err)
					}
				case step == staleStepGetAnswered:
					if !errors.Is(err, errAnswered) {
						t.Fatalf("step %d: key %d: the upstream's answer %v came back as %v, %v", i, k, errAnswered, got, err)
					}
				case step == staleStepGetFailing:
					if err != nil {
						if !errors.Is(err, errDown) {
							t.Fatalf("step %d: key %d: the failure came back as %v", i, k, err)
						}
						break
					}
					stale++
					if !m.mayServe(k, now, got, true) {
						t.Fatalf("step %d: key %d served the stale answer %v, which the upstream did not last give within TTL and MaxStale", i, k, got)
					}
				default:
					if err != nil {
						t.Fatalf("step %d: key %d: %v", i, k, err)
					}
					m.at[k], m.answers[k] = now, got
				}
			case staleStepClear:
				c.Clear()
				clear(m.at)
				clear(m.answers)
			case staleStepSweep:
				c.Sweep()
			case staleStepAdvance:
				clk.advance(testTTL / 3)
			case staleStepAdvanceLong:
				clk.advance(staleFuzzLongStep)
			}
			if st := c.Stats(); st.Stale != stale || uint64(len(*served)) != stale {
				t.Fatalf("step %d: counted %d stale answers, told OnStale of %d; served %d", i, st.Stale, len(*served), stale)
			}
			if c.Len() > fuzzMaxEntries {
				t.Fatalf("step %d: %d entries over the bound %d", i, c.Len(), fuzzMaxEntries)
			}
		}
	})
}
