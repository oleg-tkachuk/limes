package limes

import (
	"crypto/ed25519"
	"testing"
)

// signatures is n revocation ids the size of a block signature, each
// distinct.
func signatures(n int) [][]byte {
	ids := make([][]byte, n)
	for i := range ids {
		ids[i] = make([]byte, ed25519.SignatureSize)
		ids[i][0], ids[i][1] = byte(i), byte(i>>8)
	}
	return ids
}

// A set's key is the same size however many ids it holds, so a copy of
// many blocks costs the cache no more per key than one of two.
func TestASetKeyIsOfFixedSize(t *testing.T) {
	for _, n := range []int{1, 2, MaxBiscuitBlocks + 1} {
		if got := len(setKey(signatures(n))); got != setKeySize {
			t.Errorf("a set of %d ids: key of %d bytes, want %d", n, got, setKeySize)
		}
	}
}

// prefixKeys is setKey of every prefix two ids long or more, in order: the
// key a revoked copy is seeded under is the one its attenuations look for.
func TestPrefixKeysAreThePrefixesSetKeys(t *testing.T) {
	ids := signatures(MaxBiscuitBlocks)
	keys := prefixKeys(ids)
	if len(keys) != len(ids)-1 {
		t.Fatalf("%d prefix keys for %d ids, want %d", len(keys), len(ids), len(ids)-1)
	}
	for i, k := range keys {
		if want := setKey(ids[:i+2]); k != want {
			t.Errorf("prefix of %d ids: key differs from its setKey", i+2)
		}
	}
	if prefixKeys(ids[:1]) != nil {
		t.Error("prefix keys of one id: want none")
	}
}

// No two sets spell one key: not the same bytes split otherwise, nor the
// same ids in another order, nor a set and its own prefix, nor an id and
// the set of it alone.
func TestSetKeysTellSetsApart(t *testing.T) {
	ab, c := []byte("ab"), []byte("c")
	a, bc := []byte("a"), []byte("bc")
	sets := map[string][][]byte{
		"ab|c":  {ab, c},
		"a|bc":  {a, bc},
		"c|ab":  {c, ab},
		"ab":    {ab},
		"abc":   {[]byte("abc")},
		"a|b|c": {a, []byte("b"), c},
	}
	seen := map[string]string{idKey([]byte("ab")): "id ab"}
	for name, ids := range sets {
		k := setKey(ids)
		if other, ok := seen[k]; ok {
			t.Errorf("%s and %s spell one key", name, other)
		}
		seen[k] = name
	}
}
