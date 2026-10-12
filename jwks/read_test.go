package jwks

import (
	"math"
	"strings"
	"testing"
)

// readCapped takes a document up to its limit and refuses one past it, and
// at the largest limit an int holds still reads the document rather than
// overflow the one byte it reads past the limit.
func TestReadCapped(t *testing.T) {
	const limit = 4
	doc := strings.Repeat("x", limit)
	cases := map[string]struct {
		body     string
		maxBytes int
		wantErr  bool
	}{
		"empty":           {"", limit, false},
		"at the limit":    {doc, limit, false},
		"one past":        {doc + "x", limit, true},
		"limit 0, empty":  {"", 0, false},
		"limit 0, a byte": {"x", 0, true},
		"the largest int": {doc, math.MaxInt, false},
	}
	for name, tc := range cases {
		got, err := readCapped(strings.NewReader(tc.body), tc.maxBytes)
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: err = %v, want error %v", name, err, tc.wantErr)
		}
		if !tc.wantErr && string(got) != tc.body {
			t.Errorf("%s: read %q, want %q", name, got, tc.body)
		}
	}
}
