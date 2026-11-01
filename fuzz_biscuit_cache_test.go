package limes

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"
)

// The steps of a FuzzBiscuitRevocations script. A step is two bytes: the
// first picks the step, modulo biscuitSteps; the second is its argument —
// in its lowest two bits how many blocks of one chain a copy has (one more)
// or which id to revoke, in its low four a count.
const (
	biscuitStepAsk = iota
	biscuitStepRevoke
	biscuitStepFail
	biscuitStepRecover
	biscuitStepAdvance
	biscuitStepAdvanceLong
	biscuitStepClear
	// biscuitStepFlood asks about that many ids never asked before, all
	// live, so a bounded cache makes room.
	biscuitStepFlood
	biscuitSteps
)

const (
	biscuitStepSize = 2
	biscuitIDs      = 4
	biscuitIDMask   = biscuitIDs - 1
	biscuitFloodMax = 1<<4 - 1
	// biscuitMaxSteps bounds one script.
	biscuitMaxSteps = 128
	// biscuitTight holds fewer answers than one question can leave, in one
	// shard; biscuitWide spreads its answers over shards, so a set and an
	// id it holds can sit apart.
	biscuitTight = 3
	biscuitWide  = 600
)

// biscuitUpstream answers whether any id asked is revoked, or fails.
type biscuitUpstream struct {
	revoked map[string]bool
	down    bool
}

func (u *biscuitUpstream) IsBiscuitRevoked(_ context.Context, ids [][]byte) (bool, error) {
	if u.down {
		return false, errUpstreamDown
	}
	return slices.ContainsFunc(ids, func(id []byte) bool { return u.revoked[string(id)] }), nil
}

// learnedRevoked is a question the cache answered revoked, and when.
type learnedRevoked struct {
	ids []string
	at  time.Time
}

// FuzzBiscuitRevocations asks a RevocationCache with MaxStale about copies
// of one Biscuit — the revocation ids of a chain of blocks, authority first,
// each copy's a prefix of the copies attenuated from it — while ids are
// revoked upstream, the upstream fails and recovers, time passes, the cache
// is cleared and flooded past its bound, and holds every answer to the
// cache's promise: a revocation it learned is not undone — no copy it
// answered revoked, nor any copy attenuated from it, is answered live while
// that answer would still be kept, unless the cache was cleared since.
// With the upstream up, no question fails.
func FuzzBiscuitRevocations(f *testing.F) {
	step := func(s, arg int) []byte { return []byte{byte(s), byte(arg)} }
	// A copy live, its second block revoked and learned, a flood, a failure.
	f.Add(slices.Concat([]byte{1}, step(biscuitStepAsk, 1), step(biscuitStepAdvance, 0), step(biscuitStepAdvance, 0),
		step(biscuitStepAdvance, 0), step(biscuitStepRevoke, 1), step(biscuitStepAsk, 1), step(biscuitStepFlood, biscuitFloodMax),
		step(biscuitStepFail, 0), step(biscuitStepAsk, 1), step(biscuitStepAsk, 2)))
	// A copy's last block revoked and learned through its last two ids, the
	// copy it was attenuated from read live since, then a failure: the
	// revoked block's own answer is stale and live.
	f.Add(slices.Concat([]byte{0}, step(biscuitStepAsk, 2), step(biscuitStepAdvance, 0), step(biscuitStepAdvance, 0),
		step(biscuitStepAdvance, 0), step(biscuitStepRevoke, 2), step(biscuitStepAsk, 0), step(biscuitStepAsk, 2),
		step(biscuitStepAsk, 1), step(biscuitStepFail, 0), step(biscuitStepAsk, 2)))
	f.Fuzz(func(t *testing.T, script []byte) {
		if len(script) == 0 {
			return
		}
		bound := biscuitWide
		if script[0]&1 != 0 {
			bound = biscuitTight
		}
		script = script[1:]
		if len(script) > biscuitMaxSteps*biscuitStepSize {
			script = script[:biscuitMaxSteps*biscuitStepSize]
		}
		up := &biscuitUpstream{revoked: map[string]bool{}}
		clk := &stepClock{now: time.Unix(0, 0)}
		c := newRevocationCache(t, RevocationCacheConfig{
			Capabilities: &flakyUpstream{}, Biscuits: up, TTL: cacheTTL, MaxStale: cacheMaxStale, MaxEntries: bound, Now: clk.Now,
		})
		keep := cacheTTL + cacheMaxStale
		var learned []learnedRevoked
		flooded := 0
		for i := 0; i+biscuitStepSize <= len(script); i += biscuitStepSize {
			at, arg := i/biscuitStepSize, int(script[i+1])
			switch int(script[i]) % biscuitSteps {
			case biscuitStepAsk:
				var ids []string
				for k := range arg&biscuitIDMask + 1 {
					ids = append(ids, string(rune('a'+k)))
				}
				asked := make([][]byte, len(ids))
				for k, id := range ids {
					asked[k] = []byte(id)
				}
				revoked, err := c.IsBiscuitRevoked(t.Context(), asked)
				now := clk.Now()
				switch {
				case err != nil:
					if !up.down {
						t.Fatalf("step %d: %v answered %v with the upstream up", at, ids, err)
					}
				case revoked:
					learned = append(learned, learnedRevoked{ids: ids, at: now})
				default:
					for _, l := range learned {
						if now.Before(l.at.Add(keep)) && len(l.ids) <= len(ids) {
							t.Fatalf("step %d: %v answered live, though %v was answered revoked at %v (bound %d, upstream down %v)", at, ids, l.ids, l.at, bound, up.down)
						}
					}
				}
			case biscuitStepRevoke:
				up.revoked[string(rune('a'+arg&biscuitIDMask))] = true
			case biscuitStepFail:
				up.down = true
			case biscuitStepRecover:
				up.down = false
			case biscuitStepAdvance:
				clk.advance(cacheTTL / 3)
			case biscuitStepAdvanceLong:
				clk.advance(cacheMaxStale / 3)
			case biscuitStepClear:
				c.Clear()
				learned = nil
			case biscuitStepFlood:
				for range arg&biscuitFloodMax + 1 {
					flooded++
					// A failure here is the upstream's; the flood only fills.
					_, _ = c.IsBiscuitRevoked(t.Context(), [][]byte{[]byte(fmt.Sprintf("flood-%d", flooded))})
				}
			}
		}
	})
}
