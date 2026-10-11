package limes

import (
	"context"
	"errors"
	"testing"
	"time"
)

// RFC 3986 §6.2.2 and §6.2.3, as RFC 9449 §4.3 applies them to htu.
func TestNormalizeURI(t *testing.T) {
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
		if got, err := normalizeURI(in); err != nil || got != want {
			t.Errorf("normalizeURI(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"/relative/path", "api.example.com/a", "https://", "://x", "a://:", "https://user:pw@api.example.com/a"} {
		if _, err := normalizeURI(in); err == nil {
			t.Errorf("normalizeURI(%q) accepted a URI that is not absolute", in)
		}
	}
}

// RFC 3986 §5.4's examples of remove_dot_segments.
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
	}
	for in, want := range cases {
		if got := removeDotSegments(in); got != want {
			t.Errorf("removeDotSegments(%q) = %q, want %q", in, got, want)
		}
	}
}

// A proof is accepted for the request URL however either is spelled, as long
// as both normalise to the same URI.
func TestDPoPMatchesNormalizedURIs(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	key := dpopKeys(t)["Ed25519"]
	c := boundCap(t, key)
	for _, tc := range []struct {
		proofURL, requestURL string
		ok                   bool
	}{
		{"https://data.example.com:443/v1/%7Eobj", "https://DATA.example.com/v1/~obj", true},
		{"https://data.example.com/v1/a/../obj", "https://data.example.com/v1/obj", true},
		{"https://data.example.com/v1/obj", "https://data.example.com/v1/obj/", false},
		{"https://data.example.com/v1/obj", "http://data.example.com/v1/obj", false},
		{"https://data.example.com/v1/obj", "https://data.example.com:8443/v1/obj", false},
	} {
		v := &DPoPVerifier{Replay: NewMemoryReplayCache(0), Now: func() time.Time { return now }}
		proof, err := NewDPoPProof(key, DPoPProofRequest{Method: dpopMethod, URL: tc.proofURL, Token: "tok", Now: now})
		if err != nil {
			t.Fatal(err)
		}
		err = v.check(context.Background(), c, "tok", DPoPProof{Proof: proof, Method: dpopMethod, URL: tc.requestURL})
		if tc.ok && err != nil {
			t.Errorf("proof for %s, request %s: %v", tc.proofURL, tc.requestURL, err)
		}
		if !tc.ok && !errors.Is(err, ErrDPoPInvalid) {
			t.Errorf("proof for %s, request %s: err = %v, want ErrDPoPInvalid", tc.proofURL, tc.requestURL, err)
		}
	}
}

// Under MatchPathOnly the proof's htu is still a URI the request could have
// been made to — absolute, with a host — and a request with no path matches
// nothing.
func TestHTUPathOnlyStillReadsTheProof(t *testing.T) {
	cases := []struct {
		htu, request string
		ok           bool
	}{
		{"https://gw.example.com/api/v1/objects", "http://10.0.0.7:8080/v1/objects", true},
		{"https://gw.example.com/v1/objects", "/v1/objects", true},
		{"x/v1/objects", "/v1/objects", false},          // relative htu
		{"urn:anything:/", "/", false},                  // no host
		{"https://u:p@gw.example.com/v1", "/v1", false}, // userinfo
		{"https://gw.example.com/anything/", "", false}, // no request path
		{"https://gw.example.com/a/../v1/x", "/v1/x", true},
	}
	for _, tc := range cases {
		if got := htuMatches(tc.htu, tc.request, true); got != tc.ok {
			t.Errorf("htuMatches(%q, %q, pathOnly) = %v, want %v", tc.htu, tc.request, got, tc.ok)
		}
	}
}
