package limes

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// The response freshness a JWKS fetch honours, after RFC 9111. The standard
// library sends and receives Cache-Control but parses none of it; this reads
// the two directives a key-set cache acts on, and the Age header, and nothing
// else.

// HTTP caching names (RFC 9111 §5.1, §5.2.2).
const (
	headerAge          = "Age"
	headerCacheControl = "Cache-Control"
	directiveMaxAge    = "max-age"
	directiveNoCache   = "no-cache"
	directiveNoStore   = "no-store"
)

// responseFreshness is how long a response stays fresh by its own headers:
// max-age less the Age it has already spent in caches (RFC 9111 §4.2.1,
// §4.2.3), zero for no-cache or no-store (§5.2.2.4, §5.2.2.5). ok is false
// when the response says nothing, and the caller's default applies.
func responseFreshness(h http.Header) (freshness time.Duration, ok bool) {
	maxAge, found := -1, false
	for _, line := range h.Values(headerCacheControl) {
		for directive := range strings.SplitSeq(line, ",") {
			name, value, _ := strings.Cut(strings.TrimSpace(directive), "=")
			switch strings.ToLower(name) {
			case directiveNoCache, directiveNoStore:
				return 0, true
			case directiveMaxAge:
				// §1.2.2: delta-seconds, unquoted or (§5.2) a quoted-string.
				n, err := strconv.Atoi(strings.Trim(value, `"`))
				if err != nil || n < 0 {
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
	age, err := strconv.Atoi(h.Get(headerAge))
	if err != nil || age < 0 {
		age = 0
	}
	return time.Duration(max(maxAge-age, 0)) * time.Second, true
}
