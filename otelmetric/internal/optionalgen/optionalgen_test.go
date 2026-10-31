package optionalgen

import (
	"slices"
	"testing"
)

func TestMixesAreEverySubsetLargestFirst(t *testing.T) {
	got := mixes(3)
	want := [][]int{{0, 1, 2}, {0, 1}, {0, 2}, {1, 2}, {0}, {1}, {2}}
	if !slices.EqualFunc(got, want, slices.Equal) {
		t.Fatalf("mixes(3) = %v, want %v", got, want)
	}
	if n := len(mixes(len(Optional))); n != 1<<len(Optional)-1 {
		t.Errorf("%d mixes of %d interfaces, want %d", n, len(Optional), 1<<len(Optional)-1)
	}
}
