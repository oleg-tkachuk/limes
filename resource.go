package limes

import (
	"slices"
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

// percentEncoded is a "%" percent-encoded: "%252e" decodes once to "%2e"
// and twice to ".".
const percentEncoded = "%25"

// segmentSeparators read as a "/" when looking for a dot segment: the
// percent-encoded slash, and the backslash and its encoding, which a consumer
// that decodes before it splits, or a backend that takes either slash, treats
// as one.
var segmentSeparators = strings.NewReplacer("%2f", "/", `\`, "/", "%5c", "/")

// hasDotSegment reports whether s has a segment that is "." or "..",
// counting "%2e" (any case) as a dot and every segmentSeparators entry as a
// "/", each also encoded once more ("%252e", "%252f") — the segments RFC 3986
// resolves away, which make a resource's text name somewhere other than it
// reads, also for a consumer that decodes before it resolves, once or twice. Caveats.Validate refuses a resource with one, and no request
// naming one matches a restricted capability.
func hasDotSegment(s string) bool {
	decoded := strings.ReplaceAll(strings.ToLower(s), percentEncoded, "%")
	for seg := range strings.SplitSeq(segmentSeparators.Replace(decoded), "/") {
		switch strings.ReplaceAll(seg, dotSegmentEncoded, ".") {
		case ".", "..":
			return true
		}
	}
	return false
}

// RestrictsResources reports whether the caveats confine the capability to a
// set of resources. False means "any resource within the tenant".
func (c Caveats) RestrictsResources() bool {
	return len(c.ResourcePrefixes) > 0 || len(c.ResourceURIs) > 0
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
	return false
}

// coversPrefix reports whether every resource under child is reachable under
// the parent caveats. Only a parent prefix can cover a child prefix: an exact
// URI names one resource, while a prefix names an open-ended set beneath it.
func (c Caveats) coversPrefix(child string) bool {
	for _, p := range c.ResourcePrefixes {
		if MatchResource(p, child) {
			return true
		}
	}
	return false
}
