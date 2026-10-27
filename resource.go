package limes

import (
	"slices"
	"strconv"
	"strings"
)

// MatchResource reports whether uri falls under prefix, matched at a path-
// segment boundary: "corpus/public" covers "corpus/public" and
// "corpus/public/x", but not "corpus/public-secret". A prefix that already
// ends in "/" matches anything beneath it. An empty prefix matches nothing —
// a caveat that means "everything" is written by leaving the list empty, not
// by an empty entry, so a stray "" can never silently widen a capability.
//
// A uri with a dot segment — "." or "..", percent-encoded or not, between
// slashes that may be encoded or backslashes — matches nothing: what it names depends on how the consumer resolves it, so the
// match it seems to make by its text proves nothing about where it leads.
// That covers a consumer that percent-decodes at most twice
// before it resolves the path, and no other notation: one that decodes more
// often, or whose backend resolves Unicode look-alikes (U+FF0E), overlong
// UTF-8 (%c0%ae), %u escapes or trailing dots and spaces as a dot segment,
// canonicalises the resource itself before it asks.
//
// This is the single definition of a resource match. Delegation narrowing and
// request-time enforcement both go through it, so the two can never disagree
// about what a prefix reaches.
func MatchResource(prefix, uri string) bool {
	if prefix == "" || !strings.HasPrefix(uri, prefix) || hasDotSegment(uri) {
		return false
	}
	if len(uri) == len(prefix) || strings.HasSuffix(prefix, "/") {
		return true
	}
	return uri[len(prefix)] == '/'
}

// dotSegmentEncoded is a "." percent-encoded, in lower case.
const dotSegmentEncoded = "%2e"

// pathParameters starts a segment's path parameters (RFC 3986 §3.3), and
// pathParametersEncoded is it percent-encoded, in lower case. A servlet-style
// backend drops a segment's parameters before it resolves the segment, so
// "..;x" is a ".." to it.
const (
	pathParameters        = ";"
	pathParametersEncoded = "%3b"
)

// consumerDecodes is how many times a consumer may percent-decode a resource
// before it resolves the path: the check below looks for a dot segment in
// the resource as given and after each of these decodings.
const consumerDecodes = 2

// segmentSeparators read as a "/" when looking for a dot segment: the
// percent-encoded slash, and the backslash and its encoding, which a consumer
// that decodes before it splits, or a backend that takes either slash, treats
// as one.
var segmentSeparators = strings.NewReplacer("%2f", "/", `\`, "/", "%5c", "/")

// hasDotSegment reports whether s has a segment that is "." or "..",
// counting "%2e" (any case) as a dot and every segmentSeparators entry as a
// "/", and reading a segment up to its path parameters ("..;x" is "..") —
// the segments RFC 3986 resolves away, which make a resource's text name
// somewhere other than it reads — in s as given and after each of up to
// consumerDecodes percent-decodings, so also for a consumer that decodes
// before it resolves, once or twice, however the second escape is spelled
// ("%252e", "%25%32%65"). Caveats.Validate refuses a resource with one, and
// no request naming one matches a restricted capability.
func hasDotSegment(s string) bool {
	for pass := 0; ; pass++ {
		for seg := range strings.SplitSeq(segmentSeparators.Replace(strings.ToLower(s)), "/") {
			seg = strings.ReplaceAll(seg, pathParametersEncoded, pathParameters)
			seg, _, _ = strings.Cut(seg, pathParameters)
			switch strings.ReplaceAll(seg, dotSegmentEncoded, ".") {
			case ".", "..":
				return true
			}
		}
		if pass == consumerDecodes {
			return false
		}
		s = percentDecode(s)
	}
}

// percentDecode decodes every well-formed "%XX" in s and keeps any other "%"
// as it is, the way a lenient consumer does, so a stray "%" can neither hide
// a dot segment nor make a resource unreadable.
func percentDecode(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) {
			if v, err := strconv.ParseUint(s[i+1:i+3], 16, 8); err == nil {
				b.WriteByte(byte(v))
				i += 2
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// MatchResourceName reports whether uri starts with prefix, as strings and
// across segments: "drafts/billing-" covers "drafts/billing-q3" and
// "drafts/billing-q3/notes", but not "drafts/billing". Like MatchResource, an
// empty prefix matches nothing, and neither does a uri with a dot segment.
// It is the single definition of a name-prefix match, for requests and for
// delegation alike.
func MatchResourceName(prefix, uri string) bool {
	return prefix != "" && strings.HasPrefix(uri, prefix) && !hasDotSegment(uri)
}

// RestrictsResources reports whether the caveats confine the capability to a
// set of resources. False means "any resource within the tenant".
func (c Caveats) RestrictsResources() bool {
	return len(c.ResourcePrefixes) > 0 || len(c.ResourceURIs) > 0 || len(c.ResourceNamePrefixes) > 0
}

// AllowsResource reports whether uri is reachable under the resource caveats:
// unrestricted caveats allow any uri; restricted ones require an
// exact ResourceURIs entry or a covering ResourcePrefixes entry. An empty uri,
// or one with a "." or ".." path segment, is never allowed by restricted
// caveats — an operation that cannot name what it touches cannot be shown to
// stay inside the scope.
func (c Caveats) AllowsResource(uri string) bool {
	if !c.RestrictsResources() {
		return true
	}
	if uri == "" || hasDotSegment(uri) {
		return false
	}
	if slices.Contains(c.ResourceURIs, uri) {
		return true
	}
	for _, prefix := range c.ResourcePrefixes {
		if MatchResource(prefix, uri) {
			return true
		}
	}
	for _, prefix := range c.ResourceNamePrefixes {
		if MatchResourceName(prefix, uri) {
			return true
		}
	}
	return false
}

// coversPrefix reports whether every resource under the child segment prefix
// child is reachable under the parent caveats. Only a parent prefix can cover
// a child prefix: an exact URI names one resource, while a prefix names an
// open-ended set beneath it. Every resource under child starts with child, so
// a parent name prefix child starts with covers it.
func (c Caveats) coversPrefix(child string) bool {
	for _, p := range c.ResourcePrefixes {
		if MatchResource(p, child) {
			return true
		}
	}
	for _, n := range c.ResourceNamePrefixes {
		if MatchResourceName(n, child) {
			return true
		}
	}
	return false
}

// coversNamePrefix reports whether every resource starting with the child
// name prefix child is reachable under the parent caveats:
//
//   - under a parent name prefix that child starts with;
//   - under a parent segment prefix p only when child reaches past p's
//     boundary: child starts with p+"/", or p ends in "/" and child starts
//     with it. A child equal to p is not covered: "a" names "ab", which the
//     segment prefix "a" does not reach;
//   - under no parent URI: a name prefix names an open-ended set.
func (c Caveats) coversNamePrefix(child string) bool {
	for _, n := range c.ResourceNamePrefixes {
		if MatchResourceName(n, child) {
			return true
		}
	}
	for _, p := range c.ResourcePrefixes {
		if MatchResource(p, child) && (child != p || strings.HasSuffix(p, "/")) {
			return true
		}
	}
	return false
}
