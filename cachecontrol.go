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

// HTTP header names the module reads or writes (RFC 9110, RFC 9111 §5),
// spelled once.
const (
	headerAccept        = "Accept"
	headerAge           = "Age"
	headerAllow         = "Allow"
	headerCacheControl  = "Cache-Control"
	headerContentLength = "Content-Length"
	headerContentType   = "Content-Type"
	headerETag          = "ETag"
	headerIfNoneMatch   = "If-None-Match"
)

// Cache-Control directives (RFC 9111 §5.2).
const (
	directiveMaxAge  = "max-age"
	directiveNoCache = "no-cache"
	directiveNoStore = "no-store"
	directivePublic  = "public"
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
				if found {
					continue // §4.2.1: the first max-age stands
				}
				n, ok := deltaSeconds(strings.Trim(value, `"`))
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
	age, ok := deltaSeconds(h.Get(headerAge))
	if !ok {
		age = 0
	}
	return time.Duration(max(maxAge-age, 0)) * time.Second, true
}

// maxDeltaSeconds is what RFC 9111 §1.2.2 reads a delta-seconds too large to
// hold as: 2^31.
const maxDeltaSeconds = 1 << 31

// deltaSeconds reads an RFC 9111 §1.2.2 delta-seconds: digits only, and any
// value past maxDeltaSeconds read as it.
func deltaSeconds(s string) (int, bool) {
	if s == "" || strings.Trim(s, "0123456789") != "" {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	if err != nil || n > maxDeltaSeconds {
		return maxDeltaSeconds, true // only digits, so too large
	}
	return n, true
}
