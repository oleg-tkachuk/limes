package storable

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestText(t *testing.T) {
	for name, tc := range map[string]struct {
		texts []string
		want  bool
	}{
		"none":                     {nil, true},
		"empty":                    {[]string{""}, true},
		"ascii":                    {[]string{"agent:alpha", "note"}, true},
		"multibyte":                {[]string{"naïve", "日本", "🙂"}, true},
		"a control other than NUL": {[]string{"tab\there\n"}, true},
		"NUL":                      {[]string{"a\x00b"}, false},
		"NUL alone":                {[]string{"\x00"}, false},
		"invalid UTF-8":            {[]string{"\xff"}, false},
		"a truncated rune":         {[]string{"\xe6\x97"}, false},
		"a surrogate":              {[]string{"\xed\xa0\x80"}, false},
		"one bad among good":       {[]string{"ok", "fine", "bad\x00"}, false},
	} {
		if got := Text(tc.texts...); got != tc.want {
			t.Errorf("%s: Text(%q) = %v, want %v", name, tc.texts, got, tc.want)
		}
	}
}

// FuzzText holds Text to its rule, written out another way: accepted is
// exactly what decodes to runes with no NUL and no replacement character
// standing for a byte that was not one.
func FuzzText(f *testing.F) {
	for _, s := range []string{"", "a", "\x00", "\xff", "naïve", "�"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		want := true
		for i, r := range s {
			if r == 0 || r == utf8.RuneError && !strings.HasPrefix(s[i:], "�") {
				want = false
			}
		}
		if got := Text(s); got != want {
			t.Fatalf("Text(%q) = %v, want %v", s, got, want)
		}
	})
}
