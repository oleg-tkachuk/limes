package ttlcache

import (
	"context"
	"strconv"
	"testing"
)

// benchFullSizes are the bounds the full-cache benchmark fills: one shard,
// several, and limes's default.
var benchFullSizes = []int{minShardEntries, 1 << 14, 100_000}

// live is a lookup answering not revoked.
func live(context.Context) (bool, error) { return false, nil }

// An answer cached into a cache already full of fresh ones, on a clock that
// stands still so none expires: every insert makes room by evicting.
func BenchmarkInsertWhenFull(b *testing.B) {
	for _, size := range benchFullSizes {
		b.Run(strconv.Itoa(size), func(b *testing.B) {
			c, _ := newCache(size)
			ctx := b.Context()
			for i := range size {
				if _, _, err := c.Get(ctx, strconv.Itoa(i), live); err != nil {
					b.Fatal(err)
				}
			}
			next := size
			for b.Loop() {
				if _, _, err := c.Get(ctx, strconv.Itoa(next), live); err != nil {
					b.Fatal(err)
				}
				next++
			}
		})
	}
}

// benchParent is the key the answer about child i is tagged with, and
// benchGrandparent the second, which a tenth of the answers share.
func benchParent(i int) string      { return "p" + strconv.Itoa(i) }
func benchGrandparent(i int) string { return "g" + strconv.Itoa(i/10) }

// A Forget of one parent in a cache half full of answers tagged with their
// parents — half, so none has made room for another in a shard the keys
// happen to crowd: it drops one answer, which is cached again. Its cost
// should not grow with the size of the cache.
func BenchmarkForget(b *testing.B) {
	for _, size := range benchFullSizes {
		b.Run(strconv.Itoa(size), func(b *testing.B) {
			c, _ := newCache(size)
			ctx := b.Context()
			child := func(i int) func(context.Context) (bool, []string, error) {
				return func(context.Context) (bool, []string, error) {
					return false, []string{benchParent(i), benchGrandparent(i)}, nil
				}
			}
			filled := size / 2
			for i := range filled {
				if _, _, err := c.GetTagged(ctx, strconv.Itoa(i), child(i)); err != nil {
					b.Fatal(err)
				}
			}
			next := 0
			for b.Loop() {
				i := next % filled
				if dropped := c.Forget([]string{benchParent(i)}); dropped != 1 {
					b.Fatalf("Forget dropped %d answers, want 1", dropped)
				}
				if _, _, err := c.GetTagged(ctx, strconv.Itoa(i), child(i)); err != nil {
					b.Fatal(err)
				}
				next++
			}
		})
	}
}
