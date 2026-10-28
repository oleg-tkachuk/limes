package rfc3986

import "testing"

// RFC 3986 §6.2.2 and §6.2.3, as RFC 9449 §4.3 applies them to htu.
func TestNormalize(t *testing.T) {
	cases := map[string]string{
		"https://api.example.com/a/b":        "https://api.example.com/a/b",
		"HTTPS://API.Example.COM/a/b":        "https://api.example.com/a/b", // case of scheme and host only
		"https://api.example.com:443/a":      "https://api.example.com/a",   // default port
		"http://api.example.com:80/a":        "http://api.example.com/a",
		"https://api.example.com:8443/a":     "https://api.example.com:8443/a",
		"https://api.example.com":            "https://api.example.com/", // empty path
		"https://api.example.com/%7euser":    "https://api.example.com/~user",
		"https://api.example.com/a%2fb":      "https://api.example.com/a%2Fb", // reserved: kept encoded, hex upper
		"https://api.example.com/a/./b/../c": "https://api.example.com/a/c",
		"https://api.example.com/a/b/":       "https://api.example.com/a/b/", // a trailing slash is another resource
		"https://api.example.com/a//b":       "https://api.example.com/a//b",
		"https://api.example.com/a?x=1#f":    "https://api.example.com/a",
		"https://[2001:DB8::1]:443/a":        "https://[2001:db8::1]/a",
		"https://[2001:db8::1]:8443/a":       "https://[2001:db8::1]:8443/a",
		"https://api.example.com/%41%62":     "https://api.example.com/Ab",
		"https://api.example.com/caf%c3%a9":  "https://api.example.com/caf%C3%A9",
		"https://api.example.com/../../a":    "https://api.example.com/a",
		"https://api.example.com/a/b/..":     "https://api.example.com/a/",
	}
	for in, want := range cases {
		if got, err := Normalize(in); err != nil || got != want {
			t.Errorf("Normalize(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"/relative/path", "api.example.com/a", "https://", "://x", "a://:", "https://user:pw@api.example.com/a"} {
		if _, err := Normalize(in); err == nil {
			t.Errorf("Normalize(%q) accepted a URI that is not absolute", in)
		}
	}
}

// RFC 3986 §5.4's examples of remove_dot_segments, and each rule of
// §5.2.4 on its own.
func TestRemoveDotSegments(t *testing.T) {
	cases := map[string]string{
		"/a/b/c/./../../g":   "/a/g",
		"mid/content=5/../6": "mid/6",
		"/./g":               "/g",
		"/../g":              "/g",
		"/g.":                "/g.",
		"/.g":                "/.g",
		"/g..":               "/g..",
		"/..g":               "/..g",
		"/b/c/./g/.":         "/b/c/g/",
		"/b/c/g/./h":         "/b/c/g/h",
		"/b/c/g/../h":        "/b/c/h",
		// Each rule of §5.2.4 step 2 on its own: A's leading "../" and "./",
		// C's trailing "/..", and D's lone "." and "..".
		"../a":    "a",
		"./a":     "a",
		"/a/b/..": "/a/",
		".":       "",
		"..":      "",
	}
	for in, want := range cases {
		if got := removeDotSegments(in); got != want {
			t.Errorf("removeDotSegments(%q) = %q, want %q", in, got, want)
		}
	}
}

// FuzzNormalize holds the htu normalisation to being a normal form:
// normalising what it returns changes nothing.
func FuzzNormalize(f *testing.F) {
	for _, s := range []string{"https://A.example:443/a/./b/../%7e?q#f", "http://[::1]:80", "https://h/%zz/.."} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		once, err := Normalize(raw)
		if err != nil {
			return
		}
		twice, err := Normalize(once)
		if err != nil || twice != once {
			t.Fatalf("%q normalises to %q, which normalises to %q, %v", raw, once, twice, err)
		}
	})
}
