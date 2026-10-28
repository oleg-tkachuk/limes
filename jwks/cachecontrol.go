package jwks

import (
	"iter"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/oleg-tkachuk/limes/internal/httpx"
)

// The response freshness a JWKS fetch honours, after RFC 9111. The standard
// library sends and receives Cache-Control but parses none of it; this reads
// the two directives a key-set cache acts on, and the Age header, and nothing
// else.

// The delimiters of a header field's list (RFC 9110 §5.6.1, §5.6.4).
const (
	// listSeparator separates the members of a list: Cache-Control's
	// directives, and an Age sent more than once.
	listSeparator = ','
	// quote opens and closes a quoted-string, inside which listSeparator
	// separates nothing.
	quote = '"'
	// escape makes the byte after it, a quote among them, part of a
	// quoted-string.
	escape = '\\'
)

// responseFreshness is how long a response stays fresh by its own headers:
// max-age less the Age it has already spent in caches (RFC 9111 §4.2.1,
// §4.2.3), zero for no-cache or no-store (§5.2.2.4, §5.2.2.5). ok is false
// when the response says nothing, and the caller's default applies.
func responseFreshness(h http.Header) (freshness time.Duration, ok bool) {
	maxAge, found := int64(-1), false
	for _, line := range h.Values(httpx.HeaderCacheControl) {
		for directive := range members(line) {
			name, value, _ := strings.Cut(strings.TrimSpace(directive), "=")
			switch strings.ToLower(name) {
			case httpx.DirectiveNoCache, httpx.DirectiveNoStore:
				return 0, true
			case httpx.DirectiveMaxAge:
				if found {
					continue // §4.2.1: the first max-age stands
				}
				n, ok := deltaSeconds(unquoted(value))
				if !ok {
					// §4.2.1: an invalid max-age makes the response stale.
					return 0, true
				}
				maxAge, found = n, true
			}
		}
	}
	if !found {
		return 0, false
	}
	// §5.1: of an Age sent as a list, the first member.
	first, _, _ := strings.Cut(h.Get(httpx.HeaderAge), string(listSeparator))
	age, ok := deltaSeconds(strings.TrimSpace(first))
	if !ok {
		age = 0
	}
	return time.Duration(max(maxAge-age, 0)) * time.Second, true
}

// members yields line's list members, a listSeparator inside a quoted-string
// separating none (RFC 9110 §5.6.4). A quoted-string never closed runs to the
// end of line.
func members(line string) iter.Seq[string] {
	return func(yield func(string) bool) {
		start, quoted, escaped := 0, false, false
		for i := range len(line) {
			switch c := line[i]; {
			case escaped:
				escaped = false
			case quoted && c == escape:
				escaped = true
			case c == quote:
				quoted = !quoted
			case !quoted && c == listSeparator:
				if !yield(line[start:i]) {
					return
				}
				start = i + 1
			}
		}
		yield(line[start:])
	}
}

// unquoted is value without its quotes when it is a quoted-string: a quote at
// each end, two of them. One quote alone is part of the value, and makes it
// invalid wherever a token is (RFC 9111 §5.2).
func unquoted(value string) string {
	if len(value) >= len(`""`) && value[0] == quote && value[len(value)-1] == quote {
		return value[1 : len(value)-1]
	}
	return value
}

// maxDeltaSeconds is what RFC 9111 §1.2.2 reads a delta-seconds too large to
// hold as: 2^31. Typed, so it means the same on a 32-bit int.
const maxDeltaSeconds int64 = 1 << 31

// deltaSeconds reads an RFC 9111 §1.2.2 delta-seconds: digits only, and any
// value past maxDeltaSeconds read as it.
func deltaSeconds(s string) (int64, bool) {
	if s == "" || strings.Trim(s, "0123456789") != "" {
		return 0, false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n > maxDeltaSeconds {
		return maxDeltaSeconds, true // only digits, so too large
	}
	return n, true
}
