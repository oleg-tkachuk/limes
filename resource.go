package limes

import "strings"

// MatchResource reports whether uri falls under prefix, matched at a path-
// segment boundary: "corpus/public" covers "corpus/public" and
// "corpus/public/x", but not "corpus/public-secret". A prefix that already
// ends in "/" matches anything beneath it. An empty prefix matches nothing —
// a caveat that means "everything" is written by leaving the list empty, not
// by an empty entry, so a stray "" can never silently widen a capability.
//
// This is the single definition of a resource match. Delegation narrowing and
// request-time enforcement both go through it, so the two can never disagree
// about what a prefix reaches.
func MatchResource(prefix, uri string) bool {
	if prefix == "" || !strings.HasPrefix(uri, prefix) {
		return false
	}
	if len(uri) == len(prefix) || strings.HasSuffix(prefix, "/") {
		return true
	}
	return uri[len(prefix)] == '/'
}

// RestrictsResources reports whether the caveats confine the capability to a
// set of resources. False means "any resource within the tenant".
func (c Caveats) RestrictsResources() bool {
	return len(c.ResourcePrefixes) > 0 || len(c.ResourceURIs) > 0
}

// AllowsResource reports whether uri is reachable under the resource caveats:
// unrestricted caveats allow any non-empty uri; restricted ones require an
// exact ResourceURIs entry or a covering ResourcePrefixes entry. An empty uri
// is never allowed by restricted caveats — an operation that cannot name what
// it touches cannot be shown to stay inside the scope.
func (c Caveats) AllowsResource(uri string) bool {
	if !c.RestrictsResources() {
		return true
	}
	if uri == "" {
		return false
	}
	for _, exact := range c.ResourceURIs {
		if exact == uri {
			return true
		}
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
