package jwks

import "testing"

// A kid is limes's when it opens with KeyIDPrefix and names something after
// it, spelled exactly.
func TestIsKeyID(t *testing.T) {
	for kid, want := range map[string]bool{
		KeyIDPrefix + "k1":       true,
		"k1":                     false,
		KeyIDPrefix:              false,
		"":                       false,
		"LIMES:k1":               false,
		" " + KeyIDPrefix + "k1": false,
	} {
		if got := IsKeyID(kid); got != want {
			t.Errorf("IsKeyID(%q) = %v, want %v", kid, got, want)
		}
	}
}
