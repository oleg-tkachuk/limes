package ttlcache

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

// The steps of a FuzzTaggedAnswers script. A step is two bytes: the first
// picks the step, modulo taggedSteps; the second's low bits a key, and its
// next taggedKeys bits a set of keys — the tags a live answer names, or the
// keys a Forget outdates.
const (
	taggedStepGetLive = iota
	taggedStepGetRevoked
	taggedStepGetFailing
	taggedStepForget
	taggedStepClear
	taggedStepSweep
	taggedStepAdvance
	taggedStepAdvanceLong
	taggedSteps
)

const (
	taggedStepSize = 2
	taggedKeys     = 4
	taggedKeyMask  = taggedKeys - 1
	taggedKeyBits  = 2
	taggedSetMask  = 1<<taggedKeys - 1
	// taggedRoomy holds every key with room to spare, so no answer is
	// evicted; taggedTight holds fewer than the keys, so answers are.
	taggedRoomy = 64
	taggedTight = 2
)

// taggedAnswer is the newest answer the upstream gave about a key, when,
// and the tags it named; held is false once the cache was told to drop it.
type taggedAnswer struct {
	revoked bool
	at      time.Time
	tags    []int
	held    bool
}

// taggedModel is what the cache was told about each key.
type taggedModel map[int]taggedAnswer

// revokedWithin reports whether the upstream last answered key revoked no
// longer than keep before now, and the cache was not told to drop it since:
// a revocation it learned that no answer about a question spanning key may
// undo while it lasts.
func (m taggedModel) revokedWithin(key int, now time.Time, keep time.Duration) bool {
	a, ok := m[key]
	return ok && a.held && a.revoked && now.Before(a.at.Add(keep))
}

// keysOf is the keys set names.
func keysOf(set int) []int {
	var keys []int
	for k := range taggedKeys {
		if set&(1<<k) != 0 {
			keys = append(keys, k)
		}
	}
	return keys
}

// FuzzTaggedAnswers runs a script of tagged lookups, failures, Forgets,
// clears and sweeps, on a cache that holds every key or fewer than them,
// and holds every answer to the cache's promises: a fresh answer is the
// upstream's last; a stale one stands only for a failed lookup, as the
// upstream last answered within TTL and MaxStale; and no revocation is
// undone — a stale live answer whose tags include a key the upstream
// answered revoked within that window does not stand, whether or not the
// cache still holds that answer.
func FuzzTaggedAnswers(f *testing.F) {
	step := func(s, key int, set int) []byte {
		return []byte{byte(s), byte(set<<taggedKeyBits | key)}
	}
	// A live answer tagged with a key answered revoked since, then a
	// failure past the TTL: the revocation stands.
	f.Add(slices.Concat([]byte{0}, step(taggedStepGetLive, 0, 0b0010), step(taggedStepGetRevoked, 1, 0),
		step(taggedStepAdvance, 0, 0), step(taggedStepAdvance, 0, 0), step(taggedStepAdvance, 0, 0), step(taggedStepGetFailing, 0, 0)))
	// The same where the cache holds fewer answers than the keys asked.
	f.Add(slices.Concat([]byte{1}, step(taggedStepGetRevoked, 1, 0), step(taggedStepGetLive, 0, 0b0010), step(taggedStepGetLive, 2, 0),
		step(taggedStepGetLive, 3, 0), step(taggedStepAdvance, 0, 0), step(taggedStepAdvance, 0, 0), step(taggedStepAdvance, 0, 0), step(taggedStepGetFailing, 0, 0)))
	// A Forget of a tag drops the live answer naming it, not a revoked one.
	f.Add(slices.Concat([]byte{0}, step(taggedStepGetLive, 0, 0b0100), step(taggedStepGetRevoked, 3, 0), step(taggedStepForget, 0, 0b1100),
		step(taggedStepAdvance, 0, 0), step(taggedStepAdvance, 0, 0), step(taggedStepAdvance, 0, 0), step(taggedStepGetFailing, 0, 0), step(taggedStepGetFailing, 3, 0)))
	f.Fuzz(func(t *testing.T, script []byte) {
		if len(script) == 0 {
			return
		}
		bound := taggedRoomy
		if script[0]&1 != 0 {
			bound = taggedTight
		}
		script = script[1:]
		if len(script) > fuzzMaxSteps*taggedStepSize {
			script = script[:fuzzMaxSteps*taggedStepSize]
		}
		c, clk, _ := staleCache(bound)
		keep := testTTL + testMaxStale
		m := taggedModel{}
		ctx := context.Background()
		for i := 0; i+taggedStepSize <= len(script); i += taggedStepSize {
			at, arg := i/taggedStepSize, int(script[i+1])
			k, set := arg&taggedKeyMask, arg>>taggedKeyBits&taggedSetMask
			now := clk.Now()
			switch s := int(script[i]) % taggedSteps; s {
			case taggedStepGetLive, taggedStepGetRevoked, taggedStepGetFailing:
				looked := false
				tags := slices.DeleteFunc(keysOf(set), func(t int) bool { return t == k })
				got, _, err := c.GetTagged(ctx, keyName(k), func(context.Context) (bool, []string, error) {
					looked = true
					if s == taggedStepGetFailing {
						return false, nil, errDown
					}
					var names []string
					for _, t := range tags {
						names = append(names, keyName(t))
					}
					return s == taggedStepGetRevoked, names, nil
				})
				a, ok := m[k]
				switch {
				case !looked:
					if err != nil || !ok || !a.held || got != a.revoked || !now.Before(a.at.Add(testTTL)) {
						t.Fatalf("step %d: key %d answered %v, %v from the cache; the upstream last said %+v", at, k, got, err, a)
					}
				case s != taggedStepGetFailing:
					if err != nil {
						t.Fatalf("step %d: key %d: %v", at, k, err)
					}
					m[k] = taggedAnswer{revoked: s == taggedStepGetRevoked, at: now, tags: tags, held: true}
				case err != nil:
					if !errors.Is(err, errDown) {
						t.Fatalf("step %d: key %d: the failure came back as %v", at, k, err)
					}
				case !ok || !a.held || !now.Before(a.at.Add(keep)):
					t.Fatalf("step %d: key %d served %v stale, with no answer of the upstream's within TTL and MaxStale: %+v", at, k, got, a)
				case !got && a.revoked:
					t.Fatalf("step %d: key %d served live stale, last answered revoked", at, k)
				case !got:
					for _, tag := range a.tags {
						if m.revokedWithin(tag, now, keep) {
							t.Fatalf("step %d: key %d served live stale over its tag %d, answered revoked at %v (bound %d)", at, k, tag, m[tag].at, bound)
						}
					}
				}
			case taggedStepForget:
				gone := keysOf(set)
				names := make([]string, len(gone))
				for j, g := range gone {
					names[j] = keyName(g)
				}
				c.Forget(names)
				for key, a := range m {
					if !a.revoked && (slices.Contains(gone, key) || slices.ContainsFunc(a.tags, func(t int) bool { return slices.Contains(gone, t) })) {
						a.held = false
						m[key] = a
					}
				}
			case taggedStepClear:
				c.Clear()
				clear(m)
			case taggedStepSweep:
				c.Sweep()
			case taggedStepAdvance:
				clk.advance(testTTL / 3)
			case taggedStepAdvanceLong:
				clk.advance(staleFuzzLongStep)
			}
		}
	})
}
