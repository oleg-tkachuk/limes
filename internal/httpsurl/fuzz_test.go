package httpsurl

import (
	"strings"
	"testing"
)

// leakWindow is the shortest part of a password a refusal must not repeat:
// a %-escape's length, the most of one url.EscapeError quotes.
const leakWindow = len("%zz")

// FuzzParse holds Parse to the two things its callers rely on: what it
// accepts is an https URL with a host, the only place keys are fetched
// from; and what it refuses is said without any part of the URL's password
// that appears nowhere else in it, since a refusal is what reaches a log.
func FuzzParse(f *testing.F) {
	for _, raw := range []string{"https://idp.example/jwks", "http://idp.example/jwks", "https://user:secret@idp.example/jwks", "https://u:" + testPassword + "@idp.example", "https:///jwks", "%"} {
		f.Add(raw)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		u, err := Parse(raw)
		if err == nil {
			if u.Scheme != Scheme || u.Host == "" {
				t.Fatalf("%q accepted as %q", raw, u)
			}
			return
		}
		password, rest, ok := passwordIn(raw)
		if !ok {
			return
		}
		for i := 0; i+leakWindow <= len(password); i++ {
			part := password[i : i+leakWindow]
			if strings.Contains(err.Error(), part) && !strings.Contains(rest, part) {
				t.Fatalf("refusal of %q repeats %q of its password: %v", raw, part, err)
			}
		}
	})
}

// passwordIn is the password raw's userinfo carries as written — the text
// between the authority's first ':' and its last '@' — and raw without it.
func passwordIn(raw string) (password, rest string, ok bool) {
	scheme, after, ok := strings.Cut(raw, "//")
	if !ok {
		return "", "", false
	}
	authority := after
	if end := strings.IndexAny(after, "/?#"); end >= 0 {
		authority = after[:end]
	}
	at := strings.LastIndex(authority, "@")
	if at < 0 {
		return "", "", false
	}
	user, password, ok := strings.Cut(authority[:at], ":")
	if !ok {
		return "", "", false
	}
	return password, scheme + "//" + user + after[at:], true
}
