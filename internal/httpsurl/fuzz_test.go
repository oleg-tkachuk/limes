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
		// What the refusal of raw without its password, or with another
		// password of the same shape, says too came from elsewhere: the
		// refusal's own words, or the rest of the URL.
		without := refusal(rest)
		disguised := refusal(strings.Replace(raw, ":"+password+"@", ":"+disguise(password)+"@", 1))
		for i := 0; i+leakWindow <= len(password); i++ {
			part := password[i : i+leakWindow]
			if strings.Contains(err.Error(), part) && !strings.Contains(rest, part) && !strings.Contains(without, part) && !strings.Contains(disguised, part) {
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

// refusal is what Parse says refusing raw, or nothing when it accepts it.
func refusal(raw string) string {
	if _, err := Parse(raw); err != nil {
		return err.Error()
	}
	return ""
}

// shapeClasses are the runs of characters disguise rotates within: a URL
// parser reads each member of one alike — a hex digit as a hex digit, any
// other letter as a letter — so a password disguised parses, or is
// refused, as the original is.
var shapeClasses = [][2]byte{{'0', '9'}, {'a', 'f'}, {'g', 'z'}, {'A', 'F'}, {'G', 'Z'}}

// disguise is password with each letter and digit swapped for the next of
// its shape class, and every other byte kept: a password of the same shape
// none of whose letters or digits is where it was. A refusal's own words
// read the same for both; a part of the password it quotes does not.
func disguise(password string) string {
	out := []byte(password)
	for i, c := range out {
		for _, class := range shapeClasses {
			if c >= class[0] && c <= class[1] {
				out[i] = class[0] + (c-class[0]+1)%(class[1]-class[0]+1)
				break
			}
		}
	}
	return string(out)
}
