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
