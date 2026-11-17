package httpsurl

import "testing"

// disguise moves every letter and digit within its shape class and keeps
// every other byte, which FuzzParse relies on to tell a refusal's own words
// from a quoted password.
func TestDisguiseKeepsTheShapeAndMovesEveryLetterAndDigit(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"ali ", "bmj "},
		{"09fzFZ", "10agAG"},
		{"%2f!", "%3a!"},
		{"!!!", "!!!"},
		{"", ""},
	} {
		if got := disguise(c.in); got != c.want {
			t.Errorf("disguise(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
